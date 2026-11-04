package cmd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/report"
	"github.com/dcadolph/peepwho/internal/resolve"
	"github.com/dcadolph/peepwho/internal/seal"
	"github.com/dcadolph/peepwho/internal/web"
)

// shutdownTimeout bounds how long serve waits for in-flight requests to finish.
const shutdownTimeout = 5 * time.Second

// serveTokenEnv holds the bearer token that gates every request when the web
// UI binds beyond localhost. Serving off-loopback without it is refused.
const serveTokenEnv = "PEEPWHO_SERVE_TOKEN"

// webConfig carries the resolver settings shared by serve and demo.
type webConfig struct {
	// addr is the listen address.
	addr string
	// mode is the default resolver mode.
	mode string
	// model is the chat model for llm mode.
	model string
	// embedModel is the Ollama embed model.
	embedModel string
	// ollamaURL is the Ollama base URL.
	ollamaURL string
	// provider is the llm-mode provider: ollama, anthropic, openai, or gemini.
	provider string
	// openaiURL is an OpenAI-compatible base URL for the openai provider.
	openaiURL string
	// episodes holds the conversations recall answers from; nil disables it.
	episodes *episode.Store
	// recallMe is the identity the recall view starts with.
	recallMe string
	// fbStrength is how hard votes move ranking.
	fbStrength string
	// places are the directories the indexed work rests on. They are supplied
	// by whoever built the index, because the tally comes from the git
	// connector rather than from the graph, and are empty when no repository
	// was indexed.
	places []resolve.Place
	// public serves open to any caller with no token, even off loopback. It is
	// meant only for the demo, whose index and recall are sample data with
	// nothing private to protect.
	public bool
}

