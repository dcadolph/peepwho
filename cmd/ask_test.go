package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestAskSemanticWithoutEmbeddingsExplains verifies asking in semantic mode on
// an index with no vectors fails with an actionable message rather than
// silently returning nothing.
func TestAskSemanticWithoutEmbeddingsExplains(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	_, stderr, err := runCmd(t, "ask", "--data-dir", dir, "--mode", "semantic", "who owns retries")
	if err == nil {
		t.Fatal("semantic ask on an index with no embeddings did not fail")
		return
	}
	if !strings.Contains(err.Error(), "--embed") && !strings.Contains(string(stderr), "--embed") {
		t.Errorf("message does not point at --embed: %v / %s", err, stderr)
	}
}

// TestAskEmptyResultWarns verifies a query that matches nothing writes an
// explanation to stderr, so an empty answer does not read as a silent success.
func TestAskEmptyResultWarns(t *testing.T) {
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	_, stderr, err := runCmd(t, "ask", "--data-dir", dir, "wildebeest zzzznothing")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(string(stderr), "No match") {
		t.Errorf("no explanation for an empty answer:\n%s", stderr)
	}
}

// TestWarnEmptyAskSuggests checks that an ask with no expertise match falls back
// to the closest entities by name, and that a non-empty answer warns nothing.
func TestWarnEmptyAskSuggests(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Kevin Novak", Email: "kevin@corp.com", Team: "Payments", Source: "t"},
	})
	ix.Canonicalize()

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&buf)

	warnEmptyAsk(cmd, ix, "kevin", resolve.Answer{})
	if out := buf.String(); !strings.Contains(out, "Kevin Novak") || !strings.Contains(out, "Closest by name") {
		t.Errorf("expected a name suggestion, got: %q", out)
	}

	buf.Reset()
	warnEmptyAsk(cmd, ix, "kevin", resolve.Answer{People: []model.Match{{}}})
	if buf.Len() != 0 {
		t.Errorf("warned on a non-empty answer: %q", buf.String())
	}

	// A multi-word question still suggests, via per-word search of the phrase.
	buf.Reset()
	warnEmptyAsk(cmd, ix, "who on the payments team can help", resolve.Answer{})
	if out := buf.String(); !strings.Contains(out, "Kevin Novak") {
		t.Errorf("multi-word question gave no suggestion: %q", out)
	}
}
