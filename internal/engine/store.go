// Package engine is the surface the command line, the web server, and the apps
// share: the data directory and the stores inside it, an opened index, and the
// choice of resolver under the egress policy. It never prompts and never reads
// a terminal, so a caller with no terminal, such as a menu bar app, uses it the
// same way the command line does.
package engine

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/fact"
	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/vault"
)

// Dir is a data directory and the names of the files peepwho keeps in it.
type Dir string

// Index returns the index file path.
func (d Dir) Index() string { return filepath.Join(string(d), "index.json") }

// Feedback returns the feedback file path. It is separate from the index so
// votes survive re-indexing.
func (d Dir) Feedback() string { return filepath.Join(string(d), "feedback.json") }

// Facts returns the facts file path. It is separate from the index so recorded
// facts survive re-indexing.
func (d Dir) Facts() string { return filepath.Join(string(d), "facts.json") }

// Refresh returns the refresh config path. It records the flags each source
// was last indexed with so a refresh can replay them without the operator
// retyping the scope.
func (d Dir) Refresh() string { return filepath.Join(string(d), "refresh.json") }

// Episodes returns the episode file path. Past conversations live beside the
// index rather than inside it, so a question about who knows something never
// pays to load them.
func (d Dir) Episodes() string { return filepath.Join(string(d), "episodes.json") }

// State returns the incremental watermark path. It holds per-source timestamps
// and scope names, sealed with the same codec as the index.
func (d Dir) State() string { return filepath.Join(string(d), "index.state.json") }

// Sources returns the path of the per-source records the index was built from.
func (d Dir) Sources() string { return index.SourcesPath(d.Index()) }

// Changes returns the change log path: what each fold added and removed, kept
// so an app can say what changed since the user last looked.
func (d Dir) Changes() string { return filepath.Join(string(d), "changes.json") }

// AttestKey returns the path of the seed every sealed finding is signed with.
func (d Dir) AttestKey() string { return filepath.Join(string(d), "attest.key") }

// Sample returns the path of the marker that says the index holds sample data.
func (d Dir) Sample() string { return filepath.Join(string(d), "sample.json") }

// Files returns every file peepwho keeps in the directory, in a fixed order.
func (d Dir) Files() []string {
	return []string{
		d.Index(), d.Sources(), d.State(), d.Episodes(), d.Feedback(), d.Facts(),
		d.Changes(), d.Refresh(), d.AttestKey(), d.Sample(),
	}
}

// Store reads and writes the files in a data directory under one at-rest
// codec. When a file is sealed and the store has no codec, a load returns an
// error matching vault.ErrEncrypted and the caller decides how to get a key.
type Store struct {
	// dir is the data directory every file lives under.
	dir Dir
	// codec seals and opens the files. A nil codec means plain JSON.
	codec vault.Codec
	// now is the clock a loaded index reads. Nil means the system clock.
	now func() time.Time
}

// NewStore returns a store over dir that seals with c, or reads and writes
// plain JSON when c is nil.
func NewStore(dir Dir, c vault.Codec) *Store {
	return &Store{dir: dir, codec: c}
}

// Dir returns the data directory.
func (s *Store) Dir() Dir { return s.dir }

// Codec returns the at-rest codec, nil for plain JSON.
func (s *Store) Codec() vault.Codec { return s.codec }

// WithCodec returns a store over the same directory and clock that seals with c.
func (s *Store) WithCodec(c vault.Codec) *Store {
	return &Store{dir: s.dir, codec: c, now: s.now}
}

// WithNow returns a store over the same directory and codec whose loaded index
// reads now as its clock, so a caller that pins the time gets reproducible
// recency and build times. A nil clock means the system clock.
func (s *Store) WithNow(now func() time.Time) *Store {
	return &Store{dir: s.dir, codec: s.codec, now: now}
}

// LoadIndex reads the index.
func (s *Store) LoadIndex() (*index.Index, error) {
	return index.Load(s.dir.Index(), index.WithCodec(s.codec), index.WithNow(s.now))
}

// SaveIndex writes the index.
func (s *Store) SaveIndex(ix *index.Index) error {
	return ix.Save(s.dir.Index(), index.WithCodec(s.codec))
}

// LoadSources reads the sources sidecar into ix, so a merge can rebuild from
// every source and not just the one being added.
func (s *Store) LoadSources(ix *index.Index) error {
	return ix.LoadSources(s.dir.Index(), index.WithCodec(s.codec))
}

// LoadEpisodes reads the episode store. A missing file is an empty store, since
// a run that has never indexed episodes has no history rather than a problem.
func (s *Store) LoadEpisodes() (*episode.Store, error) {
	return episode.LoadOrNew(s.dir.Episodes(), episode.WithCodec(s.codec))
}

// SaveEpisodes writes the episode store.
func (s *Store) SaveEpisodes(store *episode.Store) error {
	return store.Save(s.dir.Episodes(), episode.WithCodec(s.codec))
}

// LoadFeedback opens the feedback store, sealed with the same codec as the
// index so votes are never plaintext when the index is encrypted.
func (s *Store) LoadFeedback() (*feedback.Store, error) {
	return feedback.Load(s.dir.Feedback(), s.codec)
}

// LoadFacts opens the fact store, sealed with the same codec as the index. A
// fact's detail states in plain words what the index encryption exists to keep
// off a stolen disk, so the two are never split.
func (s *Store) LoadFacts() (*fact.Store, error) {
	return fact.Load(s.dir.Facts(), fact.WithCodec(s.codec))
}

// LoadChanges reads the change log, oldest entry first. A missing file is an
// empty log. It is sealed with the same codec as the index, since it names
// everyone who joined or left.
func (s *Store) LoadChanges() ([]ChangeEntry, error) {
	return readChanges(s.dir.Changes(), s.codec)
}

// SaveChanges writes the change log, keeping the newest MaxChangeEntries.
func (s *Store) SaveChanges(entries []ChangeEntry) error {
	return writeChanges(s.dir.Changes(), entries, s.codec)
}

// RewriteChanges re-persists an existing change log under c, reading it with
// the store's codec, so a vault encrypt seals it and a decrypt opens it. A
// missing log is left missing.
func (s *Store) RewriteChanges(c vault.Codec) error {
	if _, err := os.Stat(s.dir.Changes()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	entries, err := s.LoadChanges()
	if err != nil {
		return err
	}
	return writeChanges(s.dir.Changes(), entries, c)
}

// LoadState reads the incremental watermark state, returning an empty state
// when the file is absent so a first run is a full index rather than an error.
// It is sealed with the same codec as the index, since a scope names what a
// person reads.
func (s *Store) LoadState() (*state.State, error) {
	return state.Load(s.dir.State(), s.codec)
}

// SaveState writes the incremental watermark state.
func (s *Store) SaveState(st *state.State) error {
	return st.Save(s.dir.State(), s.codec)
}

// RewriteState re-persists existing watermarks under c, reading them with the
// store's codec, so a vault encrypt seals them and a decrypt opens them. A missing
// file is left missing.
func (s *Store) RewriteState(c vault.Codec) error {
	if _, err := os.Stat(s.dir.State()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	st, err := s.LoadState()
	if err != nil {
		return err
	}
	return s.WithCodec(c).SaveState(st)
}

// NeedsKey reports whether the file at path is sealed under a raw key, which no
// passphrase can open. A missing or unreadable file reports false, leaving the
// load itself to say what went wrong.
func NeedsKey(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, vault.ModeLen)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	return vault.NeedsKey(buf[:n])
}
