// Package web serves the whotoask web UI: a search page and a JSON ask API over
// the same engine the CLI uses.
package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/llm"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/report"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// assets holds the embedded templates and static files.
//
//go:embed templates/*.html static/*
var assets embed.FS

// AskFunc resolves a query in the chosen mode and provider and returns the
// answer. Empty mode and provider mean the server defaults.
type AskFunc func(ctx context.Context, query, mode, provider string, limit int) (resolve.Answer, error)

// SearchFunc finds people and channels matching a free-text query, ranked by
// how directly they match.
type SearchFunc func(query string, limit int) []resolve.SearchResult

// FeedbackFunc records a user's vote on one result.
type FeedbackFunc func(feedback.Entry) error

// PersonFunc returns the full profile for a person identifier, or false when
// the person is unknown.
type PersonFunc func(id string) (resolve.JSONProfile, bool)

// RecallFunc answers what one person worked through before. The person is
// always named, so an answer can only ever cover their own conversations.
type RecallFunc func(ctx context.Context, person, query string, limit int) (recall.Answer, error)

// ModeInfo tells the UI whether an answer mode or provider can answer right
// now and what it uses or is missing.
type ModeInfo struct {
	// Ready reports whether the mode can answer right now.
	Ready bool `json:"ready"`
	// Hint says what the mode uses, or what to do to make it ready.
	Hint string `json:"hint,omitempty"`
}

// ModesReport is the readiness picture for the UI: the answer modes, the AI
// providers to pick from, and the server's default provider.
type ModesReport struct {
	// Modes is readiness per answer mode: keyword, semantic, llm.
	Modes map[string]ModeInfo `json:"modes"`
	// Providers is readiness per AI provider: ollama, anthropic, openai,
	// gemini.
	Providers map[string]ModeInfo `json:"providers,omitempty"`
	// Provider is the server's default AI provider.
	Provider string `json:"provider,omitempty"`
}

// ModesFunc reports mode and provider readiness.
type ModesFunc func(ctx context.Context) ModesReport

// Config configures the web handler.
type Config struct {
	// Ask resolves queries; required.
	Ask AskFunc
	// Feedback records votes on results; nil disables the feedback API.
	Feedback FeedbackFunc
	// Person returns full person profiles; nil disables the person API.
	Person PersonFunc
	// Version is shown in the page footer.
	Version string
	// AuthToken, when set, requires the token on every request: a bearer
	// header, a token query parameter, or the cookie a prior visit set.
	AuthToken string `json:"-"`
	// LocalOnly rejects any request whose Host header does not name a loopback
	// host. A loopback bind with no token trusts the network boundary, and DNS
	// rebinding is the one way through it: a page on an attacker's domain,
	// re-pointed at 127.0.0.1, becomes same-origin with this server in the
	// browser, and the rebound domain in the Host header is the only trace.
	// Set it whenever the server binds loopback without a token.
	LocalOnly bool
	// Directory is the browsable inventory served at /api/directory; nil
	// disables the directory API.
	Directory *resolve.Directory
	// Search finds people and channels by name and fields at /api/search; nil
	// disables the search API.
	Search SearchFunc
	// Modes reports answer-mode readiness at /api/modes; nil disables it.
	Modes ModesFunc
	// Recall answers what one person worked through before, at /api/recall;
	// nil disables it. The request names the person, and nothing here proves
	// that name, so a caller who can reach this endpoint can ask as anyone.
	// Set it only where the caller can only be the person running whotoask.
	Recall RecallFunc
	// RecallMe is the identity the recall view starts with, usually the
	// person running whotoask. It is a starting point, not an authorization:
	// the caller can change it, which is why recall is served on loopback
	// only.
	RecallMe string
	// Brief renders the knowledge-risk brief as one self-contained page, at
	// /report/risk.html, so the finding on screen can be handed to somebody
	// who does not have whotoask. Nil leaves the route unregistered.
	Brief BriefFunc
	// Exposure reports where the organization is exposed, at /api/exposure;
	// nil disables it.
	Exposure ExposureFunc
	// Departure reports what knowledge leaves with one person, at
	// /api/departure; nil disables it.
	Departure DepartureFunc
	// Attest seals the current finding into a signed, offline-verifiable
	// bundle, at /api/attest; nil disables it.
	Attest AttestFunc
	// Related reports the topics that share a topic's experts, at
	// /api/related; nil disables it.
	Related RelatedFunc
	// CLI renders what a command line invocation prints for this same index,
	// at /api/cli; nil disables it.
	CLI CLIFunc
	// Log receives server-side error detail kept out of client responses; nil
	// discards it.
	Log io.Writer
	// Ready reports whether the server can serve real work, backing /readyz;
	// nil means always ready. /healthz is a pure liveness check independent of
	// it.
	Ready func() bool
}

