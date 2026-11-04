package feedback

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBundleCarriesNoQueries is the promise the bundle makes, tested as a
// property: a question asked of peepwho is itself a fact about the company, so
// no word of any query may survive into the report — only its silhouette.
func TestBundleCarriesNoQueries(t *testing.T) {
	t.Parallel()
	entries := []Entry{
		{Query: "who knows about project neptune acquisition", Person: "a@corp.com",
			Vote: Helpful, Comment: "spot on", Time: time.Unix(1700000000, 0)},
		{Query: "layoffs planning spreadsheet owner", Person: "b@corp.com",
			Vote: NotHelpful, Time: time.Unix(1700000100, 0)},
		{Query: "sev1 postmortem gamma-reactor", Channel: "incidents",
			Vote: Helpful, Time: time.Unix(1700000200, 0)},
	}
	b := NewBundle("v9.9.9", entries)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := strings.ToLower(string(raw))
	for _, secret := range []string{
		"neptune", "acquisition", "layoffs", "spreadsheet", "gamma-reactor",
		"a@corp.com", "b@corp.com", "incidents",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("the bundle leaked %q", secret)
		}
	}
	// What it keeps: the arithmetic, the shapes, and the typed comment.
	if b.Votes.Total != 3 || b.Votes.Helpful != 2 || b.Votes.NotHelpful != 1 {
		t.Errorf("votes = %+v", b.Votes)
	}
	if len(b.QueryShapes) != 3 || b.QueryShapes[0].Words != 6 {
		t.Errorf("shapes = %+v, want first with 6 words", b.QueryShapes)
	}
	if len(b.Comments) != 1 || b.Comments[0].Comment != "spot on" {
		t.Errorf("comments = %+v", b.Comments)
	}
	if !strings.Contains(string(raw), "never appear in this file") {
		t.Error("the bundle does not state its own redaction")
	}
}
