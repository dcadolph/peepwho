package index

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestLoadSourcesRefusesStaleSidecar covers the crash window between the two
// renames of a save, and two index runs racing each other: either leaves a
// snapshot beside a sidecar from a different run. Rebuilding from that pair
// silently reverts every source to the older records, which is the silent
// data loss this product treats as the worst failure, so the pair is refused
// with the mismatched sources named.
func TestLoadSourcesRefusesStaleSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	fresh := New()
	fresh.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane", Text: "billing"},
		{Source: "slack", Kind: connector.KindPerson, Email: "bob@x.com", Name: "Bob", Text: "kafka"},
	})
	if err := fresh.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// An older run's sidecar, holding a smaller slack read, lands beside the
	// new snapshot, exactly as a crash between the two renames leaves it.
	stale := New()
	stale.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane", Text: "billing"},
	})
	stalePath := filepath.Join(dir, "stale.json")
	if err := stale.Save(stalePath); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	overwriteSidecar(t, stalePath, path)

	ix, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = ix.LoadSources(path)
	if !errors.Is(err, ErrStaleSidecar) {
		t.Fatalf("LoadSources beside a stale sidecar = %v, want ErrStaleSidecar", err)
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("error %q does not name the mismatched source", err)
	}

	// A sidecar naming a source the snapshot never held is a different run's
	// file just the same.
	other := New()
	other.Build([]connector.Record{
		{Source: "jira", Kind: connector.KindPerson, Email: "kim@x.com", Name: "Kim", Text: "ledger"},
	})
	otherPath := filepath.Join(dir, "other.json")
	if err := other.Save(otherPath); err != nil {
		t.Fatalf("save other: %v", err)
	}
	overwriteSidecar(t, otherPath, path)
	err = ix.LoadSources(path)
	if !errors.Is(err, ErrStaleSidecar) {
		t.Fatalf("LoadSources beside a foreign sidecar = %v, want ErrStaleSidecar", err)
	}
	for _, name := range []string{"jira", "slack"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}

	// The matched pair still loads.
	if err := fresh.Save(path); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	re, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := re.LoadSources(path); err != nil {
		t.Fatalf("LoadSources on a matched pair: %v", err)
	}
}

// overwriteSidecar copies the sidecar written beside from over the one beside
// to, simulating the torn pair a crash or a racing run leaves behind.
func overwriteSidecar(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(SourcesPath(from))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if err := os.WriteFile(SourcesPath(to), raw, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// TestLoadRefusesForeignSchema covers a snapshot written by a build with a
// different layout. Decoding it best-effort drops unknown fields and zeroes
// missing ones, and a query then runs against a partial index where every
// reason degrades to a mention, so the version is checked and refused with
// both numbers named.
func TestLoadRefusesForeignSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	ix := New()
	ix.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane", Text: "billing"},
	})
	if err := ix.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A snapshot from a build with a later layout names both versions.
	setSchema(t, path, 99, false)
	_, err := Load(path)
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("Load of schema 99 = %v, want ErrSchema", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("error %q does not name the foreign schema", err)
	}

	// A snapshot from before versioning reads as schema zero and is refused.
	setSchema(t, path, 0, true)
	if _, err := Load(path); !errors.Is(err, ErrSchema) {
		t.Fatalf("Load of an unversioned snapshot = %v, want ErrSchema", err)
	}

	// The sidecar is held to the same layout.
	if err := ix.Save(path); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	setSchema(t, SourcesPath(path), 99, false)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := loaded.LoadSources(path); !errors.Is(err, ErrSchema) {
		t.Fatalf("LoadSources of a foreign-schema sidecar = %v, want ErrSchema", err)
	}
}

// setSchema rewrites the schema field of a plain-JSON snapshot file, or
// removes it when drop is set, standing in for a file another build wrote.
func setSchema(t *testing.T, path string, schema int, drop bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if drop {
		delete(doc, "schema")
	} else {
		doc["schema"] = schema
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}
