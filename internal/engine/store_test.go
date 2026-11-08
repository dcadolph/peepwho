package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/fact"
	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/simorg"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/vault"
)

// testKey is a fixed 32-byte raw key for sealing test stores.
var testKey = []byte("0123456789abcdef0123456789abcdef") //nolint:gochecknoglobals // Not modified, simplifies testing.

// newSimIndex builds the simulated company index in a fresh temp directory.
func newSimIndex(t *testing.T) *index.Index {
	t.Helper()
	ix, err := simorg.BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("simorg.BuildIndex: %v", err)
	}
	return ix
}

// testKeyCipher returns a raw-key codec over testKey.
func testKeyCipher(t *testing.T) vault.Codec {
	t.Helper()
	c, err := vault.NewKeyCipher(testKey)
	if err != nil {
		t.Fatalf("vault.NewKeyCipher: %v", err)
	}
	return c
}

// TestDirPaths verifies every file the engine names lives directly under the
// data directory with its fixed name.
func TestDirPaths(t *testing.T) {
	t.Parallel()
	d := Dir(filepath.Join("data", "dir"))
	got := []string{d.Index(), d.Feedback(), d.Facts(), d.Refresh(), d.Episodes(), d.State(), d.Changes(), d.Sample()}
	want := []string{
		filepath.Join("data", "dir", "index.json"),
		filepath.Join("data", "dir", "feedback.json"),
		filepath.Join("data", "dir", "facts.json"),
		filepath.Join("data", "dir", "refresh.json"),
		filepath.Join("data", "dir", "episodes.json"),
		filepath.Join("data", "dir", "index.state.json"),
		filepath.Join("data", "dir", "changes.json"),
		filepath.Join("data", "dir", "sample.json"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("paths mismatch (-want +got):\n%s", diff)
	}
}

// TestStoreIndexRoundTrip verifies an index saved through a store loads back
// through a store with the same codec, and that a sealed index loaded with no
// codec fails as encrypted rather than as damaged or empty.
func TestStoreIndexRoundTrip(t *testing.T) {
	t.Parallel()
	ix := newSimIndex(t)
	tests := []struct {
		Want       error
		Sealed     bool
		LoadSealed bool
		WantKey    bool
	}{{  // Test 0: Plain save, plain load.
	}, { // Test 1: Sealed save, sealed load.
		Sealed: true, LoadSealed: true, WantKey: true,
	}, { // Test 2: Sealed save, load with no key.
		Sealed: true, Want: vault.ErrEncrypted, WantKey: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := Dir(t.TempDir())
			var saveCodec, loadCodec vault.Codec
			if test.Sealed {
				saveCodec = testKeyCipher(t)
			}
			if test.LoadSealed {
				loadCodec = testKeyCipher(t)
			}
			if err := NewStore(dir, saveCodec).SaveIndex(ix); err != nil {
				t.Fatalf("SaveIndex: %v", err)
			}
			if got := NeedsKey(dir.Index()); got != test.WantKey {
				t.Errorf("NeedsKey = %t, want %t", got, test.WantKey)
			}
			got, err := NewStore(dir, loadCodec).LoadIndex()
			if !errors.Is(err, test.Want) {
				t.Fatalf("LoadIndex error = %v, want %v", err, test.Want)
			}
			if test.Want != nil {
				return
			}
			if diff := cmp.Diff(len(ix.Graph.People), len(got.Graph.People)); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStoreWithCodec verifies WithCodec keeps the directory and swaps only the
// codec, leaving the original store unchanged.
func TestStoreWithCodec(t *testing.T) {
	t.Parallel()
	plain := NewStore(Dir("d"), nil)
	sealed := plain.WithCodec(testKeyCipher(t))
	if sealed.Dir() != plain.Dir() {
		t.Errorf("WithCodec dir = %q, want %q", sealed.Dir(), plain.Dir())
	}
	if plain.Codec() != nil {
		t.Error("WithCodec changed the original store's codec")
	}
	if sealed.Codec() == nil {
		t.Error("WithCodec returned a store with no codec")
	}
}

// TestStoreWithNow verifies that the store's clock survives WithCodec and reaches
// every loaded index: an index loaded and saved again records the pinned clock
// as its build time.
func TestStoreWithNow(t *testing.T) {
	t.Parallel()
	pinned := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := NewStore(Dir(t.TempDir()), nil).
		WithNow(func() time.Time { return pinned }).
		WithCodec(testKeyCipher(t))
	if err := st.SaveIndex(index.New()); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	ix, err := st.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if err := st.SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex again: %v", err)
	}
	if ix, err = st.LoadIndex(); err != nil {
		t.Fatalf("LoadIndex again: %v", err)
	}
	if got := ix.BuiltAt(); !got.Equal(pinned) {
		t.Errorf("BuiltAt() = %v, want the pinned %v", got, pinned)
	}
}

// TestStoreSidecars verifies the episode, feedback, fact, and state files load
// empty when absent and round-trip through the store, sealed when the store
// has a codec.
func TestStoreSidecars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Sealed bool
	}{
		{Sealed: false}, // Test 0: Plain stores.
		{Sealed: true},  // Test 1: Sealed stores.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var c vault.Codec
			if test.Sealed {
				c = testKeyCipher(t)
			}
			s := NewStore(Dir(t.TempDir()), c)

			eps, err := s.LoadEpisodes()
			if err != nil {
				t.Fatalf("LoadEpisodes on an empty dir: %v", err)
			}
			if eps.Len() != 0 {
				t.Errorf("empty dir episodes = %d, want 0", eps.Len())
			}
			if err := s.SaveEpisodes(eps); err != nil {
				t.Fatalf("SaveEpisodes: %v", err)
			}
			if _, err := s.LoadEpisodes(); err != nil {
				t.Fatalf("LoadEpisodes after save: %v", err)
			}

			fb, err := s.LoadFeedback()
			if err != nil {
				t.Fatalf("LoadFeedback: %v", err)
			}
			vote := feedback.Entry{
				Query: "billing retries", Person: "angela@corp.com", Vote: feedback.Helpful,
				Time: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
			}
			if err := fb.Add(vote); err != nil {
				t.Fatalf("feedback Add: %v", err)
			}
			fb2, err := s.LoadFeedback()
			if err != nil {
				t.Fatalf("LoadFeedback after add: %v", err)
			}
			if diff := cmp.Diff(1, len(fb2.All())); diff != "" {
				t.Errorf("votes mismatch (-want +got):\n%s", diff)
			}

			facts, err := s.LoadFacts()
			if err != nil {
				t.Fatalf("LoadFacts: %v", err)
			}
			if got := len(facts.List(fact.Filter{})); got != 0 {
				t.Errorf("empty dir facts = %d, want 0", got)
			}

			st, err := s.LoadState()
			if err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			if err := s.SaveState(st); err != nil {
				t.Fatalf("SaveState: %v", err)
			}
			if _, err := s.LoadState(); err != nil {
				t.Fatalf("state reload: %v", err)
			}

			if !test.Sealed {
				return
			}
			for _, p := range []string{s.Dir().Feedback(), s.Dir().Episodes()} {
				if !NeedsKey(p) {
					t.Errorf("%s is not sealed under the key", filepath.Base(p))
				}
			}
			if _, err := NewStore(s.Dir(), nil).LoadFeedback(); err == nil {
				t.Error("sealed feedback loaded with no key")
			}
		})
	}
}