// Handler returns the whotoask web handler: an index page, embedded assets, and a
// JSON ask API. It panics if cfg.Ask is nil.
func Handler(cfg Config) (http.Handler, error) {
	if cfg.Ask == nil {
		panic("web: Handler requires an Ask function")
	}
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}

	logw := cfg.Log
	if logw == nil {
		logw = io.Discard
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("/api/ask", askHandler(cfg.Ask, logw))
	if cfg.Feedback != nil {
		mux.HandleFunc("/api/feedback", feedbackHandler(cfg.Feedback, logw))
	}
	if cfg.Person != nil {
		mux.HandleFunc("/api/person", personHandler(cfg.Person))
	}
	if cfg.Directory != nil {
		mux.HandleFunc("/api/directory", directoryHandler(cfg.Directory))
	}
	if cfg.Search != nil {
		mux.HandleFunc("/api/search", searchHandler(cfg.Search))
	}
	if cfg.Modes != nil {
		mux.HandleFunc("/api/modes", modesHandler(cfg.Modes))
	}
	if cfg.Exposure != nil {
		mux.HandleFunc("/api/exposure", exposureHandler(cfg.Exposure))
	}
	if cfg.Brief != nil {
		mux.HandleFunc("/report/risk.html", briefHandler(cfg.Brief))
	}
	if cfg.Departure != nil {
		mux.HandleFunc("/api/departure", departureHandler(cfg.Departure))
	}
	if cfg.Attest != nil {
		mux.HandleFunc("/api/attest", attestHandler(cfg.Attest, logw))
	}
	if cfg.Related != nil {
		mux.HandleFunc("/api/related", relatedHandler(cfg.Related))

	}
	if cfg.CLI != nil {
		mux.HandleFunc("/api/cli", cliHandler(cfg.CLI))
	}
	if cfg.Recall != nil {
		mux.HandleFunc("/api/recall", recallHandler(cfg.Recall, logw))
	}
	if cfg.Directory != nil {
		mux.HandleFunc("/orgchart", orgchartHandler(tmpl, cfg))
	}
	mux.HandleFunc("/", indexHandler(tmpl, cfg))

	// securityHeaders wraps outermost so hardening headers reach even the 401
	// that requireToken writes for a missing or wrong token.
	h := http.Handler(mux)
	if cfg.AuthToken != "" {
		h = requireToken(cfg.AuthToken, h)
	}
	// Liveness and readiness bypass the token so a load balancer can probe a
	// server it holds no credential for. They still sit under securityHeaders.
	probed := http.NewServeMux()
	probed.HandleFunc("/healthz", healthHandler())
	probed.HandleFunc("/readyz", readyHandler(cfg.Ready))
	probed.Handle("/", h)
	guarded := http.Handler(probed)
	if cfg.LocalOnly {
		guarded = requireLoopbackHost(guarded)
	}
	return securityHeaders(guarded), nil
}

