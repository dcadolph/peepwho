package cmd

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
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
	out, err := engine.NewStore(opts.dir(), nil).LoadInvocations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diff := cmp.Diff(engine.Invocations{"git": {
		"repo:/src/a": {"--repo-path", "/src/a"},
		"repo:/src/b": {"--repo-path", "/src/b"},
	}}, out); diff != "" {
		t.Errorf("scopes lost (-want +got):\n%s", diff)
	}
}
