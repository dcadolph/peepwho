// Package core is the peepwho surface the Mac and iPhone apps call through
// gomobile, bound as the PeepwhoCore framework.
//
// The API is flat because gomobile restricts what crosses the language
// boundary: strings, ints, and bools in, JSON strings and errors out. Answers
// are the same JSON the command line prints with --json. Only keyword ranking
// and recall run here, under the strict egress policy, so nothing an app asks
// leaves the device. The host supplies the clock, so the core never reads one.
//
// The host also supplies every secret. The index key is an argument to Open,
// and source tokens come through a Secrets the host sets on the handle. The
// core never reads the environment or a keychain, so a sandboxed app keeps its
// tokens in its own Keychain items and the core never learns where they live.
package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/vault"
)

// defaultLimit is the result count used when a caller passes zero or less.
const defaultLimit = 5

// maxLimit caps results per call so a bad argument cannot ask for everything.
const maxLimit = 100

// Handle is an opened peepwho data directory usable from Swift. Its methods
// are safe to call from several threads: a question asked while Index or
// Refresh runs answers from the index as it was until the new one is loaded.
type Handle struct {
	// mu guards loaded and secrets.
	mu sync.RWMutex
	// loaded is the index and conversations questions answer from. Nil means
	// the directory has no index yet.
	loaded *loaded
	// writeMu serializes Index and Refresh on the handle.
	writeMu sync.Mutex
	// sealed reports whether the index was opened with a key.
	sealed bool
	// store reads and writes the files in the data directory under the key.
	store *engine.Store
	// secrets supplies source tokens by name. Nil reads every name as empty.
	secrets Secrets
}

// loaded is one load of the data directory, swapped whole so a question never
// sees an index from one load and conversations from another.
type loaded struct {
	// eng answers from the index under the strict policy.
	eng *engine.Engine
	// episodes holds the conversations recall answers from.
	episodes *episode.Store
}

// Secrets supplies source credentials to the core. The host implements it,
// such as a Swift type reading the app's Keychain, since gomobile binds a Go
// interface to a Swift protocol but cannot carry a Go func value.
type Secrets interface {
	// Get returns the secret stored under name, or empty when it is unset.
	// Names are the environment variable names the command line reads, such as
	// PEEPWHO_JIRA_TOKEN, so the app and the command line agree on them.
	Get(name string) string
}

// SetSecrets sets where the handle reads source credentials. A nil Secrets
// makes every credential read as empty.
func (h *Handle) SetSecrets(s Secrets) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secrets = s
}

// secret returns the credential stored under name, or empty when the handle
// has no Secrets or the host has none by that name.
func (h *Handle) secret(name string) string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	s := h.secrets
	h.mu.RUnlock()
	if s == nil {
		return ""
	}
	return s.Get(name)
}

// Open opens the data directory at dir. key is the base64 index key that
// `peepwho vault keygen` prints and PEEPWHO_INDEX_KEY holds, or empty for a
// plain index. A sealed index opened with no key, or with the wrong key, fails
// here with an error that names the key, rather than on the first question. A
// directory with no index yet, or none at all, opens unbuilt: Index or Refresh
// builds it, and until then a question says there is no index yet.
func Open(dir, key string) (*Handle, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("core: data directory is empty")
	}
	codec, err := keyCodec(key)
	if err != nil {
		return nil, err
	}
	st := engine.NewStore(engine.Dir(dir), codec)
	l, err := load(st)
	if err != nil {
		return nil, err
	}
	return &Handle{loaded: l, sealed: codec != nil, store: st}, nil
}

// load reads the index, feedback, and conversations in st. A missing index
// returns nil and no error, since a directory nothing has indexed into yet is
// not a fault.
func load(st *engine.Store) (*loaded, error) {
	ix, err := st.LoadIndex()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, openError(st.Dir().Index(), st.Codec(), err)
	}
	eng := engine.New(ix, policy.Default())
	fb, err := st.LoadFeedback()
	if err != nil {
		return nil, openError(st.Dir().Feedback(), st.Codec(), err)
	}
	eng.SetFeedback(fb)
	eps, err := st.LoadEpisodes()
	if err != nil {
		return nil, openError(st.Dir().Episodes(), st.Codec(), err)
	}
	return &loaded{eng: eng, episodes: eps}, nil
}

