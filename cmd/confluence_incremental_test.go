package cmd

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/fakeapi"
	"github.com/dcadolph/peepwho/internal/state"
)

// TestConfluenceIncrementalReindex runs a full index then a --merge re-index
// against a fake Confluence and verifies the wiring: the first run records a
// watermark, the second sends a modified-since query and folds rather than
// replaces, and both authors survive the partial re-read.
func TestConfluenceIncrementalReindex(t *testing.T) {
	pages := []fakeapi.ConfluencePage{
		{Title: "KRaft quorum controller", SpaceKey: "KAFKA", SpaceName: "Kafka", Labels: []string{"kraft"},
			CreatedByEmail: "showuon@apache.invalid", CreatedAt: "2026-06-01T09:30:00.000Z",
			EditedByEmail: "showuon@apache.invalid", EditedAt: "2026-06-20T09:30:00.000Z"},
		{Title: "dashboard runbook", SpaceKey: "KAFKA", SpaceName: "Kafka", Labels: []string{"runbook"},
			CreatedByEmail: "brao@apache.invalid", CreatedAt: "2026-05-01T09:30:00.000Z",
			EditedByEmail: "brao@apache.invalid", EditedAt: "2026-06-18T09:30:00.000Z"},
	}
	fake := &fakeapi.Confluence{ServerMode: true, Pages: pages}
	srv := fake.Server()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("PEEPWHO_CONFLUENCE_URL", srv.URL)

	// First run: a full index writes the watermark.
	if _, stderr, err := runCmd(t, "index", "--source", "confluence", "--confluence-server",
		"--confluence-space", "KAFKA", "--data-dir", dir); err != nil {
		t.Fatalf("full index: %v\n%s", err, stderr)
	}
	st, err := state.Load(filepath.Join(dir, "index.state.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if wm, ok := st.Get("confluence", "space:KAFKA"); !ok || wm.Cursor.IsZero() {
		t.Fatalf("no confluence watermark after a full index: %+v", st.Watermarks)
	}

	// Second run: --merge is incremental, querying modified-since, oldest first.
	before := len(fake.Queries)
	if _, stderr, err := runCmd(t, "index", "--source", "confluence", "--merge", "--confluence-server",
		"--confluence-space", "KAFKA", "--data-dir", dir); err != nil {
		t.Fatalf("incremental index: %v\n%s", err, stderr)
	}
	var sawIncremental bool
	for _, raw := range fake.Queries[before:] {
		dec, _ := url.QueryUnescape(raw)
		if strings.Contains(dec, "lastmodified >=") && strings.Contains(dec, "order by lastmodified asc") {
			sawIncremental = true
		}
	}
	if !sawIncremental {
		t.Errorf("incremental run did not send a modified-since query, got: %v", fake.Queries[before:])
	}

	// Both authors survive the incremental re-index rather than being dropped.
	out, _, err := runCmd(t, "ask", "--data-dir", dir, "kafka", "--limit", "5")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	for _, want := range []string{"showuon", "brao"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("person %q missing after incremental re-index:\n%s", want, out)
		}
	}
}
