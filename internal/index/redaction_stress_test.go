package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/vault"
)

// TestRedactionEncryptionMergeCycles stresses the three newest interacting
// features together: readable text is dropped at save, the file is encrypted at
// rest, and a source is re-merged each round. Over many cycles the index must
// keep answering, never inflate weights, and never write readable message text,
// encrypted or not. These shipped hours apart, so this guards their seams.
func TestRedactionEncryptionMergeCycles(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	codec, err := vault.NewKeyCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	const secret = "the quarterly reconciliation ledger drifted seventeen cents"
	slack := []connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane",
			Text: secret + " billing retries idempotency"},
		{Source: "slack", Kind: connector.KindChannel, Name: "finance", Text: secret},
	}

	// Round 0: build and save encrypted.
	first := New()
	first.Build(slack)
	if err := first.Save(path, WithCodec(codec)); err != nil {
		t.Fatalf("save: %v", err)
	}
	want := first.Search("reconciliation ledger", 1)
	if len(want) == 0 {
		t.Fatal("baseline did not find the person")
	}

	for round := 1; round <= 15; round++ {
		// The encrypted file must not contain the readable prose, ever.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("round %d read: %v", round, err)
		}
		if !vault.IsEncrypted(raw) {
			t.Fatalf("round %d: file is not encrypted", round)
		}
		for _, run := range []string{secret, "quarterly reconciliation", "ledger drifted", "seventeen cents"} {
			if strings.Contains(string(raw), run) {
				t.Fatalf("round %d: encrypted-at-rest file leaked readable text %q", round, run)
			}
		}

		// Load (decrypt), re-merge the same source, save again.
		ix, err := Load(path, WithCodec(codec))
		if err != nil {
			t.Fatalf("round %d load: %v", round, err)
		}
		ix.Add(slack)
		if err := ix.Save(path, WithCodec(codec)); err != nil {
			t.Fatalf("round %d save: %v", round, err)
		}

		got := ix.Search("reconciliation ledger", 1)
		if len(got) != len(want) || (len(got) > 0 && got[0].Person.Email != want[0].Person.Email) {
			t.Fatalf("round %d: answer changed: %v", round, got)
		}
		if len(got) > 0 {
			if d := got[0].Score - want[0].Score; d > 1e-9 || d < -1e-9 {
				t.Fatalf("round %d: re-merging inflated the score: %.6f vs %.6f",
					round, got[0].Score, want[0].Score)
			}
		}
	}

	// After all the cycles, decrypting and reading still works and the readable
	// text never came back.
	final, err := Load(path, WithCodec(codec))
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if hits := final.Search("billing retries", 1); len(hits) == 0 {
		t.Error("final index no longer answers")
	}
}
