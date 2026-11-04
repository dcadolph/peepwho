package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

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
	got := []string{d.Index(), d.Feedback(), d.Facts(), d.Refresh(), d.Episodes(), d.State()}
	want := []string{
		filepath.Join("data", "dir", "index.json"),
		filepath.Join("data", "dir", "feedback.json"),
		filepath.Join("data", "dir", "facts.json"),
		filepath.Join("data", "dir", "refresh.json"),
		filepath.Join("data", "dir", "episodes.json"),
		filepath.Join("data", "dir", "index.state.json"),
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
			if _, err := state.Load(s.Dir().State()); err != nil {
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
