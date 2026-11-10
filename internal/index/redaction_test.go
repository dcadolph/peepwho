package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestSavedIndexHoldsNoReadableText verifies a saved index contains the stemmed
// search terms but not the readable message text they came from. This is the
// promise the privacy doc makes: without an archive, the graph and a search
// index live on disk and the conversations do not.
func TestSavedIndexHoldsNoReadableText(t *testing.T) {
	t.Parallel()
	const phrase = "the quarterly reconciliation ledger drifted by seventeen cents"
	ix := New()
	ix.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane",
			Text: phrase},
		{Source: "slack", Kind: connector.KindChannel, Name: "finance",
			Text: "channel where " + phrase},
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	on := string(raw)

	// No readable prose: not the phrase, nor any multi-word run from it. An
	// inverted search index legitimately contains individual stemmed words,
	// but the conversation itself, its word order and sentences, must be gone.
	if strings.Contains(on, phrase) {
		t.Error("the saved index contains the readable message phrase verbatim")
	}
	for _, run := range []string{
		"quarterly reconciliation", "reconciliation ledger", "ledger drifted",
		"drifted by seventeen", "channel where",
	} {
		if strings.Contains(on, run) {
			t.Errorf("the saved index contains the readable word run %q", run)
		}
	}

	// Reloaded, the person is still findable by the words they used, which proves
	// the stemmed search terms were retained even though the postings are packed
	// as a binary blob rather than readable text.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	hits := reloaded.Search("reconciliation ledger", 3)
	if len(hits) == 0 || hits[0].Person.Email != "jane@x.com" {
		t.Fatalf("reloaded index no longer finds the person: %+v", hits)
	}
}

// TestRedactedIndexStillMergesIdempotently verifies a source re-read after a
// save-and-reload replaces its own contribution rather than stacking, even
// though the reloaded records carry only stemmed terms and no readable text.
func TestRedactedIndexStillMergesIdempotently(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane",
			Text: "billing retries kept failing on the payment worker"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	first := New()
	first.Build(recs)
	if err := first.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want := first.Search("billing retries", 1)

	// Reload the redacted index, then re-read the same source and re-save.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reloaded.Add(recs)
	got := reloaded.Search("billing retries", 1)

	if len(got) != len(want) || len(got) == 0 {
		t.Fatalf("search after reload+merge = %d, want %d", len(got), len(want))
	}
	if got[0].Person.Email != want[0].Person.Email {
		t.Errorf("merge changed the answer: %q vs %q", got[0].Person.Email, want[0].Person.Email)
	}
	// The weight must match the single-index weight, not double it.
	if diff := got[0].Score - want[0].Score; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("re-merging a redacted source inflated the score: %.6f vs %.6f",
			got[0].Score, want[0].Score)
	}
}

// mockEmbedder returns a deterministic vector derived from the text length, and
// records every text it was asked to embed, so a test can prove which entities
// were re-embedded and which kept their vector.
type mockEmbedder struct {
	seen []string
}

// Embed records the text and returns a length-based vector.
func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.seen = append(m.seen, text)
	return []float32{float32(len(text)), 1}, nil
}

// TestReembedAfterMergeKeepsRichVectors verifies that re-embedding after a
// merge does not weaken the vectors of people whose message text is no longer
// in memory. Their message-built vector is kept rather than replaced by a
// thinner name-and-topic one, while a freshly merged person is embedded.
func TestReembedAfterMergeKeepsRichVectors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")

	// Index a person with message text and embed them, then save.
	first := New()
	first.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane",
			Text: "billing retries kept failing on the payment worker at 2am"},
	})
	if err := first.Embed(context.Background(), &mockEmbedder{}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := first.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload the redacted index (Jane now carries no message text), merge a new
	// person, and re-embed. The vectors are quantized on save, so the loaded one
	// is the baseline the re-embed must not disturb, not the pre-save float32.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// A merge rebuilds from every source, so the prior records come back from
	// the sidecar first, exactly as the index command does before it merges.
	if err := reloaded.LoadSources(path); err != nil {
		t.Fatalf("load sources: %v", err)
	}
	janeLoaded := append([]float32(nil), reloaded.personVecs["jane@x.com"]...)
	reloaded.Add([]connector.Record{
		{Source: "jira", Kind: connector.KindPerson, Email: "bob@x.com", Name: "Bob",
			Text: "kafka consumer lag on the ingest topic"},
	})
	em := &mockEmbedder{}
	if err := reloaded.Embed(context.Background(), em); err != nil {
		t.Fatalf("re-embed: %v", err)
	}

	// Jane's loaded vector is kept as-is; only Bob was embedded this pass.
	if got := reloaded.personVecs["jane@x.com"]; !equalVec(got, janeLoaded) {
		t.Errorf("Jane's vector changed on re-embed: %v vs %v", got, janeLoaded)
	}
	if len(em.seen) != 1 {
		t.Errorf("re-embed called the model %d times, want 1 (only the new person)", len(em.seen))
	}
}

// equalVec reports whether two float32 vectors match.
func equalVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSavedSidecarHoldsNoReadableText verifies the sources sidecar, like the
// main index, holds stemmed terms but not readable message text. On the free
// tier the sidecar is plain JSON on disk, so this is the load-bearing check that
// re-reading a source for a merge never persists conversations verbatim.
func TestSavedSidecarHoldsNoReadableText(t *testing.T) {
	t.Parallel()
	const phrase = "the quarterly reconciliation ledger drifted by seventeen cents"
	ix := New()
	ix.Build([]connector.Record{
		{Source: "slack", Kind: connector.KindPerson, Email: "jane@x.com", Name: "Jane", Text: phrase},
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(SourcesPath(path))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	on := string(raw)
	if strings.Contains(on, phrase) {
		t.Error("the sources sidecar contains the readable message phrase verbatim")
	}
	for _, run := range []string{
		"quarterly reconciliation", "reconciliation ledger", "ledger drifted", "drifted by seventeen",
	} {
		if strings.Contains(on, run) {
			t.Errorf("the sources sidecar contains the readable word run %q", run)
		}
	}
	// The source is still re-readable for a merge, proving the stemmed terms
	// survived even though the prose did not.
	reloaded := New()
	if err := reloaded.LoadSources(path); err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if reloaded.SourceSize("slack") == 0 {
		t.Error("the sidecar lost the source's records")
	}
}
