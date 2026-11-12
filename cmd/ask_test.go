package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// TestSuggestionLabelMarksTheDeactivated checks a line of the closest-by-name
// list says when the person has left, so a departed colleague found by name is
// never offered as somebody to message without a word.
func TestSuggestionLabelMarksTheDeactivated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         resolve.SearchResult
		WantResult string
	}{{ // Test 0: A departed person with a title.
		In: resolve.SearchResult{
			Kind: "person", Name: "Dinesh White", Title: "Engineer", Deactivated: true,
		},
		WantResult: "Dinesh White (Engineer, deactivated)",
	}, { // Test 1: A departed person with no title or team.
		In:         resolve.SearchResult{Kind: "person", Name: "Dinesh White", Deactivated: true},
		WantResult: "Dinesh White (deactivated)",
	}, { // Test 2: An active person with only a team.
		In:         resolve.SearchResult{Kind: "person", Name: "Kevin Novak", Team: "Payments"},
		WantResult: "Kevin Novak (Payments)",
	}, { // Test 3: An active person with nothing else to say.
		In:         resolve.SearchResult{Kind: "person", Name: "Kevin Novak"},
		WantResult: "Kevin Novak",
	}, { // Test 4: A channel with a topic.
		In:         resolve.SearchResult{Kind: "channel", Name: "payments", Team: "Payments"},
		WantResult: "#payments (Payments)",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := suggestionLabel(test.In); got != test.WantResult {
				t.Errorf("label = %q, want %q", got, test.WantResult)
			}
		})
	}
}

// TestWarnEmptyAskMarksTheDeactivated checks the closest-by-name fallback puts
// an active namesake first and marks the departed one.
func TestWarnEmptyAskMarksTheDeactivated(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Dinesh Adams", Email: "adams@corp.com",
			Title: "Engineer", Account: model.AccountDeactivated, Source: "slack"},
		{Kind: connector.KindPerson, Name: "Dinesh Black", Email: "black@corp.com",
			Title: "Engineer", Account: model.AccountActive, Source: "slack"},
	})
	ix.Canonicalize()

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&buf)
	warnEmptyAsk(cmd, ix, "dinesh", resolve.Answer{})
	out := buf.String()
	active := strings.Index(out, "  Dinesh Black (Engineer)\n")
	departed := strings.Index(out, "  Dinesh Adams (Engineer, deactivated)\n")
	if active < 0 || departed < 0 || active > departed {
		t.Errorf("want the active namesake first and the departed one marked, got: %q", out)
	}
}

// TestAskTeamsOnlyCodeowners verifies a CODEOWNERS file that names only team
// handles still answers: the people list is empty, the teams of record carry
// the answer, the command succeeds, and nothing reads as a miss.
func TestAskTeamsOnlyCodeowners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Owners   string
		Query    string
		WantTeam string
	}{{ // Test 0: A path topic owned by the team.
		Owners: "* @prometheus/default-maintainers\n/tsdb/ @prometheus/default-maintainers\n",
		Query:  "tsdb", WantTeam: "codeowners:prometheus/default-maintainers",
	}, { // Test 1: A catch-all file found by the words of its handle.
		Owners: "* @prometheus/default-maintainers\n",
		Query:  "maintainers", WantTeam: "codeowners:prometheus/default-maintainers",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			owners := filepath.Join(dir, "CODEOWNERS")
			if err := os.WriteFile(owners, []byte(test.Owners), 0o600); err != nil {
				t.Fatalf("write CODEOWNERS: %v", err)
			}
			if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "codeowners", "--file", owners); err != nil {
				t.Fatalf("index: %v", err)
			}
			stdout, stderr, err := runCmd(t, "ask", "--data-dir", dir, "--json", test.Query)
			if err != nil {
				t.Fatalf("ask: %v", err)
			}
			var got resolve.JSONAnswer
			if err := json.Unmarshal(stdout, &got); err != nil {
				t.Fatalf("decode %s: %v", stdout, err)
			}
			if len(got.People) != 0 {
				t.Errorf("people = %+v, want none", got.People)
			}
			if len(got.Teams) == 0 || got.Teams[0].ID != test.WantTeam {
				t.Errorf("teams = %+v, want %s first", got.Teams, test.WantTeam)
			}
			if strings.Contains(string(stderr), "No match") {
				t.Errorf("a teams-only answer warned as a miss:\n%s", stderr)
			}
			human, _, err := runCmd(t, "ask", "--data-dir", dir, "--human", test.Query)
			if err != nil {
				t.Fatalf("ask --human: %v", err)
			}
			if strings.Contains(string(human), "Nothing matched") {
				t.Errorf("a teams-only answer printed Nothing matched:\n%s", human)
			}
			if !strings.Contains(string(human), "TEAMS (OWNERS OF RECORD)") {
				t.Errorf("no teams section in:\n%s", human)
			}
		})
	}
}
