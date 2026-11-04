package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/resolve"
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

// TestIndexSlackExportRequiresFile verifies the source refuses to run without
// the export path.
func TestIndexSlackExportRequiresFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "slack-export"); err == nil {
		t.Fatal("index without --file: want error, got nil")
	}
}