// requireLoopbackHost rejects requests whose Host header does not name a
// loopback host. Every legitimate way to reach a loopback bind, localhost,
// 127.0.0.1, ::1, or an ssh forward to any of them, carries a loopback Host;
// a domain rebound to 127.0.0.1 carries itself, and this is where it stops.
func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "localhost" {
			ip := net.ParseIP(strings.Trim(host, "[]"))
			if ip == nil || !ip.IsLoopback() {
				writeError(w, http.StatusForbidden,
					"this server answers only loopback hosts; open it as localhost or 127.0.0.1")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// healthHandler answers a pure liveness probe. It reports the process is up
// without touching any data, so it stays 200 even when the index is empty.
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// readyHandler answers a readiness probe: whether the server can serve real
// work, not just that its process is up, so a load balancer holds traffic off
// until the index is loaded. A nil check is always ready.
func readyHandler(ready func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ready != nil && !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]bool{"ready": false})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ready": true})
	}
}

// securityHeaders sets response headers that harden every page and API reply.
// The content security policy is default-src 'self' because the UI loads only
// same-origin assets and calls only same-origin APIs. Referrer-Policy keeps a
// query, and a token still riding in a URL, out of outbound requests. API and
// report responses carry personal data, so caches are told to hold nothing.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/report/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// authCookie names the session cookie set after a token is presented.
const authCookie = "whotoask_token"

