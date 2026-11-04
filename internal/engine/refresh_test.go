package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/pflag"

	"github.com/dcadolph/peepwho/internal/fakeapi"
	"github.com/dcadolph/peepwho/internal/policy"
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

// authJira serves a fake Jira Server site that answers only the bearer token
// good, so a refresh can be run with a right and a wrong token.
func authJira(t *testing.T) string {
	t.Helper()
	fake := &fakeapi.Jira{ServerMode: true, Issues: []fakeapi.JiraIssue{{
		Key: "KAFKA-1", Summary: "consumer group rebalance", Description: "session timeout",
		AssigneeEmail: "amaki@apache.invalid", ProjectKey: "KAFKA", ProjectName: "Kafka",
		Updated: "2026-06-20T09:30:00.000+0000",
	}}}
	inner := fake.Server()
	t.Cleanup(inner.Close)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRefresh replays recorded scopes in process and reports each one, so a
// wrong token fails its own scope, by name, and leaves the others indexed.
func TestRefresh(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		Sources      []string
		WantResults  []string
		WantPolicies []string
		WantAuth     bool
		WantErr      string
		Token        string
	}{{ // Test 0: The right token indexes both sources, in name order.
		Token:        "good",
		WantResults:  []string{"jira project:KAFKA ok", "org-csv file:" + people + " ok"},
		WantPolicies: []string{"open", ""},
	}, { // Test 1: A wrong token fails jira alone and names the run count.
		Token:        "wrong",
		WantResults:  []string{"jira project:KAFKA failed", "org-csv file:" + people + " ok"},
		WantPolicies: []string{"open", ""},
		WantAuth:     true,
		WantErr:      "refresh: 1 of 2 run(s) failed",
	}, { // Test 2: Naming one source replays only that source.
		Token:        "wrong",
		Sources:      []string{"org-csv"},
		WantResults:  []string{"org-csv file:" + people + " ok"},
		WantPolicies: []string{""},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			jira := authJira(t)
			dir := Dir(t.TempDir())
			if err := NewStore(dir, nil).SaveInvocations(Invocations{
				"jira": {"project:KAFKA": {
					"--jira-server=true", "--jira-url=" + jira, "--jira-project", "KAFKA", "--policy=open",
				}},
				"org-csv": {"file:" + people: {"--file=" + people}},
			}); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			var policies []string
			x := &Indexer{
				Dir: dir, Log: &log, Now: func() time.Time { return now },
				Policy: policy.Default(),
				Creds: func(env string) string {
					if env == "PEEPWHO_JIRA_TOKEN" {
						return test.Token
					}
					return ""
				},
				PolicyFor: func(recorded string) (policy.Policy, error) {
					policies = append(policies, recorded)
					return policy.Default(), nil
				},
				Record: func(string, string) error {
					t.Error("refresh recorded a replay")
					return nil
				},
			}
			results, err := x.Refresh(context.Background(), test.Sources, now)
			if got := fmt.Sprint(err); test.WantErr != "" && got != test.WantErr {
				t.Errorf("Refresh error = %q, want %q", got, test.WantErr)
			}
			if test.WantErr == "" && err != nil {
				t.Errorf("Refresh error = %v, want nil\n%s", err, log.String())
			}
			var got []string
			for _, r := range results {
				word := "ok"
				if !r.OK {
					word = "failed"
				}
				got = append(got, r.Source+" "+r.Scope+" "+word)
				if !r.RanAt.Equal(now) {
					t.Errorf("%s RanAt = %v, want %v", r.Source, r.RanAt, now)
				}
				if r.OK != (r.Err == nil) {
					t.Errorf("%s OK = %v with Err %v", r.Source, r.OK, r.Err)
				}
				if r.Source == "jira" && errors.Is(r.Err, ErrAuth) != test.WantAuth {
					t.Errorf("jira Err = %v, want ErrAuth %v", r.Err, test.WantAuth)
				}
			}
			if diff := cmp.Diff(test.WantResults, got); diff != "" {
				t.Errorf("results mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPolicies, policies); diff != "" {
				t.Errorf("recorded policies mismatch (-want +got):\n%s", diff)
			}
			if test.WantAuth && !strings.Contains(log.String(), "refresh jira (project:KAFKA) failed: ") {
				t.Errorf("log does not name the failed scope:\n%s", log.String())
			}
		})
	}
}

// TestRefreshNothingRecorded verifies a refresh with no recorded scope is a
// bad-arguments error that says how to record one.
func TestRefreshNothingRecorded(t *testing.T) {
	t.Parallel()
	x := &Indexer{Dir: Dir(t.TempDir()), Log: &bytes.Buffer{}}
	_, err := x.Refresh(context.Background(), nil, time.Now())
	if !errors.Is(err, ErrBadArgs) || !strings.Contains(err.Error(), "peepwho index --source") {
		t.Errorf("Refresh = %v, want ErrBadArgs naming peepwho index", err)
	}
}

// TestRefreshBadRecordedFlag verifies a recorded flag the index no longer
// knows fails that scope as bad arguments instead of running it half parsed.
func TestRefreshBadRecordedFlag(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	if err := NewStore(dir, nil).SaveInvocations(Invocations{"org-csv": {"file:x": {"--no-such-flag"}}}); err != nil {
		t.Fatal(err)
	}
	x := &Indexer{Dir: dir, Log: &bytes.Buffer{}}
	results, err := x.Refresh(context.Background(), nil, time.Now())
	if err == nil || len(results) != 1 || !errors.Is(results[0].Err, ErrBadArgs) {
		t.Errorf("Refresh = %+v, %v; want one ErrBadArgs result and an error", results, err)
	}
}

// TestReplayFlags verifies a command line's flag set records only the flags an
// index run takes, less the ones that steer one run or its output, in name
// order with a repeatable flag as one pair per value.
func TestReplayFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Set        [][2]string
		WantReplay []string
	}{{ // Test 0: Nothing set records nothing.
		Set: nil, WantReplay: nil,
	}, { // Test 1: Source flags kept, command line and run-steering flags dropped.
		Set: [][2]string{
			{"source", "github"}, {"data-dir", "/d"}, {"pretty", "true"}, {"json", "true"},
			{"human", "true"}, {"merge", "true"}, {"full", "true"}, {"allow-shrink", "true"},
			{"changes-file", "c.json"}, {"github-org", "acme"}, {"repo", "a/b"}, {"repo", "c/d"},
			{"policy", "open"},
		},
		WantReplay: []string{"--github-org=acme", "--policy=open", "--repo", "a/b", "--repo", "c/d"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var args IndexArgs
			fs := pflag.NewFlagSet("index", pflag.ContinueOnError)
			RegisterIndexFlags(fs, &args)
			fs.String("source", "", "")
			fs.String("data-dir", "", "")
			fs.String("policy", "", "")
			fs.Bool("pretty", false, "")
			fs.Bool("json", false, "")
			fs.Bool("human", false, "")
			for _, kv := range test.Set {
				if err := fs.Set(kv[0], kv[1]); err != nil {
					t.Fatalf("set %s: %v", kv[0], err)
				}
			}
			if diff := cmp.Diff(test.WantReplay, ReplayFlags(fs)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
