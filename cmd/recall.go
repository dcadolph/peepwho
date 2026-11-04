package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/render"
)

// meEnv names the environment variable identifying who is asking, so the
// answer can be scoped to that person's own conversations.
const meEnv = "PEEPWHO_ME"

// newRecallCmd builds the recall command, which finds the conversation where
// something was worked out before.
func newRecallCmd(opts *options) *cobra.Command {
	var (
		limit      int
		me         string
		horizon    int
		meaning    bool
		explain    bool
		embedModel string
		model      string
		ollamaURL  string
		provider   string
		openaiURL  string
	)
	cmd := &cobra.Command{
		Use:   "recall [question]",
		Short: "Find when you worked through something before",
		Long: `Find the past conversation where something was worked out, and who was in it.

An answer is a pointer, not a transcript: the people, the place, the date, and a
link back to the conversation in the tool it happened in. Opening the link uses
your own access to that tool.

Results cover only conversations you took part in. Identify yourself with --me,
PEEPWHO_ME, or leave it unset to use your git email.

--me scopes the answer; it is not a login. Anyone who can read the index file can
ask as anyone in it, so treat the index the way you would treat the exports it was
built from.

Examples:
  peepwho recall "certificate renewal"
  peepwho recall --me jane@example.com "kafka consumer lag"`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			res := engine.New(ix, opts.pol).Recaller(store)
			if meaning {
				if err := engine.GuardLLMHost(opts.pol, ollamaURL); err != nil {
					return err
				}
				res.SetEmbedder(engine.NewOllama("", embedModel, ollamaURL))
				if !res.Semantic() {
					return fmt.Errorf(
						"%w: no conversation was embedded; re-index with --episodes --embed", ErrBadArgs)
				}
			}
			if explain {
				if err := attachSummarizer(opts, res, model, ollamaURL, provider, openaiURL); err != nil {
					return err
				}
			}
			if horizon > 0 {
				res.SetHorizon(time.Duration(horizon) * 24 * time.Hour)
			}
			person, err := resolveMe(res, me)
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			ans := res.Resolve(cmd.Context(),
				recall.Query{
					Text: query, Person: person, Limit: limit, Meaning: meaning, Explain: explain,
				})
			warnEmptyRecall(cmd, res, ans, person)
			warnUnexplained(cmd, ans, explain)
			return opts.render(cmd.OutOrStdout(), ans, func(w io.Writer, s style) {
				render.RenderRecall(w, ans, s)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 5, "Maximum conversations to return.")
	cmd.Flags().StringVar(&me, "me", "",
		"Who is asking: an email or a source identifier. Defaults to "+meEnv+", then your git email.")
	cmd.Flags().BoolVar(&meaning, "meaning", false,
		"Match by meaning instead of exact words. Needs an index built with --episodes --embed.")
	cmd.Flags().BoolVar(&explain, "how", false,
		"Show how it was worked out, for conversations peepwho keeps. Needs --archive at index time.")
	cmd.Flags().StringVar(&model, "model", "", "Ollama chat model that writes the account for --how.")
	cmd.Flags().StringVar(&provider, "provider", "ollama",
		"Model provider for --how: ollama, anthropic, openai, or gemini. Cloud providers need --policy open.")
	cmd.Flags().StringVar(&openaiURL, "openai-url", "",
		"OpenAI-compatible base URL, e.g. a local LM Studio or vLLM server.")
	cmd.Flags().StringVar(&embedModel, "embed-model", "", "Ollama embed model for --meaning.")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL.")
	cmd.Flags().IntVar(&horizon, "link-horizon-days", 0,
		"Warn that links older than this many days may have expired. Zero makes no claim.")
	return cmd
}

// attachSummarizer wires the model that writes how a conversation resolved.
// The text it sees is conversation content, so a cloud provider is allowed
// only under the open policy, exactly as it is for an answer that quotes
// candidate detail. A local model is loopback and needs no opt-in.
func attachSummarizer(
	opts *options, res *recall.Resolver,
	model, ollamaURL, provider, openaiURL string,
) error {
	switch provider {
	case "", "ollama":
		// Guard the host the request actually goes to. Checking one URL and
		// sending to another would let conversation content reach an
		// unchecked machine.
		if err := engine.GuardLLMHost(opts.pol, ollamaURL); err != nil {
			return err
		}
		res.SetSummarizer(engine.NewOllama(model, "", ollamaURL))
		return nil
	default:
		// The redacted policy promises a model never sees message text, and a
		// kept conversation is nothing but message text. So this path needs
		// the open policy, not merely a known provider.
		if opts.pol.Mode() != policy.Open {
			return fmt.Errorf(
				"%w: --how sends the conversation itself to %s, which needs --policy open",
				policy.ErrEgressDenied, provider)
		}
		chat, err := engine.CloudChatter(opts.pol, provider, model, openaiURL)
		if err != nil {
			return err
		}
		res.SetSummarizer(chat)
		return nil
	}
}

// warnUnexplained says why --how produced no written account. The words of a
// conversation are kept only by an --archive index, so a run without them
// returns ordinary results and the flag would otherwise look like it did
// nothing at all.
func warnUnexplained(cmd *cobra.Command, ans recall.Answer, explain bool) {
	if !explain || len(ans.Episodes) == 0 {
		return
	}
	for _, ep := range ans.Episodes {
		if ep.Solution != nil {
			return
		}
	}
	fmt.Fprintln(cmd.ErrOrStderr(),
		"--how found no kept conversation to work from. The words are kept by "+
			"`peepwho index --episodes --archive`. Without them, "+
			"recall still names who helped and links back to the conversation itself.")
}

// resolveMe determines who is asking, preferring the flag, then the
// environment, then the git email. Recall is scoped to one person, so an
// unidentifiable caller is an error rather than an org-wide search.
func resolveMe(res *recall.Resolver, flag string) (model.ID, error) {
	for _, hint := range []string{flag, os.Getenv(meEnv), gitEmail()} {
		if id := res.Who(hint); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf(
		"%w: pass --me, set %s, or configure git user.email", ErrNoIdentity, meEnv)
}

// gitEmail returns the configured git email, which is usually the same address
// the work tools know a person by. It returns an empty string when git is
// absent or unconfigured.
func gitEmail() string {
	out, err := exec.Command("git", "config", "--get", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// warnEmptyRecall explains an empty answer on stderr, separating a question
// that matched nothing from history that was never indexed or never included
// the asker. Silence would read as "this never happened".
func warnEmptyRecall(cmd *cobra.Command, res *recall.Resolver, ans recall.Answer, person model.ID) {
	if len(ans.Episodes) > 0 {
		return
	}
	w := cmd.ErrOrStderr()
	switch {
	case res.Len() == 0:
		fmt.Fprintln(w,
			"No conversations indexed yet: run `peepwho index --source slack --episodes`.")
	case !res.Known(person):
		fmt.Fprintf(w,
			"No indexed conversation includes %s. Check --me, and that the bot can read "+
				"the channels you talk in.\n", person)
	default:
		fmt.Fprintln(w,
			"No match in your conversations. Try other words: matching is on the words used "+
				"at the time, and direct messages are not indexed.")
	}
}