// keyCodec builds the raw-key codec for key, or nil when key is empty.
func keyCodec(key string) (vault.Codec, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("core: the index key must be base64, as peepwho vault keygen prints it: %w", err)
	}
	c, err := vault.NewKeyCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("core: index key: %w", err)
	}
	return c, nil
}

// openError says what to do about a file that would not open. A sealed file
// with no key and a key that does not fit are the two failures an app can fix
// by asking for the key again, so both name it.
func openError(path string, codec vault.Codec, err error) error {
	switch {
	case errors.Is(err, vault.ErrEncrypted) && engine.NeedsKey(path):
		return fmt.Errorf("core: %s is sealed: open it with the index key from peepwho vault keygen: %w",
			path, err)
	case errors.Is(err, vault.ErrEncrypted):
		return fmt.Errorf("core: %s is sealed with a passphrase: the app opens only an index sealed "+
			"with the index key from peepwho vault keygen: %w", path, err)
	case codec != nil && errors.Is(err, vault.ErrCorrupt):
		return fmt.Errorf("core: the index key does not open %s: %w", path, err)
	default:
		return fmt.Errorf("core: open %s: %w", path, err)
	}
}

// errNotOpen is returned by a handle Swift built directly instead of with Open.
var errNotOpen = errors.New("core: handle is not open: get one from Open")

// errNoIndex is returned by a question asked of a directory with no index yet.
var errNoIndex = errors.New("core: no index yet: index a source first")

// ready reports whether the handle came from Open. gomobile exposes a bare
// initializer for every struct, so a Swift caller can hold an empty handle.
func (h *Handle) ready() error {
	if h == nil || h.store == nil {
		return errNotOpen
	}
	return nil
}

// current returns what questions answer from, nil when there is no index yet.
func (h *Handle) current() *loaded {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loaded
}

// built returns what questions answer from, or an error when the handle is
// not open or the directory has no index yet.
func (h *Handle) built() (*loaded, error) {
	if err := h.ready(); err != nil {
		return nil, err
	}
	l := h.current()
	if l == nil {
		return nil, fmt.Errorf("%w in %s", errNoIndex, h.store.Dir())
	}
	return l, nil
}

// clampLimit maps a caller's limit onto the accepted range.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

// toJSON encodes v compactly, the way the command line prints --json.
func toJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("core: encode: %w", err)
	}
	return string(raw), nil
}

// Ask answers who to talk to and where to ask about query, ranked by keyword,
// with up to limit results per section.
func (h *Handle) Ask(query string, limit int) (string, error) {
	l, err := h.built()
	if err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("core: ask needs a question")
	}
	ans, err := l.eng.Ask(context.Background(), query, clampLimit(limit), engine.ResolverConfig{})
	if err != nil {
		return "", fmt.Errorf("core: ask: %w", err)
	}
	return toJSON(ans.View(query))
}

// Search finds people and channels by name, email, title, team, or topic.
func (h *Handle) Search(query string, limit int) (string, error) {
	l, err := h.built()
	if err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("core: search needs a query")
	}
	return toJSON(map[string]any{"query": query, "results": l.eng.Search(query, clampLimit(limit))})
}

// Person returns the full profile of the person with canonical id.
func (h *Handle) Person(id string) (string, error) {
	l, err := h.built()
	if err != nil {
		return "", err
	}
	p, ok := l.eng.Person(strings.TrimSpace(id))
	if !ok {
		return "", fmt.Errorf("core: unknown person: %s", id)
	}
	return toJSON(p)
}

// Recall finds past conversations about query that person took part in. person
// is an email or a source identifier such as slack:U123; the answer covers only
// that person's own conversations.
func (h *Handle) Recall(person, query string, limit int) (string, error) {
	l, err := h.built()
	if err != nil {
		return "", err
	}
	res := l.eng.Recaller(l.episodes)
	who := res.Who(strings.TrimSpace(person))
	if who == "" {
		return "", errors.New("core: recall needs a person, such as an email or slack:U123")
	}
	ans := res.Resolve(context.Background(),
		recall.Query{Text: strings.TrimSpace(query), Person: who, Limit: clampLimit(limit)})
	return toJSON(ans)
}

