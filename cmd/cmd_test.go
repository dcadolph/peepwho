package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// TestIndexThenAsk runs the index and ask commands end to end over a temp CSV.
func TestIndexThenAsk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Jane Roe,jane@x.com,Staff Engineer,Billing,retries;idempotency\n" +
		"Bob Lee,bob@x.com,SRE,Infra,kafka\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	out, _, err := runCmd(t, "ask", "--data-dir", dir, "--pretty", "who owns billing retries")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	var ans resolve.JSONAnswer
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode answer: %v\n%s", err, out)
	}
	if len(ans.People) == 0 || ans.People[0].Email != "jane@x.com" {
		t.Fatalf("top match = %+v, want jane@x.com", ans.People)
	}
}

// TestAskNoIndex verifies ask reports a clear error when no index exists.
func TestAskNoIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := runCmd(t, "ask", "--data-dir", dir, "anything"); err == nil {
		t.Fatal("ask without an index: want error, got nil")
	}
}

// runCmd executes the root command with args, capturing stdout and stderr.
func runCmd(t *testing.T, args ...string) (stdout, stderr []byte, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.Bytes(), errBuf.Bytes(), err
}