// newServeCmd builds the serve command, which runs the web UI on localhost and
// shuts down cleanly on interrupt.
func newServeCmd(opts *options) *cobra.Command {
	var cfg webConfig
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the peepwho web UI on localhost",
		Long: `Serve the local web UI over the same engine as ask. Binds to localhost by
default, so nothing leaves the machine. Queries are shareable links
(/?q=who+owns+billing runs on load) and every result has feedback buttons.

The AI mode's provider row shows live readiness. Local Ollama works under the
default strict policy. Cloud providers (Claude, ChatGPT, Gemini) need their
key exported (PEEPWHO_ANTHROPIC_KEY, PEEPWHO_OPENAI_KEY, PEEPWHO_GEMINI_KEY) and
--policy redacted or open, since strict keeps everything on this machine.

Binding beyond localhost requires PEEPWHO_SERVE_TOKEN; every request must then
carry the token as a bearer header or a token query parameter, which sets a
session cookie. Put TLS in front of it for anything beyond a trusted network.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			if cfg.episodes, err = opts.loadEpisodes(cmd); err != nil {
				return err
			}
			if cfg.recallMe = os.Getenv(meEnv); cfg.recallMe == "" {
				cfg.recallMe = gitEmail()
			}
			eng := engine.New(ix, opts.pol)
			store := applyFeedback(eng, opts, cmd.ErrOrStderr())
			return serveWeb(cmd, opts, eng, store, cfg)
		},
	}
	addWebFlags(cmd, &cfg, "127.0.0.1:8765")
	return cmd
}

// addWebFlags registers the shared web-serving flags on cmd.
func addWebFlags(cmd *cobra.Command, cfg *webConfig, defaultAddr string) {
	f := cmd.Flags()
	f.StringVar(&cfg.addr, "addr", defaultAddr, "Address to listen on.")
	f.StringVar(&cfg.mode, "mode", "keyword", "Default resolver: keyword, semantic, or llm.")
	f.StringVar(&cfg.model, "model", "", "Ollama chat model for llm mode.")
	f.StringVar(&cfg.embedModel, "embed-model", "", "Ollama embed model for semantic/llm mode.")
	f.StringVar(&cfg.ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL.")
	f.StringVar(&cfg.provider, "provider", "ollama",
		"LLM provider: ollama, anthropic, openai, or gemini. Cloud providers need --policy redacted or open.")
	f.StringVar(&cfg.openaiURL, "openai-url", "",
		"OpenAI-compatible base URL, e.g. a local LM Studio or vLLM server.")
	f.StringVar(&cfg.fbStrength, "feedback", "normal",
		"How hard votes move ranking: off, low, normal, or high.")
}

// recallFn returns the web recall handler, or nil when recall is unavailable.
// An answer is scoped to the person the request names, and the web app cannot
// prove who is asking: a serve token gates the server, not one person's
// history, and one token cannot tell two people apart. So recall is served
// only where the caller can only be the person running peepwho: a loopback bind
// with no token. A token means the server is shared, whether directly or
// behind a proxy that forwards to loopback, and recall stays off. The demo is
// the exception: its history is sample data, so it serves recall openly.
func recallFn(eng *engine.Engine, cfg webConfig, token string) web.RecallFunc {
	store := cfg.episodes
	if store == nil || store.Len() == 0 || token != "" || (!loopbackAddr(cfg.addr) && !cfg.public) {
		return nil
	}
	res := eng.Recaller(store)
	return func(ctx context.Context, person, query string, limit int) (recall.Answer, error) {
		who := res.Who(person)
		if who == "" {
			return recall.Answer{}, fmt.Errorf("%w: name who is asking", web.ErrBadRequest)
		}
		return res.Resolve(ctx, recall.Query{Text: query, Person: who, Limit: limit}), nil
	}
}

// serveWeb runs the web UI over ix until interrupted. A nil store disables
// the feedback API. Binding beyond localhost requires the serve token, which
// then gates every request, unless cfg.public marks the index as sample data.
func serveWeb(cmd *cobra.Command, opts *options, eng *engine.Engine, store *feedback.Store, cfg webConfig) error {
	token := os.Getenv(serveTokenEnv)
	if !loopbackAddr(cfg.addr) && token == "" && !cfg.public {
		return fmt.Errorf("%w: %s binds beyond localhost; set %s so every request needs a token",
			ErrBadArgs, cfg.addr, serveTokenEnv)
	}
	if err := eng.SetFeedbackStrength(cfg.fbStrength); err != nil {
		return err
	}
	ix := eng.Index()
	ask := func(ctx context.Context, query, reqMode, reqProvider string, limit int) (resolve.Answer, error) {
		if reqMode == "" {
			reqMode = cfg.mode
		}
		provider := cfg.provider
		if reqProvider != "" {
			provider = reqProvider
		}
		res, err := eng.Resolver(engine.ResolverConfig{
			Mode: reqMode, Model: cfg.model, EmbedModel: cfg.embedModel, OllamaURL: cfg.ollamaURL,
			Provider: provider, OpenAIURL: cfg.openaiURL,
		})
		if err != nil {
			if errors.Is(err, ErrBadArgs) {
				err = fmt.Errorf("%w: %w", web.ErrBadRequest, err)
			}
			return resolve.Answer{}, err
		}
		return res.Resolve(ctx, query, limit)
	}
	// A public server takes votes from anyone, and every vote re-ranks the
	// shared index and lands on disk, so voting stays off there.
	var vote web.FeedbackFunc
	if store != nil && !cfg.public {
		vote = func(e feedback.Entry) error {
			if err := store.Add(e); err != nil {
				return err
			}
			eng.SetFeedback(store)
			return nil
		}
	}
	person := eng.Person
	dir := resolve.BuildDirectory(ix)
	modes := func(ctx context.Context) web.ModesReport {
		return modeReadiness(ctx, ix, opts.pol, cfg)
	}

	// One sealer for every surface. Built twice, the two would warn twice and,
	// when no key can be kept, generate DIFFERENT keys: the same finding would
	// then be signed under one identity through the API and another through the
	// command-line view, which is worse than not signing it.
	sealer := attestFn(ix, opts, cmd.ErrOrStderr())

	handler, err := web.Handler(web.Config{
		Ask: ask, Feedback: vote, Person: person, Version: version, AuthToken: token,
		LocalOnly: localOnlyServe(token, cfg.public, cfg.addr),
		Directory: &dir, Modes: modes, Recall: recallFn(eng, cfg, token), RecallMe: cfg.recallMe,
		Search: eng.Search,
		Exposure: func() web.Exposure {
			return web.Exposure{
				Places:  cfg.places,
				Risk:    resolve.Risk(ix, 0),
				Drift:   resolve.Ownership(ix).Drift,
				Regions: resolve.Regions(ix, regionsShown),
				Spans:   resolve.SoleSpans(ix, spansShown),
			}
		},
		Brief: func() report.Brief {
			all := resolve.Risk(ix, 0)
			exposed := report.Exposures(all)
			return report.Brief{
				Generated: time.Now(),
				People:    len(ix.Graph.People),
				Scored:    len(all),
				Sources:   ix.SourceNames(),
				Risks:     all,
				Regions:   resolve.Regions(ix, regionsShown),
				Spans:     resolve.SoleSpans(ix, spansShown),
				Totals:    report.Count(all, exposed),
				Exposed:   exposed,
			}
		},
		Departure: func(person string) resolve.DepartureImpact {
			return resolve.Departure(ix, person)
		},
		Attest: sealer,
		Related: func(topic string, limit int) []resolve.TopicRelation {
			return resolve.Related(ix, topic, limit)
		},
		CLI:   cliFn(ix, ask, sealer),
		Log:   cmd.ErrOrStderr(),
		Ready: func() bool { return ix != nil && len(ix.Graph.People) > 0 },
	})
	if err != nil {
		return err
	}
	// The write timeout leaves room for llm-mode asks, which wait on a local
	// model; the other bounds keep slow or idle clients from holding
	// connections open on a shared box.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Bind before announcing. Serving is not worth printing until the port is
	// actually held, and a bind failure underneath a success line reads as a
	// server that started and then died.
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("%w: cannot bind %s: %w: pass --addr to use a free port", ErrServe, cfg.addr, err)
	}

	ctx := cmd.Context()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	fmt.Fprintf(cmd.ErrOrStderr(), "peepwho serving on http://%s (Ctrl-C to stop)\n", cfg.addr)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fmt.Fprintln(cmd.ErrOrStderr(), "peepwho: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// localOnlyServe reports whether the server must pin the Host header to
// loopback hosts. The tokenless loopback bind is the one configuration DNS
// rebinding can reach: no token gates it, and in the browser a rebound domain
// is same-origin with it. A token gates a shared server on its own, and the
// documented TLS proxy in front of one legitimately forwards a public Host,
// as does the demo's own proxy.
func localOnlyServe(token string, public bool, addr string) bool {
	return token == "" && !public && loopbackAddr(addr)
}

// loopbackAddr reports whether addr binds only the loopback interface. An
// empty host, such as ":8765", binds every interface and is not loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return engine.IsLoopbackHost(host)
}

// cliAskQuery is the question the command line view answers, chosen so the same
// question can sit beside a recorded run that had a model, showing what the
// model adds rather than claiming it finds someone the words alone would miss.
const cliAskQuery = "kafka consumer lag keeps climbing, who can help"

// cliFn renders what the command line prints for this same index, through the
// very renderers the terminal uses, with color turned off. The web views answer
// these questions in their own way; this exists so a reader can see the tool as
// an engineer would run it, against the data in front of them, rather than take
// a screenshot's word for it.
func cliFn(ix *index.Index, ask web.AskFunc, sealer web.AttestFunc) web.CLIFunc {
	var plain style
	return func(name string) (string, bool) {
		var b strings.Builder
		switch name {
		case "ask":
			answer, err := ask(context.Background(), cliAskQuery, "keyword", "", 5)
			if err != nil {
				return "", false
			}
			renderAsk(&b, cliAskQuery, answer.View(cliAskQuery), plain)
		case "risk":
			renderRisk(&b, resolve.Risk(ix, 12), plain)
		case "ownership":
			renderOwnership(&b, resolve.Ownership(ix), plain)
		case "related":
			topic := "billing"
			if top := resolve.Risk(ix, 1); len(top) > 0 {
				topic = top[0].Topic
			}
			renderRelated(&b, topic, resolve.Related(ix, topic, 8), plain)
		case "attest":
			if sealer == nil {
				return "", false
			}
			sealed, err := sealer()
			if err != nil {
				return "", false
			}
			b.Write(sealed.Manifest)
		default:
			return "", false
		}
		return b.String(), true
	}
}

// attestFn returns the web handler that seals the current knowledge-risk finding
// into a signed directory and serves it as a zip. The signing key is the
// persistent one under the data directory where that is writable. A hardened or
// read-only deployment, such as the public demo, cannot keep one, so it signs
// with a key held only in memory: the manifest carries its own public key, so it
// still verifies on its own terms, it just does not claim the same identity
// across restarts.
func attestFn(ix *index.Index, opts *options, logw io.Writer) web.AttestFunc {
	priv, err := opts.attestKey()
	if err != nil {
		_, key, genErr := ed25519.GenerateKey(nil)
		if genErr != nil {
			fmt.Fprintf(logw, "peepwho: attest disabled: %v\n", genErr)
			return nil
		}
		// Say what this costs and how to stop paying it. Signed findings that
		// cannot be checked against a stable key after a restart are worth
		// much less than the feature promises, and the fix is one flag.
		fmt.Fprintf(logw,
			"peepwho: attest signing with a key held only in memory: %v\n"+
				"  findings signed now will not verify against this install after a restart.\n"+
				"  point --data-dir at somewhere writable to keep one key, "+
				"such as a systemd StateDirectory.\n", err)
		priv = key
	}
	return func() (web.Sealed, error) {
		dir, err := os.MkdirTemp("", "peepwho-seal-*")
		if err != nil {
			return web.Sealed{}, fmt.Errorf("attest: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		m, err := sealRiskDir(dir, priv, ix, resolve.Risk(ix, 0))
		if err != nil {
			return web.Sealed{}, err
		}
		manifest, err := os.ReadFile(filepath.Join(dir, seal.ManifestName))
		if err != nil {
			return web.Sealed{}, fmt.Errorf("attest: %w", err)
		}
		archive, err := zipSealed(dir, m)
		if err != nil {
			return web.Sealed{}, err
		}
		return web.Sealed{Manifest: manifest, Archive: archive}, nil
	}
}

// modeReadiness reports what each answer mode and AI provider needs right
// now, so the UI can guide the user before they ask instead of failing after.
func modeReadiness(
	ctx context.Context, ix *index.Index, pol policy.Policy, cfg webConfig,
) web.ModesReport {
	modes := map[string]web.ModeInfo{
		"keyword": {Ready: true, Hint: "Matches your exact words, with typo tolerance. Always available."},
	}
	if ix.HasEmbeddings() {
		modes["semantic"] = web.ModeInfo{
			Ready: true,
			Hint: "Matches by meaning, so \"failed payments\" can find \"billing retries\". " +
				"Uses this index's local embeddings.",
		}
	} else {
		modes["semantic"] = web.ModeInfo{
			Ready: false,
			Hint: "Matches by meaning, so \"failed payments\" can find \"billing retries\". " +
				"This index has none yet: rebuild it with peepwho index --embed (uses local Ollama).",
		}
	}

	providers := map[string]web.ModeInfo{
		"ollama":    ollamaReadiness(ctx, cfg),
		"anthropic": cloudReadiness(pol, "Claude", engine.AnthropicKeyEnv),
		"openai":    cloudReadiness(pol, "ChatGPT", engine.OpenAIKeyEnv),
		"gemini":    cloudReadiness(pol, "Gemini", engine.GeminiKeyEnv),
	}
	provider := cfg.provider
	if provider == "" {
		provider = "ollama"
	}
	modes["llm"] = providers[provider]

	report := web.ModesReport{Modes: modes, Providers: providers, Provider: provider}
	if cfg.public {
		report = demoModeHints(report)
	}
	return report
}

// demoModeHints rewrites the hints a public demo shows for the modes it does not
// run. The local hints tell the reader to install Ollama, rebuild the index, or
// restart the server, which is advice a visitor cannot act on and which makes a
// deliberate choice read as a broken feature. What is true is better: ranking is
// deterministic and needs no model, a model is an upgrade you host yourself, and
// a public server must never hold a provider key.
func demoModeHints(r web.ModesReport) web.ModesReport {
	const (
		semanticHint = "Matches by meaning, so \"failed payments\" can find \"billing retries\". " +
			"It runs on an embedding model you host yourself, so this public demo stays keyword only. " +
			"Every answer you see here is deterministic, with no model involved."
		modelHint = "Peepwho ranks deterministically and explains every answer, so a model is an " +
			"upgrade rather than a requirement. Run peepwho yourself to point it at a local model, " +
			"or at Claude, ChatGPT, or Gemini under a policy you set."
		cloudHint = " This public demo carries no provider key on purpose: a shared server holding " +
			"one would spend someone else's account."
	)
	if m, ok := r.Modes["semantic"]; ok && !m.Ready {
		m.Hint = semanticHint
		r.Modes["semantic"] = m
	}
	for name, p := range r.Providers {
		if p.Ready {
			continue
		}
		p.Hint = modelHint
		if name != "ollama" {
			p.Hint += cloudHint
		}
		r.Providers[name] = p
	}
	if m, ok := r.Modes["llm"]; ok && !m.Ready {
		r.Modes["llm"] = r.Providers[r.Provider]
	}
	return r
}

// ollamaReadiness live-probes the local Ollama server.
func ollamaReadiness(ctx context.Context, cfg webConfig) web.ModeInfo {
	model := cfg.model
	if model == "" {
		model = "llama3.1"
	}
	if ollamaUp(ctx, cfg.ollamaURL) {
		return web.ModeInfo{
			Ready: true,
			Hint: "A model on this machine (" + model + ") picks the best matches and writes " +
				"a recommendation. Nothing leaves your computer.",
		}
	}
	return web.ModeInfo{
		Ready: false,
		Hint: "Runs a private model on this machine, but Ollama is not running. Install it " +
			"from ollama.com, run: ollama pull " + model + ", then pick it again.",
	}
}

// cloudReadiness checks a cloud provider's key and the egress policy.
func cloudReadiness(pol policy.Policy, name, keyEnv string) web.ModeInfo {
	if pol.Mode() == policy.Strict {
		return web.ModeInfo{
			Ready: false,
			Hint: name + " is off under the strict policy. Restart serve with --policy " +
				"redacted (sends only anonymized candidates) or open.",
		}
	}
	if os.Getenv(keyEnv) == "" {
		return web.ModeInfo{
			Ready: false,
			Hint:  "To use " + name + ", set " + keyEnv + " and restart serve.",
		}
	}
	return web.ModeInfo{
		Ready: true,
		Hint: name + " picks the best matches and writes a recommendation, under the " +
			pol.Mode().String() + " policy.",
	}
}

// ollamaUp reports whether a local Ollama answers at base within a short
// timeout. Only loopback hosts are probed; a remote host is left to the
// egress guard at ask time and assumed reachable here.
func ollamaUp(ctx context.Context, base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	if !loopbackAddr(u.Host) {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	client := httputil.NewClient(time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
