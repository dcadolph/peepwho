package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/policy"
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

// TestRecordedPolicy verifies a refreshed scope runs under the --policy it was
// recorded with, or the flag's default when none was, and never under the
// --policy the refresh itself was given.
func TestRecordedPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Recorded string
		WantMode policy.Mode
		WantErr  bool
	}{{ // Test 0: Nothing recorded runs under the default mode.
		Recorded: "", WantMode: policy.Strict,
	}, { // Test 1: A recorded mode is used.
		Recorded: "open", WantMode: policy.Open,
	}, { // Test 2: A recorded mode that no longer parses fails the scope.
		Recorded: "loose", WantErr: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			root := &cobra.Command{Use: "peepwho"}
			root.PersistentFlags().String("policy", "strict", "")
			sub := &cobra.Command{Use: "refresh"}
			root.AddCommand(sub)
			o := &options{policyName: "redacted", systemPolicyFile: filepath.Join(t.TempDir(), "absent.json")}
			got, err := o.recordedPolicy(sub)(test.Recorded)
			if (err != nil) != test.WantErr {
				t.Fatalf("recordedPolicy(%q) error = %v, want error %t", test.Recorded, err, test.WantErr)
			}
			if err == nil && got.Mode() != test.WantMode {
				t.Errorf("mode = %v, want %v", got.Mode(), test.WantMode)
			}
			if o.policyName != "redacted" {
				t.Errorf("refresh's own policy changed to %q", o.policyName)
			}
		})
	}
}
