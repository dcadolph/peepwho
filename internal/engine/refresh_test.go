package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestInvocationsRoundTrip verifies the refresh config saves and loads, and a
// missing file loads as empty.
func TestInvocationsRoundTrip(t *testing.T) {
	t.Parallel()
	store := NewStore(Dir(t.TempDir()), nil)
	in := Invocations{
		"github": {"org:acme": {"--github-org", "acme"}},
		"slack":  {"all": {"--include-private=true"}},
	}
	if err := store.SaveInvocations(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := store.LoadInvocations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out) != 2 || strings.Join(out["github"]["org:acme"], " ") != "--github-org acme" {
		t.Errorf("round trip mismatch: %+v", out)
	}
	empty, err := NewStore(Dir(t.TempDir()), nil).LoadInvocations()
	if err != nil || len(empty) != 0 {
		t.Errorf("missing file = %+v, %v; want empty, nil", empty, err)
	}
}

// TestInvocationsLegacyFormat verifies a refresh.json written before scope
// keys existed still loads, folded under an empty scope, so nothing already
// recorded is dropped by the upgrade.
func TestInvocationsLegacyFormat(t *testing.T) {
	t.Parallel()
	store := NewStore(Dir(t.TempDir()), nil)
	legacy := []byte(`{"git":["--repo-path","/src/b"],"slack":["--include-private=true"]}`)
	if err := os.WriteFile(store.Dir().Refresh(), legacy, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := store.LoadInvocations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diff := cmp.Diff(Invocations{
		"git":   {"": {"--repo-path", "/src/b"}},
		"slack": {"": {"--include-private=true"}},
	}, out); diff != "" {
		t.Errorf("legacy load mismatch (-want +got):\n%s", diff)
	}
}

// TestRefreshScope verifies the scope keys that keep separate runs separate.
func TestRefreshScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantScope      string
		Source         string
		WatermarkScope string
		File           string
		RepoPaths      []string
	}{{ // Test 0: Git keys by its repo set, order-independent.
		Source: "git", RepoPaths: []string{"/b", "/a"}, WantScope: "repo:/a,/b",
	}, { // Test 1: A file source keys by its file.
		Source: "org-csv", File: "people.csv", WantScope: "file:people.csv",
	}, { // Test 2: An incremental source reuses its watermark scope.
		Source: "github", WatermarkScope: "org:acme", WantScope: "org:acme",
	}, { // Test 3: A source with no scope flags keys everything together.
		Source: "pagerduty", WantScope: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := RefreshScope(test.Source, test.WatermarkScope, test.File, test.RepoPaths)
			if diff := cmp.Diff(test.WantScope, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
