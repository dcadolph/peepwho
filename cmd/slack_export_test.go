package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// exportZip is the real-format Slack export fixture shared with the connector
// tests.
const exportZip = "../internal/connector/testdata/slack_export.zip"

// TestIndexSlackExportThenAsk indexes a Slack export zip end to end, with no
// token and no network, then asks a question the export's messages answer.
func TestIndexSlackExportThenAsk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, stderr, err := runCmd(t, "index", "--data-dir", dir,
		"--source", "slack-export", "--file", exportZip, "--since-days", "36500")
	if err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	logs := string(stderr)
	for _, want := range []string{
		"indexed #general", "indexed #engineering",
		"skipping #ghost-channel", "#private-ops",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("index log lacks %q; log:\n%s", want, logs)
		}
	}

	out, _, err := runCmd(t, "ask", "--data-dir", dir, "who knows about the kafka upgrade")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var ans resolve.JSONAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode answer: %v\n%s", err, out)
	}
	if len(ans.People) == 0 {
		t.Fatal("no people for a question the export's messages answer")
	}
	if got := ans.People[0].Email; got != "alice@corp.com" {
		t.Errorf("top person = %q, want alice@corp.com who announced the upgrade", got)
	}
}

// departedDataDir saves an index where Nora knows ledgers and is active, and
// Dee knows ledgers and webhooks and is deactivated, into a data directory.
func departedDataDir(t *testing.T) string {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Name: "Nora Here",
			Email: "nora@corp.com", Topics: []string{"ledgers"}, Account: model.AccountActive},
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U2", Name: "Dee Parted",
			Email: "dee@corp.com", Topics: []string{"ledgers", "ledgers", "webhooks"},
			Account: model.AccountDeactivated},
	})
	ix.Canonicalize()
	dir := t.TempDir()
	if err := engine.NewStore(engine.Dir(dir), nil).SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	return dir
}

// TestAskNamesDeactivatedLast checks that a deactivated person is named in
// departed, marked, never among the people, printed tagged at the bottom of
// the people list, and that an answer holding only them is not a miss.
func TestAskNamesDeactivatedLast(t *testing.T) {
	t.Parallel()
	dir := departedDataDir(t)
	tests := []struct {
		Query      string
		WantPeople []string
		WantHuman  string
	}{{ // Test 0: Dee outscores Nora on ledgers and still comes after her.
		Query: "ledgers", WantPeople: []string{"nora@corp.com"}, WantHuman: "Dee Parted  deactivated",
	}, { // Test 1: Only Dee knows webhooks.
		Query: "webhooks", WantHuman: "Dee Parted  deactivated",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			out, stderr, err := runCmd(t, "ask", "--data-dir", dir, "--json", test.Query)
			if err != nil {
				t.Fatalf("ask: %v", err)
			}
			var ans resolve.JSONAnswer
			if err := json.Unmarshal(out, &ans); err != nil {
				t.Fatalf("decode answer: %v\n%s", err, out)
			}
			var people []string
			for _, p := range ans.People {
				people = append(people, p.Email)
			}
			if diff := cmp.Diff(test.WantPeople, people); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
			if len(ans.Departed) != 1 || ans.Departed[0].Email != "dee@corp.com" || !ans.Departed[0].Deactivated {
				t.Errorf("departed = %+v, want dee@corp.com marked", ans.Departed)
			}
			if strings.Contains(string(stderr), "No match") {
				t.Errorf("an answer naming a departed person warned as a miss:\n%s", stderr)
			}
			human, _, err := runCmd(t, "ask", "--data-dir", dir, "--human", test.Query)
			if err != nil {
				t.Fatalf("ask --human: %v", err)
			}
			if !strings.Contains(string(human), test.WantHuman) || strings.Contains(string(human), "Nothing matched") {
				t.Errorf("human answer does not show Dee tagged:\n%s", human)
			}
		})
	}
}

// TestIndexSlackExportRequiresFile verifies the source refuses to run without
// the export path.
func TestIndexSlackExportRequiresFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "slack-export"); err == nil {
		t.Fatal("index without --file: want error, got nil")
	}
}
