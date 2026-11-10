package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestSourceNames pins the registry's names and order, which the unknown-source
// error lists, so dropping or reordering an entry fails here.
func TestSourceNames(t *testing.T) {
	t.Parallel()
	want := "org-csv, matters, slack, slack-export, codeowners, github, jira, confluence, " +
		"pagerduty, git, json, or graph"
	if diff := cmp.Diff(want, SourceNames()); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestSourceAttributes pins which sources are incremental, keyed by file, read
// stdin, and are offered by connect.
func TestSourceAttributes(t *testing.T) {
	t.Parallel()
	var incremental, fileKeyed, stdin, connectable []string
	for _, s := range Sources() {
		if s.Incremental {
			incremental = append(incremental, s.Name)
		}
		if s.FileKeyed {
			fileKeyed = append(fileKeyed, s.Name)
		}
		if s.Stdin {
			stdin = append(stdin, s.Name)
		}
		if s.Connectable {
			connectable = append(connectable, s.Name)
		}
	}
	tests := []struct {
		Name       string
		Got        []string
		WantResult []string
	}{{ // Test 0: Incremental sources.
		Name: "incremental", Got: incremental,
		WantResult: []string{"slack", "github", "jira", "confluence", "git"},
	}, { // Test 1: File-keyed refresh scope.
		Name: "file-keyed", Got: fileKeyed,
		WantResult: []string{"org-csv", "matters", "slack-export", "codeowners", "json"},
	}, { // Test 2: Stdin reader.
		Name: "stdin", Got: stdin, WantResult: []string{"json"},
	}, { // Test 3: Connect offers ten sources, not slack-export or json.
		Name: "connectable", Got: connectable,
		WantResult: []string{
			"org-csv", "matters", "slack", "codeowners", "github", "jira", "confluence",
			"pagerduty", "git", "graph",
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantResult, test.Got); diff != "" {
				t.Errorf("%s mismatch (-want +got):\n%s", test.Name, diff)
			}
		})
	}
}

// TestSourceByName verifies lookup of a known and an unknown name.
func TestSourceByName(t *testing.T) {
	t.Parallel()
	if s, ok := SourceByName("git"); !ok || s.Name != "git" {
		t.Errorf("git: got %q, %v", s.Name, ok)
	}
	if _, ok := SourceByName("nosuch"); ok {
		t.Error("nosuch: want not found")
	}
}

