package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/pflag"

	"github.com/dcadolph/peepwho/internal/fakeapi"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/util"
)

// TestInvocationsRoundTrip verifies the refresh config saves and loads, and a
// missing file loads as empty.
func TestInvocationsRoundTrip(t *testing.T) {
	t.Parallel()
	store := NewStore(Dir(t.TempDir()), nil)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	in := Invocations{
		"github": {"org:acme": {Flags: []string{"--github-org", "acme"}, RanAt: at, AttemptedAt: at.Add(time.Hour), Error: "boom"}},
		"slack":  {"all": {Flags: []string{"--include-private=true"}}},
	}
	if err := store.SaveInvocations(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := store.LoadInvocations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip mismatch (-want +got):\n%s", diff)
	}
	raw, err := os.ReadFile(store.Dir().Refresh())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("a time not known was written as a zero time: %s", raw)
	}
	empty, err := NewStore(Dir(t.TempDir()), nil).LoadInvocations()
	if err != nil || len(empty) != 0 {
		t.Errorf("missing file = %+v, %v; want empty, nil", empty, err)
	}
}

// TestInvocationsOlderShapes verifies a refresh.json from before runs carried
// times still loads as runs with no times, so nothing recorded is dropped.
func TestInvocationsOlderShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantRuns Invocations
		File     string
	}{{ // Test 0: One flag list per source folds under an empty scope.
		File: `{"git":["--repo-path","/src/b"],"slack":["--include-private=true"]}`,
		WantRuns: Invocations{
			"git":   {"": {Flags: []string{"--repo-path", "/src/b"}}},
			"slack": {"": {Flags: []string{"--include-private=true"}}},
		},
	}, { // Test 1: One flag list per scope keeps its scopes.
		File: `{"git":{"repo:/a":["--repo-path","/a"],"repo:/b":["--repo-path","/b"]},"slack":{"":null}}`,
		WantRuns: Invocations{
			"git": {
				"repo:/a": {Flags: []string{"--repo-path", "/a"}},
				"repo:/b": {Flags: []string{"--repo-path", "/b"}},
			},
			"slack": {"": {}},
		},
	}, { // Test 2: The current shape reads as it is written.
		File: `{"git":{"repo:/a":{"flags":["--repo-path","/a"],"ran_at":"2026-11-01T12:00:00Z","error":"boom"}}}`,
		WantRuns: Invocations{"git": {"repo:/a": {
			Flags: []string{"--repo-path", "/a"}, RanAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), Error: "boom",
		}}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			store := NewStore(Dir(t.TempDir()), nil)
			if err := os.WriteFile(store.Dir().Refresh(), []byte(test.File), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			out, err := store.LoadInvocations()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if diff := cmp.Diff(test.WantRuns, out); diff != "" {
				t.Errorf("load mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRecordInvocationStampsAndClears verifies a successful index run stamps both
// times and clears the failure an earlier refresh left.
func TestRecordInvocationStampsAndClears(t *testing.T) {
	t.Parallel()
	store := NewStore(Dir(t.TempDir()), nil)
	then := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := store.SaveInvocations(Invocations{"git": {"repo:/a": {
		Flags: []string{"--old"}, RanAt: then, AttemptedAt: then, Error: "boom",
	}}}); err != nil {
		t.Fatal(err)
	}
	now := then.Add(time.Hour)
	if err := store.RecordInvocation("git", "repo:/a", []string{"--new"}, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	want := Invocations{"git": {"repo:/a": {Flags: []string{"--new"}, RanAt: now, AttemptedAt: now}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestRecordAttemptMissingScope verifies a try on a scope forgotten meanwhile does
// not bring it back.
func TestRecordAttemptMissingScope(t *testing.T) {
	t.Parallel()
	store := NewStore(Dir(t.TempDir()), nil)
	if err := store.RecordAttempt("git", "repo:/a", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Invocations{}, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
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
				"jira": {"project:KAFKA": {Flags: []string{
					"--jira-server=true", "--jira-url=" + jira, "--jira-project", "KAFKA", "--policy=open",
				}}},
				"org-csv": {"file:" + people: {Flags: []string{"--file=" + people}}},
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

// TestRefreshRecordsAttempts verifies each replay writes how it went: a failure
// keeps the last success and says why, and the next success clears the failure.
func TestRefreshRecordsAttempts(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	jira := authJira(t)
	dir := Dir(t.TempDir())
	store := NewStore(dir, nil)
	then := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	jiraFlags := []string{"--jira-server=true", "--jira-url=" + jira, "--jira-project", "KAFKA"}
	csvFlags := []string{"--file=" + people}
	if err := store.SaveInvocations(Invocations{
		"jira":    {"project:KAFKA": {Flags: jiraFlags, RanAt: then, AttemptedAt: then}},
		"org-csv": {"file:" + people: {Flags: csvFlags, RanAt: then, AttemptedAt: then}},
	}); err != nil {
		t.Fatal(err)
	}
	token := "wrong"
	var clock time.Time
	x := &Indexer{
		Dir: dir, Log: &bytes.Buffer{}, Policy: policy.Default(),
		Now:   func() time.Time { return clock },
		Creds: func(string) string { return token },
	}
	clock = then.Add(time.Hour)
	if _, err := x.Refresh(context.Background(), nil, clock); err == nil {
		t.Fatal("Refresh with a wrong token returned nil")
	}
	got, err := store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	failure := got["jira"]["project:KAFKA"].Error
	if failure == "" {
		t.Error("the failed scope recorded no error")
	}
	want := Invocations{
		"jira":    {"project:KAFKA": {Flags: jiraFlags, RanAt: then, AttemptedAt: clock, Error: failure}},
		"org-csv": {"file:" + people: {Flags: csvFlags, RanAt: clock, AttemptedAt: clock}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("after a failure (-want +got):\n%s", diff)
	}
	token = "good"
	clock = clock.Add(time.Hour)
	if _, err := x.Refresh(context.Background(), []string{"jira"}, clock); err != nil {
		t.Fatalf("Refresh with the right token: %v", err)
	}
	got, err = store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Run{Flags: jiraFlags, RanAt: clock, AttemptedAt: clock}, got["jira"]["project:KAFKA"]); diff != "" {
		t.Errorf("after a success (-want +got):\n%s", diff)
	}
}

// TestRefreshAttemptWriteWarns verifies a try that cannot be written warns and
// leaves the refresh's own outcome alone.
func TestRefreshAttemptWriteWarns(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	dir := Dir(t.TempDir())
	if err := NewStore(dir, nil).SaveInvocations(Invocations{
		"org-csv": {"file:" + people: {Flags: []string{"--file=" + people}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir.Refresh()+util.LockSuffix, 0o700); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	x := &Indexer{Dir: dir, Log: &log, Policy: policy.Default(), Now: time.Now}
	results, err := x.Refresh(context.Background(), nil, time.Now())
	if err != nil || len(results) != 1 || !results[0].OK {
		t.Errorf("Refresh = %+v, %v; want one ok result and no error", results, err)
	}
	if !strings.Contains(log.String(), "warning: could not save how refresh org-csv") {
		t.Errorf("log does not warn:\n%s", log.String())
	}
}

// TestRefreshNothingRecorded verifies a refresh with no recorded scope is a
// bad-arguments error any host can show, so it names no command line.
func TestRefreshNothingRecorded(t *testing.T) {
	t.Parallel()
	x := &Indexer{Dir: Dir(t.TempDir()), Log: &bytes.Buffer{}}
	_, err := x.Refresh(context.Background(), nil, time.Now())
	if !errors.Is(err, ErrBadArgs) || !errors.Is(err, ErrNothingRecorded) || strings.Contains(err.Error(), "peepwho") {
		t.Errorf("Refresh = %v, want ErrBadArgs and ErrNothingRecorded naming no command", err)
	}
}

// TestRefreshBadRecordedFlag verifies a recorded flag the index no longer
// knows fails that scope as bad arguments instead of running it half parsed.
func TestRefreshBadRecordedFlag(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	if err := NewStore(dir, nil).SaveInvocations(Invocations{"org-csv": {"file:x": {Flags: []string{"--no-such-flag"}}}}); err != nil {
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

// TestDropScopes verifies only the source's other scopes go, a source left with none goes too,
// and with nothing to drop the file is not touched.
func TestDropScopes(t *testing.T) {
	t.Parallel()
	recorded := Invocations{
		"git":     {"repo:/a": {Flags: []string{"--repo-path=/a"}}, "repo:/b": {Flags: []string{"--repo-path=/b"}}},
		"org-csv": {"file:/p.csv": {Flags: []string{"--file=/p.csv"}}},
		"slack":   {"": {}},
	}
	tests := []struct {
		WantScopes map[string][]string
		Source     string
		Keep       string
		Empty      bool
		WantNoFile bool
	}{{ // Test 0: Keeping one scope drops the other and leaves other sources alone.
		Source: "git", Keep: "repo:/b",
		WantScopes: map[string][]string{"git": {"repo:/b"}, "org-csv": {"file:/p.csv"}, "slack": {""}},
	}, { // Test 1: No keep drops the whole source.
		Source:     "git",
		WantScopes: map[string][]string{"org-csv": {"file:/p.csv"}, "slack": {""}},
	}, { // Test 2: A keep that is not recorded drops every scope of the source.
		Source: "org-csv", Keep: "file:/new.csv",
		WantScopes: map[string][]string{"git": {"repo:/a", "repo:/b"}, "slack": {""}},
	}, { // Test 3: A source with no scopes changes nothing.
		Source:     "jira",
		WantScopes: map[string][]string{"git": {"repo:/a", "repo:/b"}, "org-csv": {"file:/p.csv"}, "slack": {""}},
	}, { // Test 4: An empty directory stays empty.
		Source: "git", Empty: true, WantNoFile: true, WantScopes: map[string][]string{},
	}, { // Test 5: No keep drops a scope with no name too.
		Source:     "slack",
		WantScopes: map[string][]string{"git": {"repo:/a", "repo:/b"}, "org-csv": {"file:/p.csv"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := Dir(t.TempDir())
			s := NewStore(dir, nil)
			if !test.Empty {
				if err := s.SaveInvocations(recorded); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DropScopes(test.Source, test.Keep); err != nil {
				t.Fatalf("DropScopes: %v", err)
			}
			inv, err := s.LoadInvocations()
			if err != nil {
				t.Fatalf("LoadInvocations: %v", err)
			}
			got := map[string][]string{}
			for src, scopes := range inv {
				got[src] = slices.Sorted(maps.Keys(scopes))
			}
			if diff := cmp.Diff(test.WantScopes, got); diff != "" {
				t.Errorf("scopes mismatch (-want +got):\n%s", diff)
			}
			if _, err := os.Stat(dir.Refresh()); test.WantNoFile && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("refresh file written with nothing to drop: %v", err)
			}
		})
	}
}
