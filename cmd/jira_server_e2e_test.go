package cmd

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/fakeapi"
)

// TestJiraServerEndToEnd runs the whole index-then-ask flow against a fake
// self-hosted Jira over a real socket, the same path that was validated live
// against Apache's public Jira. It is the hermetic version of that run, so the
// Server deployment stays covered without depending on an external site.
func TestJiraServerEndToEnd(t *testing.T) {
	var issues []fakeapi.JiraIssue
	// A handful of resolved issues owned by two people, plus enough filler to
	// page, all with usernames and no emails as a Server site returns.
	owners := []string{"amaki", "brao"}
	for i := range 150 {
		owner := owners[i%2]
		summary := "generic maintenance chore"
		desc := "routine cleanup"
		if i < 4 {
			// The rebalance expert is amaki; plant it clearly.
			owner, summary, desc = "amaki", "consumer group rebalance storm",
				"raised the session timeout and fixed the coordinator"
		}
		// AssigneeEmail shapes the fixture, but ServerMode serves only the
		// username derived from it (no email), so the connector sees a
		// name-only user exactly as a public Server site returns.
		issues = append(issues, fakeapi.JiraIssue{
			Key: "KAFKA-" + strconv.Itoa(i), Summary: summary, Description: desc,
			AssigneeEmail: owner + "@apache.invalid", ProjectKey: "KAFKA", ProjectName: "Kafka",
			Updated:        "2026-06-20T09:30:00.000+0000",
			ResolutionDate: "2026-06-21T09:30:00.000+0000",
			StatusName:     "Resolved", StatusCategory: "done",
		})
	}

	srv := (&fakeapi.Jira{Issues: issues, ServerMode: true}).Server()
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	_, stderr, err := runCmd(t, "index", "--source", "jira", "--jira-server",
		"--jira-url", srv.URL, "--jira-project", "KAFKA", "--max-issues", "150",
		"--episodes", "--data-dir", dir)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	// Progress fired across pages, and people came back from a site with no
	// emails or account ids.
	if !strings.Contains(string(stderr), "fetched") {
		t.Errorf("no progress shown during a 150-issue fetch:\n%s", stderr)
	}

	out, _, err := runCmd(t, "ask", "--data-dir", dir, "consumer group rebalance", "--limit", "3")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var ans struct {
		People []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"people"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode answer: %v\n%s", err, out)
	}
	if len(ans.People) == 0 {
		t.Fatalf("no one answered for rebalancing:\n%s", out)
	}
	// The planted owner, keyed by username since the site exposed no email.
	if ans.People[0].ID != "jira:amaki" {
		t.Errorf("top answer = %q (%s), want jira:amaki", ans.People[0].ID, ans.People[0].Name)
	}
}
