package episode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/vault"
)

// TestSaveLoadRoundTrip verifies a stored episode, its postings, and its
// participant scoping survive a write and read, including the archive.
func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "episodes.json")
	s := newTestStore()
	ep := testEpisode("a", 10, "me@x.com", "billy@x.com")
	ep.Archive = []Note{{Author: "billy@x.com", At: fixedNow, Text: "bump the cert"}}
	ep.Body = "certificate renewal expired"
	s.Add(ep)
	s.SetVector("a", []float32{0.5, 0.25})
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Len() != 1 {
		t.Fatalf("Len = %d, want 1", got.Len())
	}
	hits := got.Search(Query{Text: "certificate renewal", Person: "me@x.com"})
	if len(hits) != 1 {
		t.Fatalf("Search after load = %+v, want one hit", hits)
	}
	if !hits[0].Episode.Archived() {
		t.Error("archive did not survive the round trip")
	}
	if v, ok := got.Vector("a"); !ok || len(v) != 2 {
		t.Errorf("Vector = (%v, %v), want two floats", v, ok)
	}
}

// TestSaveLoadEncrypted verifies the sidecar honors the same at-rest codec as
// the index, and that reading it without the key fails cleanly.
func TestSaveLoadEncrypted(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "episodes.json")
	codec := vault.NewPassphraseCipher([]byte("a long passphrase"))
	s := newTestStore()
	s.Add(withBody(testEpisode("a", 10, "me@x.com"), "certificate renewal"))
	if err := s.Save(path, WithCodec(codec)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Fatal("file is not encrypted")
	}
	if _, err := Load(path); !errors.Is(err, vault.ErrEncrypted) {
		t.Errorf("Load without a key = %v, want ErrEncrypted", err)
	}
	got, err := Load(path, WithCodec(codec))
	if err != nil || got.Len() != 1 {
		t.Errorf("Load with the key = (%v, %v), want one episode", got, err)
	}
}

// TestLoadOrNew verifies a missing file yields an empty store while a real
// failure is reported, so an unreadable archive is never mistaken for one that
// holds nothing.
func TestLoadOrNew(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := LoadOrNew(filepath.Join(dir, "absent.json"))
	if err != nil || s == nil || s.Len() != 0 {
		t.Fatalf("LoadOrNew on a missing file = (%v, %v), want an empty store", s, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadOrNew(bad); err == nil {
		t.Error("LoadOrNew on a corrupt file = nil error, want a failure")
	}
}

// TestInternedParticipantsRoundTrip verifies participants and archived authors
// survive interning to a shared id table and back, and that a person shared
// across episodes is written to the table once rather than per appearance.
func TestInternedParticipantsRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "episodes.json")
	s := newTestStore()
	a := testEpisode("a", 10, "me@x.com", "billy@x.com")
	a.Archive = []Note{{Author: "billy@x.com", At: fixedNow, Text: "bump the cert"}}
	s.Add(a)
	s.Add(testEpisode("b", 9, "me@x.com", "billy@x.com", "sam@x.com"))
	s.Add(testEpisode("c", 8, "billy@x.com", "sam@x.com"))
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Three distinct people across the episodes, so the interned table holds
	// three ids, not the seven participant slots and one archived author.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snap.IDs) != 3 {
		t.Errorf("interned id table = %d ids, want 3: %v", len(snap.IDs), snap.IDs)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ep, ok := got.Episode("a")
	if !ok {
		t.Fatal("episode a missing after load")
	}
	if diff := cmp.Diff([]model.ID{"me@x.com", "billy@x.com"}, ep.Participants); diff != "" {
		t.Errorf("participants mismatch (-want +got):\n%s", diff)
	}
	if len(ep.Archive) != 1 || ep.Archive[0].Author != "billy@x.com" {
		t.Errorf("archived author did not survive interning: %+v", ep.Archive)
	}
	if !got.HasPerson("sam@x.com") {
		t.Error("sam is no longer indexed as a participant after load")
	}
}

// TestUnpackRejectsOutOfRangeIndex verifies a packed episode whose participant
// index points past the id table is reported rather than loaded as a wrong or
// empty person.
func TestUnpackRejectsOutOfRangeIndex(t *testing.T) {
	t.Parallel()
	_, err := unpackEpisode(packedEpisode{ID: "e", Participants: []uint32{2}}, []model.ID{"only@x.com"})
	if err == nil {
		t.Fatal("unpackEpisode with an out-of-range index = nil error, want failure")
		return
	}
}

// TestPurgeArchiveDropsSearchIndex verifies that pruning content also removes
// the episode's postings and vector, so purged words can no longer be found,
// while the pointer to the conversation survives.
func TestPurgeArchiveDropsSearchIndex(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	ep := testEpisode("a", 1, "me@x.com")
	ep.Archive = []Note{{Author: "me@x.com", At: fixedNow, Text: "rotate the certificate"}}
	s.Add(ep)

	if got := s.Search(Query{Text: "certificate", Person: "me@x.com"}); len(got) != 1 {
		t.Fatalf("before purge: search = %d, want 1", len(got))
	}
	if n := s.PurgeArchive(); n != 1 {
		t.Fatalf("PurgeArchive = %d, want 1", n)
	}
	if got := s.Search(Query{Text: "certificate", Person: "me@x.com"}); len(got) != 0 {
		t.Errorf("after purge: content search = %d, want 0 (purged content still searchable)", len(got))
	}
	if got := s.Search(Query{Person: "me@x.com"}); len(got) != 1 {
		t.Errorf("after purge: recency list = %d, want 1 (pointer lost)", len(got))
	}
}