// statusView is the JSON shape Status returns.
type statusView struct {
	// People is the number of people indexed.
	People int `json:"people"`
	// Channels is the number of channels indexed.
	Channels int `json:"channels"`
	// Teams is the number of teams indexed.
	Teams int `json:"teams"`
	// Subjects counts topics a source stated.
	Subjects int `json:"subjects"`
	// Words counts topics mined from prose.
	Words int `json:"words"`
	// Sources maps each source name to the records it contributed.
	Sources map[string]int `json:"sources"`
	// Embeddings reports whether the index carries vectors.
	Embeddings bool `json:"embeddings"`
	// Conversations is the number of conversations recall can search.
	Conversations int `json:"conversations"`
	// Sealed reports whether the index was opened with a key.
	Sealed bool `json:"sealed"`
	// BuiltAt is when the index was last written, RFC 3339, empty when never.
	BuiltAt string `json:"built_at,omitempty"`
	// AgeSeconds is how long before now the index was written.
	AgeSeconds int64 `json:"age_seconds"`
	// Scopes lists each recorded index run in source then scope order, or each
	// source once under the scope "sample" when the index holds sample data.
	Scopes []scopeView `json:"scopes"`
	// Sample reports whether the index holds the sample company.
	Sample bool `json:"sample"`
	// LoadedAt is when the sample company was loaded, RFC 3339, empty when it was not.
	LoadedAt string `json:"loaded_at,omitempty"`
}

// scopeView is one recorded index run in the JSON Status returns.
type scopeView struct {
	// Source is the source the run reads.
	Source string `json:"source"`
	// Scope is the refresh key the run was recorded under.
	Scope string `json:"scope"`
	// RanAt is when the scope last indexed successfully, RFC 3339, empty when
	// not known.
	RanAt string `json:"ran_at,omitempty"`
	// AgeSeconds is how long before now RanAt was, zero when RanAt is empty.
	AgeSeconds int64 `json:"age_seconds"`
	// AttemptedAt is when the scope was last tried, RFC 3339, empty when not known.
	AttemptedAt string `json:"attempted_at,omitempty"`
	// Error is why the last try failed, empty when it succeeded.
	Error string `json:"error,omitempty"`
}

// scopeViews reads the recorded index runs and says how old each is at now.
func (h *Handle) scopeViews(now time.Time) ([]scopeView, error) {
	inv, err := h.store.LoadInvocations()
	if err != nil {
		return nil, fmt.Errorf("core: status: %w", err)
	}
	views := []scopeView{}
	for _, source := range slices.Sorted(maps.Keys(inv)) {
		for _, scope := range slices.Sorted(maps.Keys(inv[source])) {
			run := inv[source][scope]
			v := scopeView{Source: source, Scope: scope, Error: run.Error}
			if !run.RanAt.IsZero() {
				v.RanAt = run.RanAt.UTC().Format(time.RFC3339)
				v.AgeSeconds = max(0, int64(now.Sub(run.RanAt)/time.Second))
			}
			if !run.AttemptedAt.IsZero() {
				v.AttemptedAt = run.AttemptedAt.UTC().Format(time.RFC3339)
			}
			views = append(views, v)
		}
	}
	return views, nil
}

// Status reports what the index holds and how old it is at nowUnix, the host's
// clock in seconds since the Unix epoch. A directory with no index yet reports
// zero counts and no build time.
func (h *Handle) Status(nowUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	now := time.Unix(nowUnix, 0)
	scopes, err := h.scopeViews(now)
	if err != nil {
		return "", err
	}
	loadedAt, err := h.store.LoadSample()
	if err != nil {
		return "", fmt.Errorf("core: status: %w", err)
	}
	v := statusView{Sources: map[string]int{}, Sealed: h.sealed, Scopes: scopes, Sample: !loadedAt.IsZero()}
	if v.Sample {
		v.LoadedAt = loadedAt.UTC().Format(time.RFC3339)
	}
	l := h.current()
	if l == nil {
		return toJSON(v)
	}
	st := l.eng.Status(now)
	v.People, v.Channels, v.Teams, v.Subjects, v.Words = st.People, st.Channels, st.Teams, st.Subjects, st.Words
	v.Sources, v.Embeddings, v.Conversations = st.Sources, st.Embeddings, l.episodes.Len()
	if !st.BuiltAt.IsZero() {
		v.BuiltAt = st.BuiltAt.UTC().Format(time.RFC3339)
		v.AgeSeconds = int64(st.Age / time.Second)
	}
	if v.Sample {
		v.Scopes = sampleScopes(st.Sources, loadedAt, now)
	}
	return toJSON(v)
}