// requireToken gates every request behind the shared token. A token query
// parameter sets a strict same-site cookie so a shared link keeps working
// after the first visit, and a page navigation is then redirected to the same
// URL without the token, so the secret does not stay in the address bar or
// the browser history. API and report requests answer in place: a script
// passing the token in the query has no cookie jar, so redirecting it away
// from the token would turn the documented pattern into a 401. The cookie's
// Secure attribute also trusts X-Forwarded-Proto, because the documented
// deployment terminates TLS in a proxy and hands this server plain HTTP.
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(token, r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="whotoask"`)
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}
		if r.URL.Query().Get("token") != "" {
			secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(w, &http.Cookie{
				Name: authCookie, Value: token, Path: "/",
				HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
			})
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
				!strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/report/") {
				q := r.URL.Query()
				q.Del("token")
				clean := *r.URL
				clean.RawQuery = q.Encode()
				http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// tokenOK reports whether r carries the token in a bearer header, a query
// parameter, or the session cookie. Comparisons are constant-time.
func tokenOK(token string, r *http.Request) bool {
	const bearer = "Bearer "
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, bearer) {
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, bearer)), []byte(token)) == 1 {
			return true
		}
	}
	if q := r.URL.Query().Get("token"); q != "" {
		if subtle.ConstantTimeCompare([]byte(q), []byte(token)) == 1 {
			return true
		}
	}
	if c, err := r.Cookie(authCookie); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

// indexHandler serves the search page at the root path.
func indexHandler(tmpl *template.Template, cfg Config) http.HandlerFunc {
	data := struct {
		// Version is the running whotoask version.
		Version string
		// Recall reports whether the recall view is available.
		Recall bool
		// RecallMe is the identity the recall view starts with, which the
		// person can change.
		RecallMe string
		// Exposure reports whether the exposure view is available.
		Exposure bool
		// CLI reports whether the command line view is available.
		CLI bool
		// Brief reports whether the knowledge-risk brief can be downloaded.
		Brief bool
		// Voting reports whether the server records feedback votes.
		Voting bool
	}{
		Version: cfg.Version, Recall: cfg.Recall != nil, RecallMe: cfg.RecallMe,
		Exposure: cfg.Exposure != nil, CLI: cfg.CLI != nil, Brief: cfg.Brief != nil,
		Voting: cfg.Feedback != nil,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	}
}

// orgchartHandler serves the org-chart page, a full-screen interactive view of
// the reporting graph. It reads its data from the same /api/directory and
// /api/person endpoints the search UI uses.
func orgchartHandler(tmpl *template.Template, cfg Config) http.HandlerFunc {
	data := struct {
		// Version is the running whotoask version.
		Version string
		// Me is the person running whotoask, for the "My team" jump.
		Me string
	}{Version: cfg.Version, Me: cfg.RecallMe}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgchart" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "orgchart.html", data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	}
}

// askHandler answers queries as JSON. It reads q, mode, and limit from the query
// string and returns the same shape the CLI emits.
func askHandler(ask AskFunc, logw io.Writer) http.HandlerFunc {
	if ask == nil {
		panic("web: askHandler requires an Ask function")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			writeError(w, http.StatusBadRequest, "missing q")
			return
		}
		if tooLong(w, query) {
			return
		}
		const maxLimit = 50
		limit := 5
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxLimit {
				limit = n
			}
		}

		ans, err := ask(r.Context(), query, r.URL.Query().Get("mode"), r.URL.Query().Get("provider"), limit)
		if err != nil {
			if errors.Is(err, ErrBadRequest) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if errors.Is(err, llm.ErrModel) {
				writeError(w, http.StatusBadGateway,
					"The local model is not reachable. LLM and semantic modes need Ollama "+
						"running on this machine: install it from ollama.com, run "+
						"`ollama pull llama3.1`, and ask again. Keyword mode always works.")
				return
			}
			// Log the failure but not the query itself: a question can carry
			// sensitive terms, and an operator who wired a log file should not
			// have every asked question land in it.
			fmt.Fprintf(logw, "web: ask failed: %v\n", err)
			writeError(w, http.StatusBadGateway, "the answer service is unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(ans.View(query))
	}
}

// recallHandler answers what the named person worked through before. The
// person is required: recall never searches across the organization.
func recallHandler(fn RecallFunc, logw io.Writer) http.HandlerFunc {
	if fn == nil {
		panic("web: recallHandler requires a Recall function")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// An empty query is valid: recall then returns the conversations you
		// took part in, most recent first. A person is still required.
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if tooLong(w, query) {
			return
		}
		person := strings.TrimSpace(r.URL.Query().Get("me"))
		if person == "" {
			writeError(w, http.StatusBadRequest,
				"missing me: recall returns only conversations you took part in")
			return
		}
		const maxLimit = 25
		limit := 5
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxLimit {
				limit = n
			}
		}
		ans, err := fn(r.Context(), person, query, limit)
		if err != nil {
			if errors.Is(err, ErrBadRequest) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			// A recall query is personal-history search, so keep it out of the
			// log exactly as the ask handler keeps its question out.
			fmt.Fprintf(logw, "web: recall failed: %v\n", err)
			writeError(w, http.StatusBadGateway, "the recall service is unavailable")
			return
		}
		_ = json.NewEncoder(w).Encode(ans)
	}
}

// personHandler returns the full profile for the person named by the id query
// parameter.
func personHandler(person PersonFunc) http.HandlerFunc {
	if person == nil {
		panic("web: personHandler requires a Person function")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing id")
			return
		}
		profile, ok := person(id)
		if !ok {
			writeError(w, http.StatusNotFound, "unknown person")
			return
		}
		_ = json.NewEncoder(w).Encode(profile)
	}
}

// modesHandler reports each answer mode's readiness so the UI can guide the
// user before they ask.
func modesHandler(modes ModesFunc) http.HandlerFunc {
	if modes == nil {
		panic("web: modesHandler requires a Modes function")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modes(r.Context()))
	}
}

// Exposure is what the exposure view reports: the topics where knowledge is
// concentrated in too few people, and the areas whose declared owner is not the
// one doing the work.
type Exposure struct {
	// Places are the directories the work rests on, strongest holder first.
	// A place is a stronger claim than a topic because it is checkable: the
	// reader can run git log against the same directory and see the same
	// names. It is empty unless a repository was indexed.
	Places []resolve.Place `json:"places,omitempty"`
	// Risk is knowledge concentration per topic, most exposed first.
	Risk []resolve.TopicRisk `json:"risk"`
	// Drift is where declared ownership and real expertise disagree.
	Drift []resolve.OwnerDrift `json:"drift"`
	// Regions are the joined bodies of work resting on one person: subjects
	// changed together where the same person leads every one. It is a heavier
	// finding than a concentrated subject, and a list of subjects one at a time
	// cannot show it.
	Regions []resolve.Region `json:"regions"`
	// Spans are the connections between two subjects that only one person has
	// ever worked across. Both subjects may be well covered on their own, so
	// nothing that counts experts per subject can show it.
	Spans []resolve.Span `json:"spans"`
}

// BriefFunc builds the knowledge-risk brief for the index being served.
type BriefFunc func() report.Brief

// briefHandler serves the knowledge-risk brief as a downloadable page. It
// renders into memory first, so a failure mid-render is an error rather than
// half a report delivered with a success code.
func briefHandler(fn BriefFunc) http.HandlerFunc {
	if fn == nil {
		panic("web: briefHandler requires a Brief function")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		if err := report.WriteRisk(&buf, fn()); err != nil {
			writeError(w, http.StatusInternalServerError, "the brief could not be rendered")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="whotoask-knowledge-risk.html"`)
		_, _ = w.Write(buf.Bytes())
	}
}

