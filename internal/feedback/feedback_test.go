package feedback

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/vault"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "feedback.json")

	s, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	when := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Query: "billing retries", Person: "alice@corp.com", Vote: Helpful, Time: when},
		{Query: "billing retries", Channel: "payments", Vote: NotHelpful, Time: when},
	}
	for _, e := range entries {
		if err := s.Add(e); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	reloaded, err := Load(path, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if diff := cmp.Diff(entries, reloaded.All()); diff != "" {
		t.Errorf("entries mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "feedback.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(path, nil); err == nil {
		t.Error("want a parse error for invalid JSON")
	}
}

func TestEntryValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         Entry
		WantResult bool
	}{{ // Test 0: A person vote is valid.
		In: Entry{Query: "q", Person: "p", Vote: Helpful}, WantResult: true,
	}, { // Test 1: A channel vote is valid.
		In: Entry{Query: "q", Channel: "c", Vote: NotHelpful}, WantResult: true,
	}, { // Test 2: A missing query is invalid.
		In: Entry{Person: "p", Vote: Helpful},
	}, { // Test 3: Both targets set is invalid.
		In: Entry{Query: "q", Person: "p", Channel: "c", Vote: Helpful},
	}, { // Test 4: No target is invalid.
		In: Entry{Query: "q", Vote: Helpful},
	}, { // Test 5: An unknown vote value is invalid.
		In: Entry{Query: "q", Person: "p", Vote: 2},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := test.In.Valid(); got != test.WantResult {
				t.Errorf("Valid() = %v, want %v", got, test.WantResult)
			}
		})
	}
}

// TestAddMergesConcurrentWrite verifies a store re-reads the file before
// appending, so a vote another process wrote is not clobbered.
func TestAddMergesConcurrentWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "feedback.json")
	when := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	// Two stores stand in for two processes sharing one file.
	procA, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	procB, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load B: %v", err)
	}

	a := Entry{Query: "q", Person: "alice@corp.com", Vote: Helpful, Time: when}
	if err := procA.Add(a); err != nil {
		t.Fatalf("A add: %v", err)
	}
	// procB still holds an empty in-memory view; its Add must not drop A's vote.
	b := Entry{Query: "q", Person: "bob@corp.com", Vote: NotHelpful, Time: when}
	if err := procB.Add(b); err != nil {
		t.Fatalf("B add: %v", err)
	}

	reloaded, err := Load(path, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if diff := cmp.Diff([]Entry{a, b}, reloaded.All()); diff != "" {
		t.Errorf("persisted entries mismatch (-want +got):\n%s", diff)
	}
}

func TestAddRejectsBadEntry(t *testing.T) {
	t.Parallel()
	s, err := Load(filepath.Join(t.TempDir(), "feedback.json"), nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Add(Entry{Query: "q", Vote: Helpful}); !errors.Is(err, ErrBadEntry) {
		t.Errorf("add error = %v, want ErrBadEntry", err)
	}
}

func TestListAndClear(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "feedback.json")
	s, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	when := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Query: "billing retries", Person: "alice@corp.com", Vote: Helpful,
			Comment: "she fixed it last week", Time: when},
		{Query: "billing retries", Person: "bob@corp.com", Vote: NotHelpful, Time: when},
		{Query: "kafka", Channel: "data-platform", Vote: Helpful, Time: when},
	}
	for _, e := range entries {
		if err := s.Add(e); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	if got := s.List(Filter{Query: "Billing Retries"}); len(got) != 2 {
		t.Errorf("list by query = %d entries, want 2", len(got))
	}
	if got := s.List(Filter{Person: "alice@corp.com"}); len(got) != 1 || got[0].Comment == "" {
		t.Errorf("list by person = %+v, want alice's commented vote", got)
	}

	removed, err := s.Clear(Filter{Person: "alice@corp.com"})
	if err != nil || removed != 1 {
		t.Fatalf("clear = %d, %v; want 1 removed", removed, err)
	}
	reloaded, err := Load(path, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(Filter{}); len(got) != 2 {
		t.Errorf("after clear = %d entries, want 2 persisted", len(got))
	}
	removed, err = s.Clear(Filter{})
	if err != nil || removed != 2 {
		t.Errorf("clear all = %d, %v; want 2 removed", removed, err)
	}
}

// TestFeedbackEncryptedAtRest verifies that when a codec is given, votes are
// sealed on disk and never leak the question text, and still read back.
func TestFeedbackEncryptedAtRest(t *testing.T) {
	t.Parallel()
	codec, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "feedback.json")
	s, err := Load(path, codec)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(Entry{Query: "who owns billing", Person: "angela@corp.com", Vote: Helpful}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Error("feedback file is not encrypted at rest")
	}
	if bytes.Contains(raw, []byte("who owns billing")) {
		t.Error("feedback file leaks the question text in plaintext")
	}
	reloaded, err := Load(path, codec)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.All(); len(got) != 1 || got[0].Query != "who owns billing" {
		t.Errorf("reloaded = %+v, want the one vote", got)
	}
}

// TestKeylessReadOfSealedFile checks a sealed feedback file read with no codec
// reports vault.ErrEncrypted, so the person who unset the key is pointed at
// the key rather than at a JSON parse failure over ciphertext.
func TestKeylessReadOfSealedFile(t *testing.T) {
	t.Parallel()
	codec, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "feedback.json")
	s, err := Load(path, codec)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.Add(Entry{Query: "q", Person: "p@x.com", Vote: Helpful}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := Load(path, nil); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("keyless load error = %v, want vault.ErrEncrypted", err)
	}
}

// TestStoreRewrite seals an existing plain file and opens it back up, which is
// what `vault encrypt` and `vault decrypt` run for feedback. Without it a
// decrypt left the votes sealed under a key the user then unset, and they were
// unrecoverable.
func TestStoreRewrite(t *testing.T) {
	t.Parallel()
	codec, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "feedback.json")
	plain, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := plain.Add(Entry{Query: "q", Person: "p@x.com", Vote: Helpful}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sealing, err := Load(path, codec)
	if err != nil {
		t.Fatalf("Load with codec over plain file: %v", err)
	}
	if err := sealing.Rewrite(codec); err != nil {
		t.Fatalf("Rewrite to sealed: %v", err)
	}
	if raw, _ := os.ReadFile(path); !vault.IsEncrypted(raw) {
		t.Fatal("file still plain after Rewrite with a codec")
	}

	opening, err := Load(path, codec)
	if err != nil {
		t.Fatalf("Load sealed: %v", err)
	}
	if err := opening.Rewrite(nil); err != nil {
		t.Fatalf("Rewrite to plain: %v", err)
	}
	back, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load plain again: %v", err)
	}
	if got := back.All(); len(got) != 1 || got[0].Query != "q" {
		t.Errorf("votes after the round trip = %+v, want the one vote", got)
	}
}
