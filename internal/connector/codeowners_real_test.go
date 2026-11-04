package connector

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestParseCodeOwnersGrafana parses the CODEOWNERS file captured from
// grafana/grafana, a real 1,400-line file no fixture author would write, and
// pins what a correct read of it looks like: every one of its 51 owners and
// nothing else, in first-seen order, with sane identities and topics.
func TestParseCodeOwnersGrafana(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "codeowners_grafana"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	recs, err := parseCodeOwners(context.Background(), strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 51 {
		t.Fatalf("owners = %d, want the file's 51 distinct owners", len(recs))
	}

	names := make([]string, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Name)
		if !strings.HasPrefix(r.Name, "@") {
			t.Errorf("owner %q does not look like a handle; a comment or path leaked", r.Name)
		}
		if strings.ContainsAny(r.Name, "[]#*") {
			t.Errorf("owner %q carries pattern or comment syntax", r.Name)
		}
		want := "codeowners:" + strings.ToLower(strings.TrimPrefix(r.Name, "@"))
		if r.PersonID != want {
			t.Errorf("owner %q id = %q, want %q", r.Name, r.PersonID, want)
		}
		// One owner holds only /.vim, and a hidden path is tooling rather than
		// a subject, so they alone may carry no topics.
		if len(r.Topics)+len(r.WeakTopics) == 0 && r.Name != "@zoltanbedi" {
			t.Errorf("owner %q has no topics at all", r.Name)
		}
	}

	// First-seen order is the file's own: the changelog squad owns the first
	// entry, and individual humans appear where the file names them.
	if names[0] != "@grafana/grafana-backend-services-squad" {
		t.Errorf("first owner = %q, want the changelog squad", names[0])
	}
	for _, human := range []string{"@RichiH", "@torkelo"} {
		if !slices.Contains(names, human) {
			t.Errorf("owners lack %s, an individual the file names", human)
		}
	}

	// The frontend platform squad owns hundreds of frontend paths; if any
	// owner surfaces under "frontend" it must be them.
	var frontend Record
	for _, r := range recs {
		if r.Name == "@grafana/grafana-frontend-platform" {
			frontend = r
		}
	}
	allTopics := append(append([]string(nil), frontend.Topics...), frontend.WeakTopics...)
	if !slices.Contains(allTopics, "frontend") {
		t.Errorf("frontend platform topics %v lack frontend", allTopics)
	}

	// Determinism is a product claim: the same file reads the same twice.
	again, err := parseCodeOwners(context.Background(), strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if diff := cmp.Diff(recs, again); diff != "" {
		t.Errorf("two parses of one file disagree:\n%s", diff)
	}
}