// ExposureFunc computes the current exposure.
type ExposureFunc func() Exposure

// exposureHandler serves the exposure view's data.
func exposureHandler(fn ExposureFunc) http.HandlerFunc {
	if fn == nil {
		panic("web: exposureHandler requires an ExposureFunc")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fn())
	}
}

// CLIFunc renders one command's terminal output, reporting false for a command
// it does not know.
type CLIFunc func(command string) (string, bool)

// cliHandler serves what the command line prints, as plain text. The web app
// answers the same questions through its own views; this exists so a reader can
// see the tool the way an engineer actually runs it, against this same data.
func cliHandler(fn CLIFunc) http.HandlerFunc {
	if fn == nil {
		panic("web: cliHandler requires a CLIFunc")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		out, ok := fn(strings.TrimSpace(r.URL.Query().Get("cmd")))
		if !ok {
			writeError(w, http.StatusNotFound, "no such command")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, out)
	}
}

// RelatedFunc reports the topics that belong to the same body of knowledge.
type RelatedFunc func(topic string, limit int) []resolve.TopicRelation

// relatedHandler serves the topics whose experts overlap with one topic's.
func relatedHandler(fn RelatedFunc) http.HandlerFunc {
	if fn == nil {
		panic("web: relatedHandler requires a RelatedFunc")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		topic := strings.TrimSpace(r.URL.Query().Get("topic"))
		if topic == "" {
			writeError(w, http.StatusBadRequest, "name the topic with ?topic=")
			return
		}
		const maxLimit = 50
		limit := 8
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= maxLimit {
			limit = n
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"topic": topic, "related": fn(topic, limit)})
	}
}

// Sealed is a sealed finding as the web serves it.
type Sealed struct {
	// Manifest is the exact bytes of the signed manifest.
	Manifest []byte
	// Archive is a zip of the sealed directory: the manifest, its signature,
	// and every file the manifest covers.
	Archive []byte
}

// AttestFunc seals the current finding.
type AttestFunc func() (Sealed, error)

