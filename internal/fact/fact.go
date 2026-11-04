package fact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vault"
)

// knownRelations is the closed set of relations a fact may assert. A relation
// outside it is rejected so a typo fails loudly rather than recording a
// statement nothing will ever match.
//
//nolint:gochecknoglobals // Read-only lookup table.
var knownRelations = map[string]bool{
	"owned_by":                true,
	"not_owned_by":            true,
	"escalates_to":            true,
	"reports_to":              true,
	"runs_on":                 true,
	"answers_questions_about": true,
}

// Relations returns the known relations in sorted order, for help text and
// error messages.
func Relations() []string {
	out := slices.Sorted(maps.Keys(knownRelations))
	return out
}

// Fact is one typed statement: subject relation object, with the source that
// asserted it and when it was recorded. Subject and object are free-form refs,
// by convention kind:name such as team:payments or service:checkout.
type Fact struct {
	// Subject is what the fact is about, e.g. team:payments.
	Subject string `json:"subject"`
	// Relation is one of the known relations, e.g. not_owned_by.
	Relation string `json:"relation"`
	// Object is the other end of the relation, e.g. service:checkout.
	Object string `json:"object"`
	// Detail is an optional human note explaining the fact.
	Detail string `json:"detail,omitempty"`
	// Source names who asserted the fact, e.g. catalog or curated.
	Source string `json:"source,omitempty"`
	// Time is when the fact was recorded.
	Time time.Time `json:"time"`
}

// Valid reports why a fact is not storable, or nil when it is.
func (f Fact) Valid() error {
	if strings.TrimSpace(f.Subject) == "" {
		return fmt.Errorf("%w: subject is empty", ErrBadFact)
	}
	if strings.TrimSpace(f.Object) == "" {
		return fmt.Errorf("%w: object is empty", ErrBadFact)
	}
	if !knownRelations[f.Relation] {
		return fmt.Errorf("%w: unknown relation %q, want one of %s",
			ErrBadFact, f.Relation, strings.Join(Relations(), ", "))
	}
	return nil
}

// Store holds facts and persists them as JSON. It is safe for concurrent use
// within a process and guards its file with a cross-process lock.
type Store struct {
	// mu guards facts.
	mu sync.Mutex
	// facts are the recorded facts, oldest first.
	facts []Fact
	// path is the file the store persists to.
	path string
	// codec seals and opens the file, matching the index at-rest encryption. A
	// nil codec stores plain JSON. A fact's detail can hold exactly what the
	// index encryption exists to keep off a stolen disk, so the two travel
	// together.
	codec vault.Codec
}

// Option configures how a store reads and writes its file.
type Option func(*Store)

// WithCodec has the store encode file contents with c. A nil codec is ignored,
// leaving the store on plain JSON.
func WithCodec(c vault.Codec) Option {
	return func(s *Store) {
		if c != nil {
			s.codec = c
		}
	}
}

// Load reads a store from path. A missing file yields an empty store.
func Load(path string, opts ...Option) (*Store, error) {
	s := &Store{path: path}
	for _, opt := range opts {
		opt(s)
	}
	facts, err := readFacts(path, s.codec)
	if err != nil {
		return nil, err
	}
	s.facts = facts
	return s, nil
}

// Add records one fact, defaulting its time and merging with any facts another
// process wrote since this store last read the file.
func (s *Store) Add(f Fact) error {
	if err := f.Valid(); err != nil {
		return err
	}
	if f.Time.IsZero() {
		f.Time = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func(cur []Fact) []Fact {
		return append(cur, f)
	})
}

// Filter selects facts to list or forget. Zero fields match everything; set
// fields must all match, case-insensitively.
type Filter struct {
	// Subject matches this subject.
	Subject string
	// Relation matches this relation.
	Relation string
	// Object matches this object.
	Object string
	// Source matches this source, so forgetting a whole source is one call.
	Source string
}

// isZero reports whether the filter would match every fact.
func (f Filter) isZero() bool {
	return f.Subject == "" && f.Relation == "" && f.Object == "" && f.Source == ""
}

// matches reports whether fct satisfies every set field of f.
func (f Filter) matches(fct Fact) bool {
	if f.Subject != "" && !strings.EqualFold(f.Subject, fct.Subject) {
		return false
	}
	if f.Relation != "" && !strings.EqualFold(f.Relation, fct.Relation) {
		return false
	}
	if f.Object != "" && !strings.EqualFold(f.Object, fct.Object) {
		return false
	}
	if f.Source != "" && !strings.EqualFold(f.Source, fct.Source) {
		return false
	}
	return true
}