// sampleScopes lists each source of a sample index once, as loaded at loadedAt.
func sampleScopes(sources map[string]int, loadedAt, now time.Time) []scopeView {
	views := []scopeView{}
	for _, source := range slices.Sorted(maps.Keys(sources)) {
		views = append(views, scopeView{
			Source: source, Scope: "sample", RanAt: loadedAt.UTC().Format(time.RFC3339),
			AgeSeconds: max(0, int64(now.Sub(loadedAt)/time.Second)),
		})
	}
	return views
}

// changesView is the JSON shape ChangesSince returns.
type changesView struct {
	// AsOf is when the newest counted fold ran, RFC 3339, empty when none.
	AsOf string `json:"as_of,omitempty"`
	// Entries is how many logged folds the answer merges.
	Entries int `json:"entries"`
	// Changes is the net of those folds: who and what joined or left.
	Changes index.Changes `json:"changes"`
}

// ChangesSince reports what changed in the index after sinceUnix, seconds since
// the Unix epoch, merged into one set of changes. Zero means everything the
// log holds. The host keeps its own last-look time and passes it in.
func (h *Handle) ChangesSince(sinceUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	entries, err := h.store.LoadChanges()
	if err != nil {
		return "", fmt.Errorf("core: changes: %w", err)
	}
	var v changesView
	for _, e := range entries {
		if e.At.Unix() <= sinceUnix {
			continue
		}
		v.Changes = v.Changes.Merge(e.Changes)
		v.Entries++
		v.AsOf = e.At.UTC().Format(time.RFC3339)
	}
	return toJSON(v)
}

// maxLog caps the progress log an Index or Refresh returns, keeping its end,
// so a long git run cannot grow the answer without bound.
const maxLog = 64 << 10

// tailLog keeps the last max bytes written to it. Sources may log from more
// than one goroutine, so writes are serialized.
type tailLog struct {
	// mu serializes writes.
	mu sync.Mutex
	// buf holds the kept bytes.
	buf []byte
	// max is how many bytes to keep.
	max int
}

// Write appends p and drops the oldest bytes past the cap.
func (l *tailLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > l.max {
		l.buf = append([]byte(nil), l.buf[len(l.buf)-l.max:]...)
	}
	return len(p), nil
}

// String returns the kept log.
func (l *tailLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.buf)
}

// indexer returns the engine indexer every write on the handle runs through,
// so Index and Refresh cannot differ in policy, key, or credentials. It runs
// under the strict policy, reads credentials only from the host's Secrets,
// never prompts, and reads now as its clock.
func (h *Handle) indexer(now time.Time, log *tailLog) *engine.Indexer {
	codec := h.store.Codec()
	return &engine.Indexer{
		Dir:    h.store.Dir(),
		Codec:  func() (vault.Codec, error) { return codec, nil },
		Policy: policy.Default(),
		Creds:  h.secret,
		Log:    log,
		Stdin:  strings.NewReader(""),
		Now:    func() time.Time { return now },
	}
}

// reload loads the data directory again and swaps it in whole, so the next
// question sees what the write just saved. On failure the old load stays.
func (h *Handle) reload() error {
	l, err := load(h.store)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded = l
	return nil
}

