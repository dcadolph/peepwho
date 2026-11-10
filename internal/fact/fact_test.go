package fact

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/vault"
)

func TestFactValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Fact Fact
		Want error
	}{{ // Test 0: A known relation with subject and object is valid.
		Fact: Fact{Subject: "team:pay", Relation: "not_owned_by", Object: "svc:checkout"},
		Want: nil,
	}, { // Test 1: An unknown relation is rejected.
		Fact: Fact{Subject: "team:pay", Relation: "owns", Object: "svc:checkout"},
		Want: ErrBadFact,
	}, { // Test 2: An empty subject is rejected.
		Fact: Fact{Subject: "", Relation: "owned_by", Object: "svc:checkout"},
		Want: ErrBadFact,
	}, { // Test 3: An empty object is rejected.
		Fact: Fact{Subject: "team:pay", Relation: "owned_by", Object: ""},
		Want: ErrBadFact,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if err := test.Fact.Valid(); !errors.Is(err, test.Want) {
				t.Errorf("Valid() = %v, want %v", err, test.Want)
			}
		})
	}
}

func TestStoreAddListForget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "facts.json")
	store, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	adds := []Fact{
		{Subject: "team:pay", Relation: "owned_by", Object: "svc:checkout", Source: "catalog"},
		{Subject: "team:pay", Relation: "not_owned_by", Object: "svc:refunds", Source: "curated"},
		{Subject: "team:ship", Relation: "owned_by", Object: "svc:labels", Source: "catalog"},
	}
	for _, f := range adds {
		if err := store.Add(f); err != nil {
			t.Fatalf("add %v: %v", f, err)
		}
	}
	if got := len(store.List(Filter{})); got != 3 {
		t.Fatalf("list all = %d, want 3", got)
	}
	if got := len(store.List(Filter{Subject: "team:pay"})); got != 2 {
		t.Errorf("list team:pay = %d, want 2", got)
	}
	if got := len(store.List(Filter{Source: "catalog"})); got != 2 {
		t.Errorf("list source catalog = %d, want 2", got)
	}
	// An empty filter forgets nothing.
	if n, _ := store.Forget(Filter{}); n != 0 {
		t.Errorf("forget empty = %d, want 0", n)
	}
	// Forget a whole source.
	if n, err := store.Forget(Filter{Source: "catalog"}); err != nil || n != 2 {
		t.Errorf("forget source catalog = %d, %v, want 2, nil", n, err)
	}
	// The reloaded store reflects the forget.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(reloaded.List(Filter{})); got != 1 {
		t.Errorf("after forget, reloaded list = %d, want 1", got)
	}
}

func TestStoreImportReplace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "facts.json")
	store, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	first := `[{"subject":"team:pay","relation":"owned_by","object":"svc:a"},
	           {"subject":"team:pay","relation":"owned_by","object":"svc:b"}]`
	if n, err := store.Import(strings.NewReader(first), "catalog"); err != nil || n != 2 {
		t.Fatalf("import first = %d, %v, want 2, nil", n, err)
	}
	// Re-importing the source replaces its facts rather than appending.
	second := `[{"subject":"team:pay","relation":"owned_by","object":"svc:c"}]`
	if n, err := store.Import(strings.NewReader(second), "catalog"); err != nil || n != 1 {
		t.Fatalf("import second = %d, %v, want 1, nil", n, err)
	}
	if got := len(store.List(Filter{Source: "catalog"})); got != 1 {
		t.Errorf("catalog facts after replace = %d, want 1", got)
	}
	// An import with no replace source appends and defaults nothing away.
	third := `[{"subject":"team:ship","relation":"runs_on","object":"cloud:aws","source":"curated"}]`
	if n, err := store.Import(strings.NewReader(third), ""); err != nil || n != 1 {
		t.Fatalf("import third = %d, %v, want 1, nil", n, err)
	}
	if got := len(store.List(Filter{})); got != 2 {
		t.Errorf("total after appends = %d, want 2", got)
	}
}

// TestStoreEncryptedAtRest checks a store loaded with a codec writes sealed
// bytes, reads them back, and that a read without the key reports
// vault.ErrEncrypted rather than a parse error. A fact's detail states in
// plain words what the index encryption exists to keep off a stolen disk.
func TestStoreEncryptedAtRest(t *testing.T) {
	t.Parallel()
	codec, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "facts.json")
	s, err := Load(path, WithCodec(codec))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const detail = "Jane holds the prod keys"
	if err := s.Add(Fact{
		Subject: "team:pay", Relation: "escalates_to", Object: "person:jane@corp.com", Detail: detail,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Error("facts file is not encrypted at rest")
	}
	if bytes.Contains(raw, []byte(detail)) {
		t.Error("facts file leaks the detail in plaintext")
	}
	reloaded, err := Load(path, WithCodec(codec))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.List(Filter{}); len(got) != 1 || got[0].Detail != detail {
		t.Errorf("reloaded = %+v, want the one fact", got)
	}
	if _, err := Load(path); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("keyless load error = %v, want vault.ErrEncrypted so the reader is pointed at the key", err)
	}
}

// TestStoreRewrite seals an existing plain file and opens it back up, which is
// what `vault encrypt` and `vault decrypt` run for facts.
func TestStoreRewrite(t *testing.T) {
	t.Parallel()
	codec, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	path := filepath.Join(t.TempDir(), "facts.json")
	plain, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := plain.Add(Fact{Subject: "a", Relation: "owned_by", Object: "b"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sealing, err := Load(path, WithCodec(codec))
	if err != nil {
		t.Fatalf("Load with codec over plain file: %v", err)
	}
	if err := sealing.Rewrite(codec); err != nil {
		t.Fatalf("Rewrite to sealed: %v", err)
	}
	if raw, _ := os.ReadFile(path); !vault.IsEncrypted(raw) {
		t.Fatal("file still plain after Rewrite with a codec")
	}

	opening, err := Load(path, WithCodec(codec))
	if err != nil {
		t.Fatalf("Load sealed: %v", err)
	}
	if err := opening.Rewrite(nil); err != nil {
		t.Fatalf("Rewrite to plain: %v", err)
	}
	if raw, _ := os.ReadFile(path); vault.IsEncrypted(raw) {
		t.Fatal("file still sealed after Rewrite to plain")
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load plain again: %v", err)
	}
	if got := back.List(Filter{}); len(got) != 1 {
		t.Errorf("facts after the round trip = %+v, want the one fact", got)
	}
}

// TestForgetMatching removes facts by predicate, which is what a person purge
// uses: no field filter can express "names any of these identities".
func TestForgetMatching(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "facts.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	add := func(subject, object string) {
		t.Helper()
		if err := s.Add(Fact{Subject: subject, Relation: "escalates_to", Object: object}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	add("team:pay", "person:jane@corp.com")
	add("person:jane@corp.com", "person:oncall@corp.com")
	add("team:pay", "person:bob@corp.com")

	n, err := s.ForgetMatching(func(f Fact) bool {
		return strings.Contains(f.Subject, "jane@corp.com") || strings.Contains(f.Object, "jane@corp.com")
	})
	if err != nil {
		t.Fatalf("ForgetMatching: %v", err)
	}
	if n != 2 {
		t.Errorf("removed = %d, want 2", n)
	}
	left := s.List(Filter{})
	if len(left) != 1 || left[0].Object != "person:bob@corp.com" {
		t.Errorf("left = %+v, want only bob's fact", left)
	}
	if n, err := s.ForgetMatching(nil); err != nil || n != 0 {
		t.Errorf("nil match = %d, %v; want 0, nil", n, err)
	}
}