// List returns the facts matching f, oldest first.
func (s *Store) List(f Filter) []Fact {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Fact
	for _, fct := range s.facts {
		if f.matches(fct) {
			out = append(out, fct)
		}
	}
	return out
}

// Forget removes the facts matching f, persists the store, and returns how many
// were removed. An empty filter removes nothing, so a bare forget cannot wipe
// the store by accident.
func (s *Store) Forget(f Filter) (int, error) {
	if f.isZero() {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := s.mutate(func(cur []Fact) []Fact {
		kept := make([]Fact, 0, len(cur))
		for _, fct := range cur {
			if f.matches(fct) {
				removed++
				continue
			}
			kept = append(kept, fct)
		}
		return kept
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ForgetMatching removes the facts match reports true for, persists the store,
// and returns how many were removed. It exists for purges that no field filter
// can express, such as every fact naming one of a person's identities.
func (s *Store) ForgetMatching(match func(Fact) bool) (int, error) {
	if match == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := s.mutate(func(cur []Fact) []Fact {
		kept := make([]Fact, 0, len(cur))
		for _, fct := range cur {
			if match(fct) {
				removed++
				continue
			}
			kept = append(kept, fct)
		}
		return kept
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// Rewrite re-persists the file's current contents under codec, so `vault
// encrypt` can seal an existing plain file and `vault decrypt` can open a
// sealed one. The file is read with the codec the store was loaded with and
// written with the new one, which the store keeps for its later writes.
func (s *Store) Rewrite(codec vault.Codec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := util.LockFile(s.path + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := readFacts(s.path, s.codec)
	if err != nil {
		return err
	}
	if err := saveFacts(s.path, cur, codec); err != nil {
		return err
	}
	s.facts = cur
	s.codec = codec
	return nil
}

// Import reads a JSON array of facts from r and folds them in. When
// replaceSource is set, every existing fact from that source is dropped first
// and each imported fact without a source is labeled with it, so an import is
// the whole of what a source currently asserts. It returns how many were added.
func (s *Store) Import(r io.Reader, replaceSource string) (int, error) {
	var incoming []Fact
	if err := json.NewDecoder(r).Decode(&incoming); err != nil {
		return 0, fmt.Errorf("fact: decode import: %w", err)
	}
	for i := range incoming {
		if incoming[i].Source == "" {
			incoming[i].Source = replaceSource
		}
		if incoming[i].Time.IsZero() {
			incoming[i].Time = time.Now()
		}
		if err := incoming[i].Valid(); err != nil {
			return 0, fmt.Errorf("fact: import record %d: %w", i, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.mutate(func(cur []Fact) []Fact {
		next := cur
		if replaceSource != "" {
			next = next[:0:0]
			for _, fct := range cur {
				if !strings.EqualFold(fct.Source, replaceSource) {
					next = append(next, fct)
				}
			}
		}
		return append(next, incoming...)
	})
	if err != nil {
		return 0, err
	}
	return len(incoming), nil
}

// mutate applies fn to the file's current contents under a cross-process lock,
// persists the result atomically, and only then swaps the in-memory facts, so a
// failed write never leaves memory ahead of disk and a concurrent process's
// facts are not lost. Callers hold s.mu.
func (s *Store) mutate(fn func([]Fact) []Fact) error {
	unlock, err := util.LockFile(s.path + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()

	cur, err := readFacts(s.path, s.codec)
	if err != nil {
		return err
	}
	next := fn(cur)
	if err := saveFacts(s.path, next, s.codec); err != nil {
		return err
	}
	s.facts = next
	return nil
}

// readFacts reads the facts at path, decoding with codec. A missing file
// yields no facts. With a nil codec an encrypted file returns
// vault.ErrEncrypted rather than a parse error, so the caller can point at the
// missing key.
func readFacts(path string, codec vault.Codec) ([]Fact, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fact: read %s: %w", path, err)
	}
	if codec == nil {
		codec = vault.Plain{}
	}
	if raw, err = codec.Decode(raw); err != nil {
		return nil, fmt.Errorf("fact: decode %s: %w", path, err)
	}
	var facts []Fact
	if err := json.Unmarshal(raw, &facts); err != nil {
		return nil, fmt.Errorf("fact: parse %s: %w", path, err)
	}
	return facts, nil
}

// saveFacts writes facts to path atomically, sealing them when codec is set.
func saveFacts(path string, facts []Fact, codec vault.Codec) error {
	raw, err := json.Marshal(facts)
	if err != nil {
		return fmt.Errorf("fact: encode: %w", err)
	}
	if codec != nil {
		if raw, err = codec.Encode(raw); err != nil {
			return fmt.Errorf("fact: seal: %w", err)
		}
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("fact: write %s: %w", path, err)
	}
	return nil
}
