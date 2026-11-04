package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/simorg"
	"github.com/dcadolph/peepwho/internal/vault"
)

// testKey is a fixed 32-byte raw key for sealing test data directories.
var testKey = []byte("0123456789abcdef0123456789abcdef") //nolint:gochecknoglobals // Not modified, simplifies testing.

// testKeyB64 is testKey in the form peepwho vault keygen prints.
var testKeyB64 = base64.StdEncoding.EncodeToString(testKey) //nolint:gochecknoglobals // Not modified, simplifies testing.

// newTestDataDir writes the simulated company and its conversations to a temp
// data directory sealed with c, or plain when c is nil.
func newTestDataDir(t *testing.T, c vault.Codec) string {
	t.Helper()
	ix, err := simorg.BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("simorg.BuildIndex: %v", err)
	}
	eps, err := simorg.BuildEpisodes(ix)
	if err != nil {
		t.Fatalf("simorg.BuildEpisodes: %v", err)
	}
	dir := t.TempDir()
	st := engine.NewStore(engine.Dir(dir), c)
	if err := st.SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	if err := st.SaveEpisodes(eps); err != nil {
		t.Fatalf("SaveEpisodes: %v", err)
	}
	return dir
}

// testKeyCipher returns the raw-key codec over testKey.
func testKeyCipher(t *testing.T) vault.Codec {
	t.Helper()
	c, err := vault.NewKeyCipher(testKey)
	if err != nil {
		t.Fatalf("vault.NewKeyCipher: %v", err)
	}
	return c
}

// TestOpen verifies plain and sealed directories open with the right key, and
// that every failure an app can fix names the key instead of crashing or
// opening an empty index.
func TestOpen(t *testing.T) {
	t.Parallel()
	plain := newTestDataDir(t, nil)
	keyed := newTestDataDir(t, testKeyCipher(t))
	passed := newTestDataDir(t, vault.NewPassphraseCipher([]byte("correct horse")))
	otherKey := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	tests := []struct {
		Want        error
		Dir         string
		Key         string
		WantMessage string
	}{{ // Test 0: Plain directory, no key.
		Dir: plain,
	}, { // Test 1: Sealed directory, right key.
		Dir: keyed, Key: testKeyB64,
	}, { // Test 2: Sealed directory, right key with surrounding whitespace.
		Dir: keyed, Key: " " + testKeyB64 + "\n",
	}, { // Test 3: Sealed directory, no key.
		Dir: keyed, Want: vault.ErrEncrypted, WantMessage: "index key",
	}, { // Test 4: Sealed directory, wrong key.
		Dir: keyed, Key: otherKey, Want: vault.ErrCorrupt, WantMessage: "index key does not open",
	}, { // Test 5: Key that is not base64.
		Dir: keyed, Key: "not base64!", WantMessage: "must be base64",
	}, { // Test 6: Key of the wrong length.
		Dir: keyed, Key: base64.StdEncoding.EncodeToString([]byte("short")), WantMessage: "index key",
	}, { // Test 7: Passphrase-sealed directory, no key.
		Dir: passed, Want: vault.ErrEncrypted, WantMessage: "sealed with a passphrase",
	}, { // Test 8: Empty directory argument.
		Dir: "  ", WantMessage: "data directory is empty",
	}, { // Test 9: Directory with no index.
		Dir: t.TempDir(), WantMessage: "open",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			h, err := Open(test.Dir, test.Key)
			if test.WantMessage == "" {
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				if h == nil {
					t.Fatal("Open returned a nil handle and no error")
				}
				return
			}
			if err == nil {
				t.Fatalf("Open succeeded, want an error containing %q", test.WantMessage)
			}
			if h != nil {
				t.Error("Open returned a handle alongside an error")
			}
			if test.Want != nil && !errors.Is(err, test.Want) {
				t.Errorf("Open error = %v, want %v", err, test.Want)
			}
			if !strings.Contains(err.Error(), test.WantMessage) {
				t.Errorf("Open error %q does not contain %q", err, test.WantMessage)
			}
		})
	}
}