// TestNeedsKey verifies the raw-key probe on missing, short, plain, and
// passphrase-sealed files.
func TestNeedsKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	pass, err := vault.NewPassphraseCipher([]byte("correct horse")).Encode([]byte("{}"))
	if err != nil {
		t.Fatalf("passphrase Encode: %v", err)
	}
	keyed, err := testKeyCipher(t).Encode([]byte("{}"))
	if err != nil {
		t.Fatalf("key Encode: %v", err)
	}
	tests := []struct {
		Path string
		Want bool
	}{
		{Path: filepath.Join(dir, "missing.json")},          // Test 0: Missing file.
		{Path: write("empty.json", nil)},                    // Test 1: Empty file.
		{Path: write("plain.json", []byte(`{"a":1}`))},      // Test 2: Plain JSON.
		{Path: write("pass.json", pass)},                    // Test 3: Passphrase seal.
		{Path: write("key.json", keyed), Want: true},        // Test 4: Raw-key seal.
		{Path: write("short.json", keyed[:3]), Want: false}, // Test 5: Truncated header.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := NeedsKey(test.Path); got != test.Want {
				t.Errorf("NeedsKey(%s) = %t, want %t", filepath.Base(test.Path), got, test.Want)
			}
		})
	}
}

// TestStoreChangesCapAndRewrite verifies a save keeps the newest
// MaxChangeEntries entries, and a rewrite seals and then opens the log in place.
func TestStoreChangesCapAndRewrite(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	entries := make([]ChangeEntry, MaxChangeEntries+1)
	for i := range entries {
		entries[i] = ChangeEntry{
			At:      start.Add(time.Duration(i) * time.Minute),
			Changes: index.Changes{PeopleJoined: []string{fmt.Sprintf("p%03d", i)}},
		}
	}
	dir := Dir(t.TempDir())
	plain := NewStore(dir, nil)
	if err := plain.SaveChanges(entries); err != nil {
		t.Fatalf("SaveChanges: %v", err)
	}
	got, err := plain.LoadChanges()
	if err != nil {
		t.Fatalf("LoadChanges: %v", err)
	}
	if diff := cmp.Diff(entries[1:], got); diff != "" {
		t.Errorf("capped log mismatch (-want +got):\n%s", diff)
	}

	key := testKeyCipher(t)
	if err := plain.RewriteChanges(key); err != nil {
		t.Fatalf("RewriteChanges sealed: %v", err)
	}
	if _, err := plain.LoadChanges(); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("plain load of a sealed log = %v, want %v", err, vault.ErrEncrypted)
	}
	sealed := NewStore(dir, key)
	if err := sealed.RewriteChanges(nil); err != nil {
		t.Fatalf("RewriteChanges plain: %v", err)
	}
	got, err = plain.LoadChanges()
	if err != nil {
		t.Fatalf("LoadChanges after opening: %v", err)
	}
	if diff := cmp.Diff(entries[1:], got); diff != "" {
		t.Errorf("reopened log mismatch (-want +got):\n%s", diff)
	}

	missing := NewStore(Dir(t.TempDir()), nil)
	if err := missing.RewriteChanges(key); err != nil {
		t.Fatalf("RewriteChanges with no log: %v", err)
	}
	if _, err := os.Stat(missing.Dir().Changes()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a rewrite with no log created one: %v", err)
	}
}

