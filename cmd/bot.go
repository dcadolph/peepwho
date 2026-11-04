package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/bot"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/resolve"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/slack"
)

// Bot environment variables. The bot token is shared with the index command.
const (
	// slackAppTokenEnv holds the app-level token (xapp-) for socket mode.
	slackAppTokenEnv = "PEEPWHO_SLACK_APP_TOKEN"
	// slackSigningSecretEnv holds the signing secret for the events transport.
	slackSigningSecretEnv = "PEEPWHO_SLACK_SIGNING_SECRET"
)

// slackReplier posts bot answers through the Slack Web API.
type slackReplier struct {
	// client posts messages.
	client *slack.Client
}

// Reply posts text to channel, threading under threadTS when set.
func (s slackReplier) Reply(ctx context.Context, channel, threadTS, text string) error {
	return s.client.PostMessage(ctx, channel, threadTS, text)
}

// ReplyPrivately posts text visible only to user, which is how recall answers
// are delivered so one person's history is never shown to a channel.
func (s slackReplier) ReplyPrivately(ctx context.Context, channel, user, text string) error {
	return s.client.PostEphemeral(ctx, channel, user, text)
}

// slackResponder posts slash-command answers, publicly for questions about who
// knows something and privately for recall.
type slackResponder struct{}

// Respond posts text to a response URL for the whole channel to see.
func (slackResponder) Respond(ctx context.Context, responseURL, text string) error {
	return slack.Respond(ctx, responseURL, text)
}

// RespondPrivately posts text to a response URL for the caller alone.
func (slackResponder) RespondPrivately(ctx context.Context, responseURL, text string) error {
	return slack.RespondPrivately(ctx, responseURL, text)
}

// newBotCmd builds the bot command, which answers questions from Slack over
// Socket Mode or the Events API.
func newBotCmd(opts *options) *cobra.Command {
	var (
		transport  string
		mode       string
		model      string
		embedModel string
		provider   string
		openaiURL  string
		fbStrength string
		ollamaURL  string
		addr       string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "bot",
		Short: "Run the Slack bot",
		Long: `Run the Slack bot. Mention it in a channel or send it a direct message; a
trailing --llm or --keyword in a message overrides the mode for that answer.

Transports and their credentials:
  socket  no public URL needed     PEEPWHO_SLACK_TOKEN + PEEPWHO_SLACK_APP_TOKEN
  events  serves HTTP for Slack    PEEPWHO_SLACK_TOKEN + PEEPWHO_SLACK_SIGNING_SECRET`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			botToken := secret.Resolve(slackTokenEnv)
			if botToken == "" {
				return fmt.Errorf("%w: set %s", ErrBadArgs, slackTokenEnv)
			}
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			eng := engine.New(ix, opts.pol)
			applyFeedback(eng, opts, cmd.ErrOrStderr())
			if err := eng.SetFeedbackStrength(fbStrength); err != nil {
				return err
			}

			botClient := slack.New(botToken)
			auth, err := botClient.AuthTest(cmd.Context())
			if err != nil {
				return fmt.Errorf("slack auth: %w", err)
			}
			botUserID := auth.UserID

			ask := func(ctx context.Context, query, reqMode string, n int) (resolve.Answer, error) {
				if reqMode == "" {
					reqMode = mode
				}
				return eng.Ask(ctx, query, n, engine.ResolverConfig{
					Mode: reqMode, Model: model, EmbedModel: embedModel, OllamaURL: ollamaURL,
					Provider: provider, OpenAIURL: openaiURL,
				})
			}
			// Recall is scoped to whoever typed the message: the Slack user
			// id resolves to a person, and the store only returns
			// conversations that person took part in.
			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			res := eng.Recaller(store)
			recallFn := func(ctx context.Context, user, query string, n int) (recall.Answer, error) {
				person := res.Who("slack:" + user)
				if person == "" {
					return recall.Answer{}, nil
				}
				return res.Resolve(ctx, recall.Query{Text: query, Person: person, Limit: n}), nil
			}
			engine := bot.New(ask, mode, botUserID, limit, bot.WithRecall(recallFn))
			replier := slackReplier{client: botClient}

			switch transport {
			case "", "socket":
				return runSocketBot(cmd, engine, replier, botUserID)
			case "events":
				return runEventsBot(cmd, engine, replier, botUserID, addr)
			default:
				return fmt.Errorf("%w: transport %q (want socket or events)", ErrBadArgs, transport)
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&transport, "transport", "socket", "Slack transport: socket or events.")
	f.StringVar(&mode, "mode", "keyword", "Default answer mode: keyword, semantic, or llm.")
	f.StringVar(&model, "model", "", "Ollama chat model for llm mode.")
	f.StringVar(&embedModel, "embed-model", "", "Ollama embed model for semantic/llm mode.")
	f.StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL.")
	f.StringVar(&provider, "provider", "ollama",
		"LLM provider: ollama, anthropic, openai, or gemini. Cloud providers need --policy redacted or open.")
	f.StringVar(&openaiURL, "openai-url", "",
		"OpenAI-compatible base URL, e.g. a local LM Studio or vLLM server.")
	f.StringVar(&fbStrength, "feedback", "normal",
		"How hard votes move ranking: off, low, normal, or high.")
	f.StringVar(&addr, "addr", "127.0.0.1:8766", "Address for the events transport HTTP server.")
	f.IntVar(&limit, "limit", 5, "Maximum results per section.")
	return cmd
}

// runSocketBot runs the bot over Socket Mode until interrupted.
func runSocketBot(cmd *cobra.Command, engine *bot.Engine, replier bot.Replier, botUserID string) error {
	appToken := os.Getenv(slackAppTokenEnv)
	if appToken == "" {
		return fmt.Errorf("%w: set %s for socket transport", ErrBadArgs, slackAppTokenEnv)
	}
	runner := bot.NewSocketRunner(slack.New(appToken), engine, replier, botUserID,
		bot.WithLog(cmd.ErrOrStderr()), bot.WithResponder(slackResponder{}))
	fmt.Fprintln(cmd.ErrOrStderr(), "peepwho bot: connected over socket mode (Ctrl-C to stop)")
	return runner.Run(cmd.Context())
}

// runEventsBot serves the Slack Events API until interrupted.
func runEventsBot(cmd *cobra.Command, engine *bot.Engine, replier bot.Replier, botUserID, addr string) error {
	secret := os.Getenv(slackSigningSecretEnv)
	if secret == "" {
		return fmt.Errorf("%w: set %s for events transport", ErrBadArgs, slackSigningSecretEnv)
	}
	handler := bot.NewEventsHandler(engine, replier, botUserID, secret, bot.WithEventsLog(cmd.ErrOrStderr()))
	slash := bot.NewSlashHandler(engine, slackResponder{}, secret,
		bot.WithSlashLog(cmd.ErrOrStderr()))
	mux := http.NewServeMux()
	mux.Handle("/slack/events", handler)
	mux.Handle("/slack/commands", slash)
	// This server must be reachable from the internet for Slack to deliver
	// events, so the full timeout set applies: a header-only bound would let a
	// client send headers fast, drip the body forever, and hold a goroutine
	// each time, with idle keep-alives never reaped. Slack posts small bodies
	// and expects an answer within seconds, so these bounds cost nothing real.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx := cmd.Context()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(cmd.ErrOrStderr(),
		"peepwho bot: events endpoint on http://%s/slack/events (Ctrl-C to stop)\n", addr)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fmt.Fprintln(cmd.ErrOrStderr(), "peepwho bot: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