// TestHandleAskMatchesEngine verifies the core answer is the same JSON the
// engine's keyword answer encodes to, so an app and the command line agree.
func TestHandleAskMatchesEngine(t *testing.T) {
	t.Parallel()
	dir := newTestDataDir(t, testKeyCipher(t))
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := engine.NewStore(engine.Dir(dir), testKeyCipher(t))
	ix, err := st.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	ans, err := engine.New(ix, policy.Default()).Ask(context.Background(), "billing retries", 5,
		engine.ResolverConfig{})
	if err != nil {
		t.Fatalf("engine Ask: %v", err)
	}
	want, err := json.Marshal(ans.View("billing retries"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := h.Ask("  billing retries ", 0)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("ask mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(got, `"angela@corp.com"`) {
		t.Errorf("ask answer does not name the billing owner: %s", got)
	}
}

// TestHandleCalls verifies each call's success and failure shapes.
func TestHandleCalls(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, nil), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tests := []struct {
		Call         func() (string, error)
		WantContains string
		WantErr      string
	}{{ // Test 0: Empty question.
		Call: func() (string, error) { return h.Ask(" ", 5) }, WantErr: "needs a question",
	}, { // Test 1: Search by name.
		Call: func() (string, error) { return h.Search("angela", 3) }, WantContains: `"angela@corp.com"`,
	}, { // Test 2: Empty search.
		Call: func() (string, error) { return h.Search("", 3) }, WantErr: "needs a query",
	}, { // Test 3: Known person.
		Call: func() (string, error) { return h.Person("angela@corp.com") }, WantContains: `"Angela Malone"`,
	}, { // Test 4: Unknown person.
		Call: func() (string, error) { return h.Person("nobody@nowhere.example") }, WantErr: "unknown person",
	}, { // Test 5: Recall for the demo person.
		Call:         func() (string, error) { return h.Recall("angela@corp.com", "billing retries", 5) },
		WantContains: `"episodes":[{`,
	}, { // Test 6: Recall with no person.
		Call: func() (string, error) { return h.Recall("", "billing", 5) }, WantErr: "needs a person",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, err := test.Call()
			if test.WantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.WantErr) {
					t.Errorf("error = %v, want one containing %q", err, test.WantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if !strings.Contains(got, test.WantContains) {
				t.Errorf("result %s does not contain %s", got, test.WantContains)
			}
		})
	}
}

// TestHandleStatus verifies age comes from the host clock and the counts and
// seal flag reflect what was opened.
func TestHandleStatus(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, testKeyCipher(t)), testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	built := h.eng.Index().BuiltAt()
	raw, err := h.Status(built.Unix() + 3600)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got statusView
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if got.AgeSeconds < 3599 || got.AgeSeconds > 3600 {
		t.Errorf("age_seconds = %d, want 3600 from the supplied clock", got.AgeSeconds)
	}
	if !got.Sealed {
		t.Error("status reports a keyed index as not sealed")
	}
	if got.People == 0 || got.Conversations == 0 || got.BuiltAt == "" {
		t.Errorf("status missing counts or build time: %s", raw)
	}
}

// TestUnopenedHandle verifies a handle not made by Open fails every call with
// an error instead of a crash, since gomobile lets Swift construct one.
func TestUnopenedHandle(t *testing.T) {
	t.Parallel()
	var nilHandle *Handle
	for _, h := range []*Handle{{}, nilHandle} {
		calls := []func() (string, error){
			func() (string, error) { return h.Ask("billing", 5) },
			func() (string, error) { return h.Search("billing", 5) },
			func() (string, error) { return h.Person("angela@corp.com") },
			func() (string, error) { return h.Recall("angela@corp.com", "billing", 5) },
			func() (string, error) { return h.Status(0) },
		}
		for i, call := range calls {
			if _, err := call(); !errors.Is(err, errNotOpen) {
				t.Errorf("call %d on an unopened handle = %v, want %v", i, err, errNotOpen)
			}
		}
	}
}

// TestClampLimit verifies limits map onto the accepted range.
func TestClampLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   int
		Want int
	}{
		{In: -1, Want: defaultLimit},   // Test 0: Negative.
		{In: 0, Want: defaultLimit},    // Test 1: Zero.
		{In: 7, Want: 7},               // Test 2: In range.
		{In: maxLimit, Want: maxLimit}, // Test 3: At the cap.
		{In: 1 << 20, Want: maxLimit},  // Test 4: Beyond the cap.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, clampLimit(test.In)); diff != "" {
				t.Errorf("clampLimit mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