// TestSourceValidation verifies each source refuses missing inputs with the
// message the index command has always printed, and builds when given them.
func TestSourceValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Source  string
		Opts    SourceOptions
		WantMsg string
		Want    error
	}{{ // Test 0: org-csv needs a file.
		Source: "org-csv", WantMsg: "--file is required for org-csv", Want: ErrBadArgs,
	}, { // Test 1: org-csv with a file builds.
		Source: "org-csv", Opts: SourceOptions{File: "people.csv"},
	}, { // Test 2: matters needs a file.
		Source: "matters", WantMsg: "is required for matters", Want: ErrBadArgs,
	}, { // Test 3: codeowners needs a file.
		Source: "codeowners", WantMsg: "required for codeowners", Want: ErrBadArgs,
	}, { // Test 4: slack-export needs a file.
		Source: "slack-export", WantMsg: "is required for slack-export", Want: ErrBadArgs,
	}, { // Test 5: slack needs a token.
		Source: "slack", WantMsg: "set " + SlackTokenEnv, Want: ErrBadArgs,
	}, { // Test 6: slack private channels need the policy.
		Source: "slack",
		Opts: SourceOptions{
			Creds: map[string]string{SlackTokenEnv: "xoxb-1"},
			Slack: connector.SlackOptions{IncludePrivate: true},
		},
		WantMsg: "private-channel ingest is disabled by policy", Want: ErrBadArgs,
	}, { // Test 7: slack private channels with the policy build.
		Source: "slack",
		Opts: SourceOptions{
			Creds: map[string]string{SlackTokenEnv: "xoxb-1"}, AllowPrivateChannels: true,
			Slack: connector.SlackOptions{IncludePrivate: true},
		},
	}, { // Test 8: github needs a token.
		Source: "github", WantMsg: "set " + GitHubTokenEnv, Want: ErrBadArgs,
	}, { // Test 9: github needs a repo or an org.
		Source: "github", Opts: SourceOptions{Creds: map[string]string{GitHubTokenEnv: "ghp_1"}},
		WantMsg: "--repo or --github-org required for github", Want: ErrBadArgs,
	}, { // Test 10: jira cloud needs a site, an email, and a token.
		Source: "jira", Opts: SourceOptions{JiraURL: "https://x.atlassian.net"},
		WantMsg: "--jira-server for a self-hosted site", Want: ErrBadArgs,
	}, { // Test 11: jira server needs only the site.
		Source: "jira", Opts: SourceOptions{Jira: connector.JiraOptions{Server: true}},
		WantMsg: "for the Server site", Want: ErrBadArgs,
	}, { // Test 12: jira server with a site from the credential builds.
		Source: "jira",
		Opts: SourceOptions{
			Creds: map[string]string{JiraURLEnv: "https://jira.example"},
			Jira:  connector.JiraOptions{Server: true},
		},
	}, { // Test 13: confluence falls back to the Jira credentials.
		Source: "confluence",
		Opts: SourceOptions{Creds: map[string]string{
			JiraURLEnv: "https://x.atlassian.net", JiraEmailEnv: "a@b.c", JiraTokenEnv: "t",
		}},
	}, { // Test 14: confluence cloud with nothing set refuses.
		Source: "confluence", WantMsg: "or the Jira ones", Want: ErrBadArgs,
	}, { // Test 15: pagerduty needs a token.
		Source: "pagerduty", WantMsg: "set " + PagerDutyTokenEnv, Want: ErrBadArgs,
	}, { // Test 16: graph needs a token.
		Source: "graph", WantMsg: "set " + GraphTokenEnv + " for the graph source", Want: ErrBadArgs,
	}, { // Test 17: git needs a repo path.
		Source: "git", WantMsg: "--repo-path is required for git", Want: ErrBadArgs,
	}, { // Test 18: json with no file reads stdin.
		Source: "json", Opts: SourceOptions{Stdin: strings.NewReader("[]")},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			spec, ok := SourceByName(test.Source)
			if !ok {
				t.Fatalf("source %q not registered", test.Source)
			}
			src, err := spec.New(test.Opts)
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v", err, test.Want)
			}
			if err != nil {
				if !strings.Contains(err.Error(), test.WantMsg) {
					t.Errorf("message %q missing %q", err, test.WantMsg)
				}
				return
			}
			if src == nil {
				t.Error("nil source with no error")
			}
		})
	}
}

// TestJSONSource verifies the json source reads stdin and a named file, and
// reports a file it cannot open.
func TestJSONSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "people.json")
	if err := os.WriteFile(path, []byte(`[{"name":"Pat","email":"pat@corp.com"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		Opts      SourceOptions
		WantCount int
		Want      error
	}{{ // Test 0: Empty stdin array.
		Opts: SourceOptions{Stdin: strings.NewReader("[]")}, WantCount: 0,
	}, { // Test 1: A named file.
		Opts: SourceOptions{File: path}, WantCount: 1,
	}, { // Test 2: A missing file.
		Opts: SourceOptions{File: filepath.Join(dir, "missing.json")}, Want: fs.ErrNotExist,
	}}
	spec, _ := SourceByName("json")
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			src, err := spec.New(test.Opts)
			if err != nil {
				t.Fatal(err)
			}
			recs, err := src.Fetch(context.Background())
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v", err, test.Want)
			}
			if diff := cmp.Diff(test.WantCount, len(recs)); diff != "" {
				t.Errorf("count mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReadsStdin verifies only a stdin source given no file, or "-", counts as
// reading standard input.
func TestReadsStdin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Source    string
		File      string
		WantStdin bool
	}{{ // Test 0: The json source with no file reads stdin.
		Source: "json", WantStdin: true,
	}, { // Test 1: The json source with "-" reads stdin.
		Source: "json", File: "-", WantStdin: true,
	}, { // Test 2: The json source with a file does not.
		Source: "json", File: "people.json",
	}, { // Test 3: A source that never reads stdin does not, even with no file.
		Source: "org-csv",
	}, { // Test 4: An unknown source does not.
		Source: "nosuch",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := ReadsStdin(test.Source, test.File); got != test.WantStdin {
				t.Errorf("ReadsStdin(%q, %q) = %t, want %t", test.Source, test.File, got, test.WantStdin)
			}
		})
	}
}