// TestForgetChangeLabels verifies matching labels leave the joined and left
// lists, entries left empty are dropped, and every edited entry is counted.
func TestForgetChangeLabels(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		In         []ChangeEntry
		Labels     []string
		WantResult []ChangeEntry
		WantCount  int
	}{{ // Test 0: Name and email match after case folding and trimming.
		In: []ChangeEntry{
			{At: at, Changes: index.Changes{PeopleJoined: []string{"Bob Okafor", "Zed"}}},
			{At: at, Changes: index.Changes{PeopleLeft: []string{"BOB@corp.com"}, TeamsAdded: []string{"pay"}}},
		},
		Labels: []string{" bob okafor ", "bob@corp.com"},
		WantResult: []ChangeEntry{
			{At: at, Changes: index.Changes{PeopleJoined: []string{"Zed"}}},
			{At: at, Changes: index.Changes{TeamsAdded: []string{"pay"}}},
		},
		WantCount: 2,
	}, { // Test 1: An entry naming only the person is dropped.
		In:        []ChangeEntry{{At: at, Changes: index.Changes{PeopleJoined: []string{"Bob Okafor"}}}},
		Labels:    []string{"Bob Okafor"},
		WantCount: 1,
	}, { // Test 2: A label that only contains the name is kept.
		In:         []ChangeEntry{{At: at, Changes: index.Changes{PeopleJoined: []string{"Bob Okafor Jr"}}}},
		Labels:     []string{"Bob Okafor", ""},
		WantResult: []ChangeEntry{{At: at, Changes: index.Changes{PeopleJoined: []string{"Bob Okafor Jr"}}}},
	}, { // Test 3: Empty log.
		Labels: []string{"Bob Okafor"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, count := ForgetChangeLabels(test.In, test.Labels)
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("entries mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantCount, count); diff != "" {
				t.Errorf("count mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRecordsSealUnderCodec verifies the refresh config and watermarks seal under the
// store's codec on write, read back only with it, and open a plain file from before,
// which seals on its next write.
func TestRecordsSealUnderCodec(t *testing.T) {
	t.Parallel()
	const scope = "project:PLANTED"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key, err := vault.NewKeyCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := vault.NewKeyCipher([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	dir := Dir(t.TempDir())
	plain, sealed := NewStore(dir, nil), NewStore(dir, key)

	if err := plain.RecordInvocation("jira", scope, []string{"--jira-project", "PLANTED"}, now); err != nil {
		t.Fatalf("plain RecordInvocation: %v", err)
	}
	wm := state.New()
	wm.Set(state.Watermark{Source: "jira", Scope: scope, Cursor: now, RanAt: now})
	if err := plain.SaveState(wm); err != nil {
		t.Fatalf("plain SaveState: %v", err)
	}
	if err := sealed.RecordInvocation("jira", "project:SECOND", []string{"--jira-project", "SECOND"}, now); err != nil {
		t.Fatalf("sealed RecordInvocation over a plain file: %v", err)
	}
	got, err := sealed.LoadState()
	if err != nil {
		t.Fatalf("sealed LoadState over a plain file: %v", err)
	}
	if err := sealed.SaveState(got); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	for _, path := range []string{dir.Refresh(), dir.State()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !vault.IsEncrypted(raw) || bytes.Contains(raw, []byte("PLANTED")) {
			t.Errorf("%s is not sealed: %s", filepath.Base(path), raw)
		}
	}
	inv, err := sealed.LoadInvocations()
	if err != nil {
		t.Fatalf("LoadInvocations: %v", err)
	}
	if len(inv["jira"]) != 2 {
		t.Errorf("refresh holds %d scopes, want the plain one and the new one: %v", len(inv["jira"]), inv)
	}
	if back, err := sealed.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	} else if _, ok := back.Get("jira", scope); !ok {
		t.Errorf("watermark lost across the seal: %v", back.Watermarks)
	}

	if _, err := plain.LoadInvocations(); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("LoadInvocations with no key = %v, want %v", err, vault.ErrEncrypted)
	}
	if _, err := plain.LoadState(); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("LoadState with no key = %v, want %v", err, vault.ErrEncrypted)
	}
	wrong := NewStore(dir, other)
	if _, err := wrong.LoadInvocations(); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("LoadInvocations with the wrong key = %v, want %v", err, vault.ErrCorrupt)
	}
	if _, err := wrong.LoadState(); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("LoadState with the wrong key = %v, want %v", err, vault.ErrCorrupt)
	}

	if err := sealed.RewriteInvocations(nil); err != nil {
		t.Fatalf("RewriteInvocations: %v", err)
	}
	if err := sealed.RewriteState(nil); err != nil {
		t.Fatalf("RewriteState: %v", err)
	}
	if _, err := plain.LoadInvocations(); err != nil {
		t.Errorf("LoadInvocations after a rewrite to plain: %v", err)
	}
	if _, err := plain.LoadState(); err != nil {
		t.Errorf("LoadState after a rewrite to plain: %v", err)
	}
	missing := NewStore(Dir(t.TempDir()), nil)
	if err := missing.RewriteInvocations(key); err != nil {
		t.Errorf("RewriteInvocations with no file: %v", err)
	}
	if err := missing.RewriteState(key); err != nil {
		t.Errorf("RewriteState with no file: %v", err)
	}
	if _, err := os.Stat(missing.Dir().Refresh()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a rewrite made a refresh config from nothing: %v", err)
	}
	if _, err := os.Stat(missing.Dir().State()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a rewrite made watermarks from nothing: %v", err)
	}
}

// TestIndexerUnlocksSealedRecords verifies a run with no configured codec asks Unlock for
// sealed records, since a terminal passphrase reaches them before it reaches the index,
// and keeps the codec it gets for what it writes next.
func TestIndexerUnlocksSealedRecords(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key, err := vault.NewKeyCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	dir := Dir(t.TempDir())
	sealed := NewStore(dir, key)
	if err := sealed.RecordInvocation("jira", "project:A", []string{"--jira-project", "A"}, now); err != nil {
		t.Fatal(err)
	}
	wm := state.New()
	wm.Set(state.Watermark{Source: "jira", Scope: "project:A", Cursor: now, RanAt: now})
	if err := sealed.SaveState(wm); err != nil {
		t.Fatal(err)
	}
	unlocks := 0
	unlock := func(_ string, try func(vault.Codec) error) (vault.Codec, error) {
		unlocks++
		return key, try(key)
	}

	x := &Indexer{Dir: dir, Unlock: unlock}
	if inv, err := x.loadInvocations(); err != nil || len(inv["jira"]) != 1 {
		t.Errorf("loadInvocations = %v, %v; want one scope", inv, err)
	}
	if _, err := x.loadState(); err != nil {
		t.Errorf("loadState after an unlock: %v", err)
	}
	if unlocks != 1 {
		t.Errorf("unlocks = %d, want 1", unlocks)
	}

	y := &Indexer{Dir: dir, Unlock: unlock}
	if st, err := y.loadState(); err != nil {
		t.Errorf("loadState = %v", err)
	} else if _, ok := st.Get("jira", "project:A"); !ok {
		t.Errorf("loadState lost the watermark: %v", st.Watermarks)
	}
	if unlocks != 2 {
		t.Errorf("unlocks = %d, want 2", unlocks)
	}

	z := &Indexer{Dir: dir}
	if _, err := z.loadInvocations(); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("loadInvocations with no way in = %v, want %v", err, vault.ErrEncrypted)
	}
	if _, err := z.loadState(); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("loadState with no way in = %v, want %v", err, vault.ErrEncrypted)
	}
}
