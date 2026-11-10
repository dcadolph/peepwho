package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestSourcesSidecar verifies the source records live in a sidecar that a query
// never loads, that a query load carries the counts but not the records, that a
// merge brings the records back and keeps every prior source, and that saving a
// query-loaded index leaves the sidecar intact rather than erasing it.
func TestSourcesSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	first := New()
	first.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane", Text: "billing retries"},
	})
	if err := first.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(SourcesPath(path)); err != nil {
		t.Fatalf("sidecar was not written: %v", err)
	}

	// A query load carries the counts and the derived graph, but not the records.
	q, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if q.sources != nil {
		t.Error("a query load pulled in the source records; it should not")
	}
	if q.SourceSize("slack") != 1 {
		t.Errorf("source count = %d, want 1 read from the main index", q.SourceSize("slack"))
	}
	if _, ok := q.Graph.People["jane@x.com"]; !ok {
		t.Error("Jane is missing from the loaded graph")
	}

	// A merge loads the sidecar first, so the prior source survives the rebuild.
	m, err := Load(path)
	if err != nil {
		t.Fatalf("load for merge: %v", err)
	}
	if err := m.LoadSources(path); err != nil {
		t.Fatalf("load sources: %v", err)
	}
	m.Add([]connector.Record{
		{Source: "jira", Kind: connector.KindPerson, Email: "bob@x.com", Name: "Bob", Text: "kafka lag"},
	})
	if _, ok := m.Graph.People["jane@x.com"]; !ok {
		t.Error("the merge dropped Jane; the sidecar records were not folded in")
	}
	if _, ok := m.Graph.People["bob@x.com"]; !ok {
		t.Error("the merge did not add Bob")
	}

	// A missing sidecar is an error, not an empty set, so a merge cannot silently
	// shrink the index.
	if err := m.LoadSources(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("LoadSources on a missing sidecar did not error")
	}

	// Saving a query-loaded index, which holds no records, leaves the sidecar in
	// place rather than overwriting it with nothing.
	if err := q.Save(path); err != nil {
		t.Fatalf("re-save query index: %v", err)
	}
	back := New()
	if err := back.LoadSources(path); err != nil {
		t.Fatalf("sidecar was erased by saving a query-loaded index: %v", err)
	}
	if back.SourceSize("slack") != 1 {
		t.Errorf("sidecar source count = %d after a query-load save, want 1", back.SourceSize("slack"))
	}
}