// attestHandler serves a sealed finding. A plain fetch returns the signed
// manifest so the page can show what was sealed. A download returns the whole
// sealed directory as a zip, because the directory is the artifact: a reader
// takes it away and verifies it with a command that never talks to this server.
func attestHandler(fn AttestFunc, logw io.Writer) http.HandlerFunc {
	if fn == nil {
		panic("web: attestHandler requires an AttestFunc")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sealed, err := fn()
		if err != nil {
			fmt.Fprintf(logw, "web: attest: %v\n", err)
			writeError(w, http.StatusInternalServerError, "could not seal the finding")
			return
		}
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="whotoask-finding.zip"`)
			_, _ = w.Write(sealed.Archive)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sealed.Manifest)
	}
}

// DepartureFunc reports what leaves with the person a query names.
type DepartureFunc func(person string) resolve.DepartureImpact

// departureHandler serves what one person's departure would cost.
func departureHandler(fn DepartureFunc) http.HandlerFunc {
	if fn == nil {
		panic("web: departureHandler requires a DepartureFunc")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		person := strings.TrimSpace(r.URL.Query().Get("person"))
		if person == "" {
			writeError(w, http.StatusBadRequest, "name who is leaving with ?person=")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fn(person))
	}
}

// directoryHandler serves the precomputed directory of people, channels,
// teams, and topics for the browse views.
func directoryHandler(dir *resolve.Directory) http.HandlerFunc {
	if dir == nil {
		panic("web: directoryHandler requires a Directory")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dir)
	}
}

// searchHandler answers /api/search: a ranked list of people and channels
// matching the q parameter, capped by an optional limit.
func searchHandler(search SearchFunc) http.HandlerFunc {
	if search == nil {
		panic("web: searchHandler requires a SearchFunc")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeError(w, http.StatusBadRequest, "name what to search for with ?q=")
			return
		}
		if tooLong(w, q) {
			return
		}
		const maxLimit = 50
		limit := 20
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= maxLimit {
			limit = n
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"query": q, "results": search(q, limit)})
	}
}

// feedbackHandler records a vote on one result. It accepts a POST with a JSON
// body naming the query, the person or channel, and the vote direction. A
// cross-origin POST is rejected so another site the operator has open cannot
// cast votes through their browser.
func feedbackHandler(record FeedbackFunc, logw io.Writer) http.HandlerFunc {
	if record == nil {
		panic("web: feedbackHandler requires a Feedback function")
	}
	// maxFeedbackBytes bounds a feedback body so a large POST cannot exhaust
	// memory; a well-formed vote is well under this.
	const maxFeedbackBytes = 64 << 10
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
			writeError(w, http.StatusForbidden, "cross-origin feedback rejected")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFeedbackBytes)
		var body struct {
			// Query is the question the vote is about.
			Query string `json:"query"`
			// Person is the voted person's identifier.
			Person string `json:"person"`
			// Channel is the voted channel's name.
			Channel string `json:"channel"`
			// Vote is "helpful" or "not-helpful".
			Vote string `json:"vote"`
			// Comment is an optional note explaining the vote.
			Comment string `json:"comment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "feedback too large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		entry := feedback.Entry{
			Query:   strings.TrimSpace(body.Query),
			Person:  strings.TrimSpace(body.Person),
			Channel: strings.TrimSpace(body.Channel),
			Comment: strings.TrimSpace(body.Comment),
			Time:    time.Now(),
		}
		switch body.Vote {
		case "helpful":
			entry.Vote = feedback.Helpful
		case "not-helpful":
			entry.Vote = feedback.NotHelpful
		}
		if !entry.Valid() {
			writeError(w, http.StatusBadRequest, feedback.ErrBadEntry.Error())
			return
		}
		if err := record(entry); err != nil {
			fmt.Fprintf(logw, "web: record feedback: %v\n", err)
			writeError(w, http.StatusInternalServerError, "could not record feedback")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
	}
}

// sameOrigin reports whether the Origin header names this server: a web scheme
// and a host matching the request's. The scheme check rejects an opaque origin,
// such as "null" or a file URL, that carries no host of its own.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host == host
}

// maxQueryLen bounds how long a question may be. Nobody types more than a
// couple of lines, and without a bound one request hands the ranker an
// unbounded amount of work and gets an answer just as large back, which is a
// cost a public instance pays on behalf of whoever sent it.
const maxQueryLen = 512

// tooLong answers an over-long query and reports that the caller should stop.
func tooLong(w http.ResponseWriter, query string) bool {
	if len(query) <= maxQueryLen {
		return false
	}
	writeError(w, http.StatusBadRequest,
		fmt.Sprintf("q is %d characters, longer than the %d allowed", len(query), maxQueryLen))
	return true
}

// writeError writes a JSON error response with the given status. It sets the
// content type itself so error paths that never reached a handler, like the
// 401 for a missing token, still declare JSON.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