// splitFlags splits a newline-separated flag list into its arguments, one
// per line, skipping blank lines. A line is passed as it is, so a path with a
// space in it needs no quoting.
func splitFlags(flags string) []string {
	var args []string
	for _, line := range strings.Split(flags, "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

// indexView is the JSON shape Index returns.
type indexView struct {
	// Source is the source that was indexed.
	Source string `json:"source"`
	// Scope is the refresh key the run was recorded under.
	Scope string `json:"scope"`
	// Log is the end of the run's progress output.
	Log string `json:"log"`
}

// Index reads one source and merges it into the index at nowUnix, the host's
// clock in seconds since the Unix epoch. flags holds the index flags the
// command line takes, one argument per line, such as "--repo-path" and a path
// on the next line, or "--file=/path/people.csv". The run is recorded the way
// the command line records it, so Refresh replays it later. Every run merges,
// so a second source adds to the first; to start over, Wipe then Index.
// Credentials come from the handle's Secrets. --policy is refused, since the
// core runs only under the strict policy.
func (h *Handle) Index(source, flags string, nowUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	source = strings.TrimSpace(source)
	run, err := engine.IndexFromFlags(source, splitFlags(flags))
	if err != nil {
		return "", fmt.Errorf("core: index %s: %w", source, err)
	}
	if run.Policy != "" {
		return "", fmt.Errorf("core: index %s: %w: --policy is not accepted: "+
			"the core runs only under the strict policy", source, engine.ErrBadArgs)
	}
	if engine.ReadsStdin(source, run.Request.File) {
		return "", fmt.Errorf("core: index %s: %w: the source reads standard input, "+
			"which an app cannot supply: pass --file", source, engine.ErrBadArgs)
	}
	run.Request.Merge = true
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := h.refuseSample(); err != nil {
		return "", err
	}
	log := &tailLog{max: maxLog}
	now := time.Unix(nowUnix, 0)
	x := h.indexer(now, log)
	var scope string
	x.Record = func(src, sc string) error {
		scope = sc
		return h.store.RecordInvocation(src, sc, run.Replay, now)
	}
	if err := x.Index(context.Background(), run.Request); err != nil {
		return "", fmt.Errorf("core: index %s: %w", source, err)
	}
	if err := h.reload(); err != nil {
		return "", fmt.Errorf("core: index %s saved, but it would not load: "+
			"open the data directory again: %w", source, err)
	}
	return toJSON(indexView{Source: source, Scope: scope, Log: log.String()})
}

// refreshResult is one replayed scope in the JSON Refresh returns.
type refreshResult struct {
	// Source is the replayed source.
	Source string `json:"source"`
	// Scope is the refresh key the flags were recorded under.
	Scope string `json:"scope"`
	// RanAt is when the refresh ran, RFC 3339.
	RanAt string `json:"ran_at"`
	// OK is true when the scope indexed without error.
	OK bool `json:"ok"`
	// Error is the scope's failure, empty when OK.
	Error string `json:"error,omitempty"`
}

// refreshView is the JSON shape Refresh returns.
type refreshView struct {
	// OK is true when every replayed scope succeeded.
	OK bool `json:"ok"`
	// Results lists each replayed scope in source then scope order.
	Results []refreshResult `json:"results"`
	// Log is the end of the refresh's progress output.
	Log string `json:"log"`
}

// Refresh replays every recorded index run of the named sources at nowUnix,
// the host's clock in seconds since the Unix epoch. sources is a comma list,
// or empty for every recorded source. One scope failing, such as on an expired
// token, does not stop the others: the answer lists each scope with its error,
// and ok is false. An error is returned only when nothing could run.
func (h *Handle) Refresh(sources string, nowUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	var names []string
	for _, name := range strings.Split(sources, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := engine.SourceByName(name); !ok {
			return "", fmt.Errorf("core: refresh: %w: %q (want %s)", engine.ErrUnknownSource, name,
				engine.SourceNames())
		}
		names = append(names, name)
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if err := h.refuseSample(); err != nil {
		return "", err
	}
	now := time.Unix(nowUnix, 0)
	log := &tailLog{max: maxLog}
	results, err := h.indexer(now, log).Refresh(context.Background(), names, now)
	if err != nil && len(results) == 0 {
		return "", fmt.Errorf("core: refresh: %w", err)
	}
	v := refreshView{OK: err == nil, Results: []refreshResult{}}
	ranOK := false
	for _, r := range results {
		rr := refreshResult{
			Source: r.Source, Scope: r.Scope, RanAt: r.RanAt.UTC().Format(time.RFC3339), OK: r.OK,
		}
		if r.Err != nil {
			rr.Error = r.Err.Error()
		}
		ranOK = ranOK || r.OK
		v.Results = append(v.Results, rr)
	}
	if ranOK {
		if err := h.reload(); err != nil {
			return "", fmt.Errorf("core: refresh saved, but the index would not load: "+
				"open the data directory again: %w", err)
		}
	}
	v.Log = log.String()
	return toJSON(v)
}

// refuseSample returns engine.ErrSample when the index holds sample data, so real
// sources never merge into it.
func (h *Handle) refuseSample() error {
	loadedAt, err := h.store.LoadSample()
	if err != nil {
		return fmt.Errorf("core: %w", err)
	}
	if !loadedAt.IsZero() {
		return engine.ErrSample
	}
	return nil
}

// sampleView is the JSON shape ImportSample returns.
type sampleView struct {
	// People is the number of people the sample holds.
	People int `json:"people"`
	// Channels is the number of channels the sample holds.
	Channels int `json:"channels"`
	// Sources is the number of sources the sample was built from.
	Sources int `json:"sources"`
	// LoadedAt is when the sample was loaded, RFC 3339.
	LoadedAt string `json:"loaded_at"`
}

// ImportSample copies the sample company at dir, a plain index.json and
// episodes.json the app bundles, into the data directory under the handle's key
// at nowUnix, the host's clock in seconds since the Unix epoch. It refuses a
// directory holding real data, and loading over an earlier sample replaces it.
// The marker goes first, so an import cut short can never leave sample data that
// reads as real. Until a wipe, Index and Refresh refuse, since nothing about a
// simulated company can be refreshed.
func (h *Handle) ImportSample(dir string, nowUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	earlier, err := h.store.LoadSample()
	if err != nil {
		return "", fmt.Errorf("core: load sample: %w", err)
	}
	old := h.current()
	if old != nil && earlier.IsZero() {
		return "", engine.ErrRealIndex
	}
	now := time.Unix(nowUnix, 0)
	src := engine.NewStore(engine.Dir(dir), nil).WithNow(func() time.Time { return now })
	ix, err := src.LoadIndex()
	if err != nil {
		return "", fmt.Errorf("core: load sample: %w", err)
	}
	eps, err := src.LoadEpisodes()
	if err != nil {
		return "", fmt.Errorf("core: load sample: %w", err)
	}
	if err := h.store.SaveSample(now); err != nil {
		return "", fmt.Errorf("core: load sample: %w", err)
	}
	if err := h.saveSample(ix, eps, old, now); err != nil {
		return "", fmt.Errorf("core: load sample: %w", err)
	}
	if err := h.reload(); err != nil {
		return "", fmt.Errorf("core: load sample: saved, but it would not load: %w", err)
	}
	st := h.current().eng.Status(now)
	return toJSON(sampleView{
		People: st.People, Channels: st.Channels, Sources: len(st.Sources),
		LoadedAt: now.UTC().Format(time.RFC3339),
	})
}

// saveSample writes the sample under the handle's key and logs who it adds beside
// old, the sample it replaces, nil when there was none.
func (h *Handle) saveSample(ix *index.Index, eps *episode.Store, old *loaded, now time.Time) error {
	if err := h.store.SaveIndex(ix); err != nil {
		return err
	}
	if err := h.store.SaveEpisodes(eps); err != nil {
		return err
	}
	var prev *model.Graph
	if old != nil {
		prev = old.eng.Index().Graph
	}
	changes := index.Diff(prev, ix.Graph)
	if changes.Empty() {
		return nil
	}
	entries, err := h.store.LoadChanges()
	if err != nil {
		return err
	}
	return h.store.SaveChanges(append(entries, engine.ChangeEntry{At: now, Changes: changes}))
}

// wipeView is the JSON shape Wipe returns.
type wipeView struct {
	// Removed lists the files the wipe deleted.
	Removed []string `json:"removed"`
}

// Wipe deletes every file peepwho keeps in the data directory at dir, then the
// directory itself when nothing else is left in it. It needs no key, so a lost
// key never blocks a wipe. Files peepwho did not write are left in place. A
// handle open on dir still answers from memory afterward, so drop it. The
// host's own Keychain items are the host's to delete.
func Wipe(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("core: data directory is empty")
	}
	files, err := engine.PlanWipe(engine.Dir(dir))
	if err != nil {
		return "", fmt.Errorf("core: wipe: %w", err)
	}
	if err := engine.Wipe(engine.Dir(dir), files); err != nil {
		return "", fmt.Errorf("core: wipe: %w", err)
	}
	if files == nil {
		files = []string{}
	}
	return toJSON(wipeView{Removed: files})
}
