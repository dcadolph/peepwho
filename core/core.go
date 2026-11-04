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
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/vault"
)

// defaultLimit is the result count used when a caller passes zero or less.
const defaultLimit = 5

// maxLimit caps results per call so a bad argument cannot ask for everything.
const maxLimit = 100

// Handle is an opened peepwho data directory usable from Swift.
type Handle struct {
	// eng answers from the opened index under the strict policy.
	eng *engine.Engine
	// episodes holds the conversations recall answers from.
	episodes *episode.Store
	// sealed reports whether the index was opened with a key.
	sealed bool
	// store reads the files beside the index, such as the change log.
	store *engine.Store
	// secrets supplies source tokens by name. Nil reads every name as empty.
	secrets Secrets
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
	h.secrets = s
}

// secret returns the credential stored under name, or empty when the handle
// has no Secrets or the host has none by that name.
func (h *Handle) secret(name string) string {
	if h == nil || h.secrets == nil {
		return ""
	}
	return h.secrets.Get(name)
}

// Open opens the data directory at dir. key is the base64 index key that
// `peepwho vault keygen` prints and PEEPWHO_INDEX_KEY holds, or empty for a
// plain index. A sealed index opened with no key, or with the wrong key, fails
// here with an error that names the key, rather than on the first question.
func Open(dir, key string) (*Handle, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("core: data directory is empty")
	}
	codec, err := keyCodec(key)
	if err != nil {
		return nil, err
	}
	st := engine.NewStore(engine.Dir(dir), codec)
	ix, err := st.LoadIndex()
	if err != nil {
		return nil, openError(st.Dir().Index(), codec, err)
	}
	eng := engine.New(ix, policy.Default())
	fb, err := st.LoadFeedback()
	if err != nil {
		return nil, openError(st.Dir().Feedback(), codec, err)
	}
	eng.SetFeedback(fb)
	eps, err := st.LoadEpisodes()
	if err != nil {
		return nil, openError(st.Dir().Episodes(), codec, err)
	}
	return &Handle{eng: eng, episodes: eps, sealed: codec != nil, store: st}, nil
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

// ready reports whether the handle came from Open. gomobile exposes a bare
// initializer for every struct, so a Swift caller can hold an empty handle.
func (h *Handle) ready() error {
	if h == nil || h.eng == nil || h.episodes == nil || h.store == nil {
		return errNotOpen
	}
	return nil
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
	if err := h.ready(); err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("core: ask needs a question")
	}
	ans, err := h.eng.Ask(context.Background(), query, clampLimit(limit), engine.ResolverConfig{})
	if err != nil {
		return "", fmt.Errorf("core: ask: %w", err)
	}
	return toJSON(ans.View(query))
}

// Search finds people and channels by name, email, title, team, or topic.
func (h *Handle) Search(query string, limit int) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("core: search needs a query")
	}
	return toJSON(map[string]any{"query": query, "results": h.eng.Search(query, clampLimit(limit))})
}

// Person returns the full profile of the person with canonical id.
func (h *Handle) Person(id string) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	p, ok := h.eng.Person(strings.TrimSpace(id))
	if !ok {
		return "", fmt.Errorf("core: unknown person: %s", id)
	}
	return toJSON(p)
}

// Recall finds past conversations about query that person took part in. person
// is an email or a source identifier such as slack:U123; the answer covers only
// that person's own conversations.
func (h *Handle) Recall(person, query string, limit int) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	res := h.eng.Recaller(h.episodes)
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
}

// Status reports what the index holds and how old it is at nowUnix, the host's
// clock in seconds since the Unix epoch.
func (h *Handle) Status(nowUnix int64) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	st := h.eng.Status(time.Unix(nowUnix, 0))
	v := statusView{
		People: st.People, Channels: st.Channels, Teams: st.Teams,
		Subjects: st.Subjects, Words: st.Words, Sources: st.Sources,
		Embeddings: st.Embeddings, Conversations: h.episodes.Len(), Sealed: h.sealed,
	}
	if !st.BuiltAt.IsZero() {
		v.BuiltAt = st.BuiltAt.UTC().Format(time.RFC3339)
		v.AgeSeconds = int64(st.Age / time.Second)
	}
	return toJSON(v)
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
