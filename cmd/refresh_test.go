package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/policy"
)

// TestIndexRecordsEveryScope indexes two files in separate org-csv runs and a
// json run from standard input, and verifies the refresh config keeps both
// files under their own scopes, records exactly what the core would record for
// the same flags, and leaves out the stdin run, which a refresh could not
// supply. Keyed by source alone, the second run once replaced the first and the
// weekly refresh let the first file go stale with no warning.
func TestIndexRecordsEveryScope(t *testing.T) {
	t.Parallel()
	people, err := os.ReadFile("../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	dir, files := t.TempDir(), t.TempDir()
	a, b := filepath.Join(files, "a.csv"), filepath.Join(files, "b.csv")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, people, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		Stdin string
		Args  []string
	}{{
		Args: []string{"index", "--source", "org-csv", "--file", a, "--json"},
	}, {
		Args: []string{"index", "--source", "org-csv", "--file", b, "--merge", "--pretty"},
	}, {
		Stdin: `[{"name":"Ann Lee","email":"ann@x.com"}]`,
		Args:  []string{"index", "--source", "json", "--merge"},
	}}
	for stepNum, step := range steps {
		args := append(append([]string(nil), step.Args...), "--data-dir", dir)
		if _, errOut, err := runCmdIn(t, step.Stdin, args...); err != nil {
			t.Fatalf("step %d %v: %v\n%s", stepNum, step.Args, err, errOut)
		}
	}
	got, err := engine.NewStore(engine.Dir(dir), nil).LoadInvocations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := engine.Invocations{"org-csv": {}}
	for _, f := range []string{a, b} {
		run, err := engine.IndexFromFlags("org-csv", []string{"--file", f})
		if err != nil || len(run.Replay) != 1 || run.Replay[0] != "--file="+f {
			t.Fatalf("core records %q, %v; want only --file=%s", run.Replay, err, f)
		}
		want["org-csv"][engine.RefreshScope("org-csv", "", f, nil)] = engine.Run{Flags: run.Replay}
	}
	for _, scopes := range got {
		for scope, run := range scopes {
			if run.RanAt.IsZero() || !run.RanAt.Equal(run.AttemptedAt) {
				t.Errorf("%s recorded times %v and %v, want both set and equal", scope, run.RanAt, run.AttemptedAt)
			}
		}
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(engine.Run{}, "RanAt", "AttemptedAt")); diff != "" {
		t.Errorf("recorded runs mismatch (-want +got):\n%s", diff)
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
