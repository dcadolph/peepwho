package cmd

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/fakeapi"
)

// TestConfluenceServerEndToEnd runs the whole index-then-ask flow against a
// fake self-hosted Confluence over a real socket, the same path validated live
// against Apache's public wiki. It keeps the Server deployment covered without
// depending on an external site.
func TestConfluenceServerEndToEnd(t *testing.T) {
	var pages []fakeapi.ConfluencePage
	// One clear KRaft expert plus filler authored by others.
	for i := range 120 {
		author, title, labels := "filler"+strconv.Itoa(i%3), "release notes", []string{"notes"}
		if i < 5 {
			author, title, labels = "showuon", "KRaft quorum controller design",
				[]string{"kraft", "controller", "quorum"}
		}
		pages = append(pages, fakeapi.ConfluencePage{
			Title: title, SpaceKey: "KAFKA", SpaceName: "Kafka", Labels: labels,
			CreatedByEmail: author + "@apache.invalid", CreatedAt: "2026-06-01T09:30:00.000Z",
			EditedByEmail: author + "@apache.invalid", EditedAt: "2026-06-20T09:30:00.000Z",
		})
	}
	srv := (&fakeapi.Confluence{ServerMode: true, Pages: pages}).Server()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("WHOTOASK_CONFLUENCE_URL", srv.URL)
	_, stderr, err := runCmd(t, "index", "--source", "confluence", "--confluence-server",
		"--confluence-space", "KAFKA", "--max-pages", "120", "--data-dir", dir)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(stderr), "fetched") {
		t.Errorf("no progress shown during a 120-page fetch:\n%s", stderr)
	}
	// The fake serves a body search in windows of fifty, as a real site does,
	// so a read that advances by a wider window than the server used comes
	// back short.
	if !strings.Contains(string(stderr), "confluence: 120 pages") {
		t.Errorf("index did not read all 120 pages:\n%s", stderr)
	}

	out, _, err := runCmd(t, "ask", "--data-dir", dir, "kraft quorum controller", "--limit", "3")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var ans struct {
		People []struct {
			ID string `json:"id"`
		} `json:"people"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(ans.People) == 0 || ans.People[0].ID != "confluence:showuon" {
		t.Fatalf("top answer = %+v, want confluence:showuon (username identity):\n%s", ans.People, out)
	}
}
