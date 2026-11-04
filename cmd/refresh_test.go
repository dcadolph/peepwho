package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

func TestCaptureIndexFlags(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "index"}
	f := cmd.Flags()
	var org, source string
	var repos []string
	var merge bool
	f.StringVar(&org, "github-org", "", "")
	f.StringArrayVar(&repos, "repo", nil, "")
	f.StringVar(&source, "source", "", "")
	f.BoolVar(&merge, "merge", false, "")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.Set("github-org", "acme"))
	must(f.Set("repo", "a/b"))
	must(f.Set("repo", "c/d"))
	must(f.Set("source", "github"))
	must(f.Set("merge", "true"))

	got := strings.Join(captureIndexFlags(cmd), " ")
	if !strings.Contains(got, "--github-org=acme") {
		t.Errorf("missing github-org: %q", got)
	}
	if !strings.Contains(got, "--repo a/b") || !strings.Contains(got, "--repo c/d") {
		t.Errorf("missing repeated repo flags: %q", got)
	}
	if strings.Contains(got, "merge") || strings.Contains(got, "source") {
		t.Errorf("replayed a skipped flag: %q", got)
	}
}

func TestInvocationsRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "refresh.json")
	in := invocations{
		"github": {"org:acme": {"--github-org", "acme"}},
		"slack":  {"all": {"--include-private=true"}},
	}
	if err := saveInvocations(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := loadInvocations(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(out) != 2 || strings.Join(out["github"]["org:acme"], " ") != "--github-org acme" {
		t.Errorf("round trip mismatch: %+v", out)
	}
	empty, err := loadInvocations(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(empty) != 0 {
		t.Errorf("missing file = %+v, %v; want empty, nil", empty, err)
	}
}

// TestInvocationsLegacyFormat verifies a refresh.json written before scope
// keys existed still loads, folded under an empty scope, so nothing already
// recorded is dropped by the upgrade.
func TestInvocationsLegacyFormat(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "refresh.json")
	legacy := []byte(`{"git":["--repo-path","/src/b"],"slack":["--include-private=true"]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := loadInvocations(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diff := cmp.Diff(invocations{
		"git":   {"": {"--repo-path", "/src/b"}},
		"slack": {"": {"--include-private=true"}},
	}, out); diff != "" {
		t.Errorf("legacy load mismatch (-want +got):\n%s", diff)
	}
}

// TestSaveInvocationKeepsEveryScope indexes two repositories in separate git
// runs and verifies both survive in the refresh config. Keyed by source alone,
// the second run silently replaced the first and the weekly refresh let the
// first repository go stale with no warning.
func TestSaveInvocationKeepsEveryScope(t *testing.T) {
	t.Parallel()
	opts := &options{dataDir: t.TempDir()}
	run := func(repo string) {
		cmd := &cobra.Command{Use: "index"}
		var repoPaths []string
		cmd.Flags().StringArrayVar(&repoPaths, "repo-path", nil, "")
		if err := cmd.Flags().Set("repo-path", repo); err != nil {
			t.Fatal(err)
		}
		if err := saveInvocation(opts, cmd, "git", "repo:"+repo); err != nil {
			t.Fatalf("save %s: %v", repo, err)
		}
	}
	run("/src/a")
	run("/src/b")
	out, err := loadInvocations(opts.refreshPath())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diff := cmp.Diff(invocations{"git": {
		"repo:/src/a": {"--repo-path", "/src/a"},
		"repo:/src/b": {"--repo-path", "/src/b"},
	}}, out); diff != "" {
		t.Errorf("scopes lost (-want +got):\n%s", diff)
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
			got := refreshScope(test.Source, test.WatermarkScope, test.File, test.RepoPaths)
			if diff := cmp.Diff(test.WantScope, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
