package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/fakeapi"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/simorg"
	"github.com/dcadolph/peepwho/internal/vault"
)

// testKey is a fixed 32-byte raw key for sealing test data directories.
var testKey = []byte("0123456789abcdef0123456789abcdef") //nolint:gochecknoglobals // Not modified, simplifies testing.

// testKeyB64 is testKey in the form peepwho vault keygen prints.
var testKeyB64 = base64.StdEncoding.EncodeToString(testKey) //nolint:gochecknoglobals // Not modified, simplifies testing.

// newTestDataDir writes the simulated company and its conversations to a temp
// data directory sealed with c, or plain when c is nil.
func newTestDataDir(t *testing.T, c vault.Codec) string {
	t.Helper()
	ix, err := simorg.BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("simorg.BuildIndex: %v", err)
	}
	eps, err := simorg.BuildEpisodes(ix)
	if err != nil {
		t.Fatalf("simorg.BuildEpisodes: %v", err)
	}
	dir := t.TempDir()
	st := engine.NewStore(engine.Dir(dir), c)
	if err := st.SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	if err := st.SaveEpisodes(eps); err != nil {
		t.Fatalf("SaveEpisodes: %v", err)
	}
	return dir
}

// testKeyCipher returns the raw-key codec over testKey.
func testKeyCipher(t *testing.T) vault.Codec {
	t.Helper()
	c, err := vault.NewKeyCipher(testKey)
	if err != nil {
		t.Fatalf("vault.NewKeyCipher: %v", err)
	}
	return c
}

// TestOpen verifies plain and sealed directories open with the right key, that
// every failure an app can fix names the key instead of crashing, and that a
// directory with no index yet opens unbuilt.
func TestOpen(t *testing.T) {
	t.Parallel()
	plain := newTestDataDir(t, nil)
	keyed := newTestDataDir(t, testKeyCipher(t))
	passed := newTestDataDir(t, vault.NewPassphraseCipher([]byte("correct horse")))
	otherKey := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	tests := []struct {
		Want        error
		Dir         string
		Key         string
		WantMessage string
	}{{ // Test 0: Plain directory, no key.
		Dir: plain,
	}, { // Test 1: Sealed directory, right key.
		Dir: keyed, Key: testKeyB64,
	}, { // Test 2: Sealed directory, right key with surrounding whitespace.
		Dir: keyed, Key: " " + testKeyB64 + "\n",
	}, { // Test 3: Sealed directory, no key.
		Dir: keyed, Want: vault.ErrEncrypted, WantMessage: "index key",
	}, { // Test 4: Sealed directory, wrong key.
		Dir: keyed, Key: otherKey, Want: vault.ErrCorrupt, WantMessage: "index key does not open",
	}, { // Test 5: Key that is not base64.
		Dir: keyed, Key: "not base64!", WantMessage: "must be base64",
	}, { // Test 6: Key of the wrong length.
		Dir: keyed, Key: base64.StdEncoding.EncodeToString([]byte("short")), WantMessage: "index key",
	}, { // Test 7: Passphrase-sealed directory, no key.
		Dir: passed, Want: vault.ErrEncrypted, WantMessage: "sealed with a passphrase",
	}, { // Test 8: Empty directory argument.
		Dir: "  ", WantMessage: "data directory is empty",
	}, { // Test 9: Directory with no index opens unbuilt.
		Dir: t.TempDir(),
	}, { // Test 10: Directory that does not exist yet opens unbuilt.
		Dir: filepath.Join(t.TempDir(), "Application Support", "data"),
	}, { // Test 11: An index that is not a file still fails.
		Dir: newIndexDirTrap(t), WantMessage: "open",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			h, err := Open(test.Dir, test.Key)
			if test.WantMessage == "" {
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				if h == nil {
					t.Fatal("Open returned a nil handle and no error")
				}
				return
			}
			if err == nil {
				t.Fatalf("Open succeeded, want an error containing %q", test.WantMessage)
			}
			if h != nil {
				t.Error("Open returned a handle alongside an error")
			}
			if test.Want != nil && !errors.Is(err, test.Want) {
				t.Errorf("Open error = %v, want %v", err, test.Want)
			}
			if !strings.Contains(err.Error(), test.WantMessage) {
				t.Errorf("Open error %q does not contain %q", err, test.WantMessage)
			}
		})
	}
}

// TestHandleAskMatchesEngine verifies the core answer is the same JSON the
// engine's keyword answer encodes to, so an app and the command line agree.
func TestHandleAskMatchesEngine(t *testing.T) {
	t.Parallel()
	dir := newTestDataDir(t, testKeyCipher(t))
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := engine.NewStore(engine.Dir(dir), testKeyCipher(t))
	ix, err := st.LoadIndex()
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	ans, err := engine.New(ix, policy.Default()).Ask(context.Background(), "billing retries", 5,
		engine.ResolverConfig{})
	if err != nil {
		t.Fatalf("engine Ask: %v", err)
	}
	want, err := json.Marshal(ans.View("billing retries"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := h.Ask("  billing retries ", 0)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("ask mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(got, `"angela@example.com"`) {
		t.Errorf("ask answer does not name the billing owner: %s", got)
	}
}

// TestHandleCalls verifies each call's success and failure shapes.
func TestHandleCalls(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, nil), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tests := []struct {
		Call         func() (string, error)
		WantContains string
		WantErr      string
	}{{ // Test 0: Empty question.
		Call: func() (string, error) { return h.Ask(" ", 5) }, WantErr: "needs a question",
	}, { // Test 1: Search by name.
		Call: func() (string, error) { return h.Search("angela", 3) }, WantContains: `"angela@example.com"`,
	}, { // Test 2: Empty search.
		Call: func() (string, error) { return h.Search("", 3) }, WantErr: "needs a query",
	}, { // Test 3: Known person.
		Call: func() (string, error) { return h.Person("angela@example.com") }, WantContains: `"Angela Malone"`,
	}, { // Test 4: Unknown person.
		Call: func() (string, error) { return h.Person("nobody@nowhere.example") }, WantErr: "unknown person",
	}, { // Test 5: Recall for the demo person.
		Call:         func() (string, error) { return h.Recall("angela@example.com", "billing retries", 5) },
		WantContains: `"episodes":[{`,
	}, { // Test 6: Recall with no person.
		Call: func() (string, error) { return h.Recall("", "billing", 5) }, WantErr: "needs a person",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, err := test.Call()
			if test.WantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.WantErr) {
					t.Errorf("error = %v, want one containing %q", err, test.WantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if !strings.Contains(got, test.WantContains) {
				t.Errorf("result %s does not contain %s", got, test.WantContains)
			}
		})
	}
}

// TestHandleStatus verifies age comes from the host clock and the counts and
// seal flag reflect what was opened.
func TestHandleStatus(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, testKeyCipher(t)), testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	built := h.current().eng.Index().BuiltAt()
	raw, err := h.Status(built.Unix() + 3600)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got statusView
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if got.AgeSeconds < 3599 || got.AgeSeconds > 3600 {
		t.Errorf("age_seconds = %d, want 3600 from the supplied clock", got.AgeSeconds)
	}
	if !got.Sealed {
		t.Error("status reports a keyed index as not sealed")
	}
	if got.People == 0 || got.Conversations == 0 || got.BuiltAt == "" {
		t.Errorf("status missing counts or build time: %s", raw)
	}
}

// TestUnopenedHandle verifies a handle not made by Open fails every call with
// an error instead of a crash, since gomobile lets Swift construct one.
func TestUnopenedHandle(t *testing.T) {
	t.Parallel()
	var nilHandle *Handle
	for _, h := range []*Handle{{}, nilHandle} {
		calls := []func() (string, error){
			func() (string, error) { return h.Ask("billing", 5) },
			func() (string, error) { return h.Search("billing", 5) },
			func() (string, error) { return h.Person("angela@example.com") },
			func() (string, error) { return h.Recall("angela@example.com", "billing", 5) },
			func() (string, error) { return h.Status(0) },
			func() (string, error) { return h.ChangesSince(0) },
			func() (string, error) { return h.Index("org-csv", "--file=people.csv", 0) },
			func() (string, error) { return h.Refresh("", 0) },
		}
		for i, call := range calls {
			if _, err := call(); !errors.Is(err, errNotOpen) {
				t.Errorf("call %d on an unopened handle = %v, want %v", i, err, errNotOpen)
			}
		}
	}
}

// TestClampLimit verifies limits map onto the accepted range.
func TestClampLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   int
		Want int
	}{
		{In: -1, Want: defaultLimit},   // Test 0: Negative.
		{In: 0, Want: defaultLimit},    // Test 1: Zero.
		{In: 7, Want: 7},               // Test 2: In range.
		{In: maxLimit, Want: maxLimit}, // Test 3: At the cap.
		{In: 1 << 20, Want: maxLimit},  // Test 4: Beyond the cap.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, clampLimit(test.In)); diff != "" {
				t.Errorf("clampLimit mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// mapSecrets is a Secrets backed by a map, standing in for the host Keychain.
type mapSecrets map[string]string

// Get returns the value stored under name.
func (m mapSecrets) Get(name string) string { return m[name] }

// TestHandleSecrets verifies the handle reads credentials only from the
// Secrets the host set, and reads every name as empty without one.
func TestHandleSecrets(t *testing.T) {
	t.Parallel()
	var nilHandle *Handle
	tests := []struct {
		Handle    *Handle
		Secrets   Secrets
		Name      string
		WantValue string
	}{{ // Test 0: Name the host stored.
		Handle: &Handle{}, Secrets: mapSecrets{engine.JiraTokenEnv: "tok-1"},
		Name: engine.JiraTokenEnv, WantValue: "tok-1",
	}, { // Test 1: Name the host does not hold.
		Handle: &Handle{}, Secrets: mapSecrets{engine.JiraTokenEnv: "tok-1"}, Name: "PEEPWHO_SLACK_TOKEN",
	}, { // Test 2: No Secrets set.
		Handle: &Handle{}, Name: engine.JiraTokenEnv,
	}, { // Test 3: Secrets cleared with nil.
		Handle: &Handle{secrets: mapSecrets{engine.JiraTokenEnv: "tok-1"}}, Name: engine.JiraTokenEnv,
	}, { // Test 4: Nil handle.
		Handle: nilHandle, Secrets: mapSecrets{engine.JiraTokenEnv: "tok-1"}, Name: engine.JiraTokenEnv,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			test.Handle.SetSecrets(test.Secrets)
			if diff := cmp.Diff(test.WantValue, test.Handle.secret(test.Name)); diff != "" {
				t.Errorf("secret mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// foldRosters indexes each org chart roster into dir under c in turn, the
// indexer clock one hour later each time from start.
func foldRosters(t *testing.T, dir string, c vault.Codec, start time.Time, rosters ...string) {
	t.Helper()
	for i, roster := range rosters {
		csv := filepath.Join(t.TempDir(), "people.csv")
		if err := os.WriteFile(csv, []byte("name,email,title,team,topics\n"+roster), 0o600); err != nil {
			t.Fatalf("write csv: %v", err)
		}
		at := start.Add(time.Duration(i) * time.Hour)
		x := &engine.Indexer{
			Dir: engine.Dir(dir), Codec: func() (vault.Codec, error) { return c, nil },
			Now: func() time.Time { return at },
		}
		req := engine.IndexRequest{Source: "org-csv", IndexArgs: engine.IndexArgs{File: csv}}
		if err := x.Index(context.Background(), req); err != nil {
			t.Fatalf("index roster %d: %v", i, err)
		}
	}
}

// TestHandleChangesSince verifies the change log entries after the host's
// last look merge into one answer, and zero means the whole log.
func TestHandleChangesSince(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	base := "Jane Doe,jane@x.com,Engineer,Billing,retries\n"
	dir := t.TempDir()
	foldRosters(t, dir, testKeyCipher(t), start,
		base,
		base+"Ann Lee,ann@x.com,Engineer,Payments,ledger\n",
		base+"Ann Lee,ann@x.com,Engineer,Billing,ledger\n",
		base+"Ann Lee,ann@x.com,Engineer,Billing,ledger\n")
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tests := []struct {
		Since      int64
		WantResult changesView
	}{{ // Test 0: Everything the log holds.
		WantResult: changesView{
			AsOf: "2026-11-01T14:00:00Z", Entries: 2,
			Changes: index.Changes{PeopleJoined: []string{"Ann Lee"}, TeamsRemoved: []string{"Payments"}},
		},
	}, { // Test 1: After the first logged fold.
		Since: start.Add(time.Hour).Unix(),
		WantResult: changesView{
			AsOf: "2026-11-01T14:00:00Z", Entries: 1,
			Changes: index.Changes{TeamsRemoved: []string{"Payments"}},
		},
	}, { // Test 2: After the newest fold.
		Since: start.Add(3 * time.Hour).Unix(),
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			raw, err := h.ChangesSince(test.Since)
			if err != nil {
				t.Fatalf("ChangesSince: %v", err)
			}
			var got changesView
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("changes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// newIndexDirTrap returns a data directory whose index path is a directory, so
// loading it fails with something other than a missing file.
func newIndexDirTrap(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(engine.Dir(dir).Index(), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestUnbuiltHandle verifies a handle on a directory with no index says so on
// every question, reports empty status, and reads an empty change log.
func TestUnbuiltHandle(t *testing.T) {
	t.Parallel()
	h, err := Open(t.TempDir(), testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i, call := range []func() (string, error){
		func() (string, error) { return h.Ask("billing", 5) },
		func() (string, error) { return h.Search("billing", 5) },
		func() (string, error) { return h.Person("angela@example.com") },
		func() (string, error) { return h.Recall("angela@example.com", "billing", 5) },
	} {
		if _, err := call(); !errors.Is(err, errNoIndex) {
			t.Errorf("call %d = %v, want %v", i, err, errNoIndex)
		}
	}
	raw, err := h.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if diff := cmp.Diff(`{"people":0,"channels":0,"teams":0,"subjects":0,"words":0,"sources":{},`+
		`"embeddings":false,"conversations":0,"sealed":true,"age_seconds":0,"scopes":[],"sample":false}`, raw); diff != "" {
		t.Errorf("status mismatch (-want +got):\n%s", diff)
	}
	if raw, err := h.ChangesSince(0); err != nil || raw != `{"entries":0,"changes":{}}` {
		t.Errorf("ChangesSince = %s, %v; want an empty answer", raw, err)
	}
}

// newGitRepo makes a small repository with two authors and returns its path.
// The path holds a space, as an app's Application Support path does.
func newGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "my repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		cmd.Env = append(cmd.Env, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(nil, "init", "-q", "-b", "main")
	for i, who := range []string{"Ann Lee <ann@x.com>", "Bob Ray <bob@x.com>", "Ann Lee <ann@x.com>"} {
		name, email, _ := strings.Cut(strings.TrimSuffix(who, ">"), " <")
		file := filepath.Join(repo, "billing", fmt.Sprintf("retry%d.go", i))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("package billing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		date := fmt.Sprintf("2026-10-%02dT12:00:00Z", 10+i)
		env := []string{
			"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
		}
		run(env, "add", ".")
		run(env, "commit", "-q", "-m", "Add billing retry")
	}
	return repo
}

// TestHandleIndexAndRefresh verifies the first index of a fresh sealed
// directory builds the same index the engine does for the command line,
// records the run the way the command line records it, answers at once, and
// that Refresh replays it.
func TestHandleIndexAndRefresh(t *testing.T) {
	t.Parallel()
	repo := newGitRepo(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	flags := "--repo-path\n" + repo + "\n\n--git-since-days=10000\n--allow-shrink\n"
	dir := filepath.Join(t.TempDir(), "Application Support", "data")
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	raw, err := h.Index("git", flags, now.Unix())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	var iv indexView
	if err := json.Unmarshal([]byte(raw), &iv); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if iv.Source != "git" || iv.Scope != "repo:"+repo {
		t.Errorf("index view = %+v, want git under repo:%s", iv, repo)
	}
	if iv.Repos != 1 || iv.People == 0 {
		t.Errorf("index view read %d repositories and holds %d people, want 1 and some", iv.Repos, iv.People)
	}
	if !engine.NeedsKey(engine.Dir(dir).Index()) {
		t.Error("the first index of a keyed handle was not sealed under the key")
	}
	// The command line runs the same request through an engine indexer.
	ref := t.TempDir()
	refStore := engine.NewStore(engine.Dir(ref), nil)
	var refScope string
	x := &engine.Indexer{
		Dir: engine.Dir(ref), Codec: func() (vault.Codec, error) { return testKeyCipher(t), nil },
		Policy: policy.Default(), Log: &strings.Builder{}, Now: func() time.Time { return now },
		Record: func(source, scope string) error {
			refScope = scope
			return refStore.RecordInvocation(source, scope, nil, now)
		},
		Outcome: func(o engine.Outcome) {
			if err := refStore.RecordFound("git", refScope, o); err != nil {
				t.Errorf("RecordFound: %v", err)
			}
		},
	}
	req := engine.IndexRequest{Source: "git", IndexArgs: engine.IndexArgs{RepoPaths: []string{repo}, GitSinceDays: 10000}}
	if err := x.Index(context.Background(), req); err != nil {
		t.Fatalf("engine Index: %v", err)
	}
	refHandle, err := Open(ref, testKeyB64)
	if err != nil {
		t.Fatalf("Open reference: %v", err)
	}
	want, err := refHandle.Status(now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Status(now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("status mismatch against the engine (-want +got):\n%s", diff)
	}
	if !strings.Contains(got, `"people":2`) {
		t.Errorf("status %s, want two people", got)
	}
	if !strings.Contains(got, `"found":{"repos":1,"people":2,"channels":0}`) {
		t.Errorf("status %s, want the git scope to have read 1 repository and found 2 people", got)
	}
	inv, err := h.store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	found := &engine.Outcome{Repos: 1, People: 2}
	wantInv := engine.Invocations{"git": {"repo:" + repo: {
		Flags: []string{"--git-since-days=10000", "--repo-path", repo}, RanAt: now, AttemptedAt: now, Found: found,
	}}}
	if diff := cmp.Diff(wantInv, inv); diff != "" {
		t.Errorf("recorded run mismatch (-want +got):\n%s", diff)
	}
	if _, err := h.Search("ann", 3); err != nil {
		t.Errorf("Search after Index: %v", err)
	}
	later := now.Add(time.Hour)
	raw, err = h.Refresh("", later.Unix())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var rv refreshView
	if err := json.Unmarshal([]byte(raw), &rv); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	// The refresh reads no new commits, yet the source still holds both people.
	wantResults := []refreshResult{{
		Source: "git", Scope: "repo:" + repo, RanAt: later.Format(time.RFC3339), OK: true, Found: found,
	}}
	if diff := cmp.Diff(wantResults, rv.Results); diff != "" || !rv.OK {
		t.Errorf("refresh ok %v, results mismatch (-want +got):\n%s", rv.OK, diff)
	}
	if !strings.Contains(rv.Log, "refreshing git (repo:"+repo+")...") {
		t.Errorf("refresh log does not name the scope:\n%s", rv.Log)
	}
	st, err := h.Status(later.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, `"built_at":"`+later.Format(time.RFC3339)+`"`) {
		t.Errorf("status after refresh %s, want built_at %s from the reloaded index", st, later.Format(time.RFC3339))
	}
	if !strings.Contains(st, `"found":{"repos":1,"people":2,"channels":0}`) {
		t.Errorf("status after refresh %s, want the git scope to keep 1 repository and 2 people", st)
	}
}

// TestStatusScopes verifies Status says when each recorded scope last worked and
// how its last try went, in source then scope order, and leaves out a time it
// does not know.
func TestStatusScopes(t *testing.T) {
	t.Parallel()
	repo := newGitRepo(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := h.Index("git", "--repo-path\n"+repo+"\n--git-since-days=10000\n", now.Unix()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	failedAt := now.Add(time.Hour)
	if _, err := h.Refresh("", failedAt.Unix()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	store := h.store
	inv, err := store.LoadInvocations()
	if err != nil {
		t.Fatal(err)
	}
	inv["slack"] = map[string]engine.Run{"": {}}
	if err := store.SaveInvocations(inv); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Status(failedAt.Add(time.Hour).Unix())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got statusView
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	failure := inv["git"]["repo:"+repo].Error
	if failure == "" {
		t.Error("the failed refresh recorded no error")
	}
	want := []scopeView{{
		Source: "git", Scope: "repo:" + repo, RanAt: now.Format(time.RFC3339), AgeSeconds: 7200,
		AttemptedAt: failedAt.Format(time.RFC3339), Error: failure, Found: &engine.Outcome{Repos: 1, People: 2},
	}, {Source: "slack"}}
	if diff := cmp.Diff(want, got.Scopes); diff != "" {
		t.Errorf("scopes mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(raw[strings.Index(raw, `"source":"slack"`):], "ran_at") {
		t.Errorf("a scope with no known time carries ran_at: %s", raw)
	}
	early, err := h.Status(now.Add(-time.Hour).Unix())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(early, `"scope":"repo:`+repo+`","ran_at":"`+now.Format(time.RFC3339)+`","age_seconds":0`) {
		t.Errorf("a clock set back gave a negative age: %s", early)
	}
}

// TestStatusNoIndexScopes verifies a directory with no index still answers with an
// empty scope list, and a damaged refresh config is an error rather than no scopes.
func TestStatusNoIndexScopes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	raw, err := h.Status(0)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(raw, `"scopes":[]`) {
		t.Errorf("status %s, want an empty scope list", raw)
	}
	if err := os.WriteFile(engine.Dir(dir).Refresh(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Status(0); err == nil {
		t.Error("Status over a damaged refresh config returned nil")
	}
}

// TestRefreshNothingRecorded verifies a refresh with nothing recorded says so in
// words a host can show as they are.
func TestRefreshNothingRecorded(t *testing.T) {
	t.Parallel()
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = h.Refresh("", 100)
	if !errors.Is(err, engine.ErrNothingRecorded) || err.Error() != engine.ErrNothingRecorded.Error() {
		t.Errorf("Refresh = %v, want only %v", err, engine.ErrNothingRecorded)
	}
}

// TestImportSample verifies the sample company loads sealed under the handle's key,
// reads as sample data, blocks real sources until a wipe, and never replaces real data.
func TestImportSample(t *testing.T) {
	t.Parallel()
	bundled := t.TempDir()
	copySample(t, engine.Dir(newTestDataDir(t, nil)), bundled)
	if err := os.Chmod(bundled, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bundled, 0o700) })
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "data")
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	raw, err := h.ImportSample(bundled, now.Unix())
	if err != nil {
		t.Fatalf("ImportSample: %v", err)
	}
	var got sampleView
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	want := sampleView{People: 12, Channels: 4, Sources: 8, LoadedAt: now.Format(time.RFC3339)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("import mismatch (-want +got):\n%s", diff)
	}
	ans, err := h.Ask("terraform", 3)
	if err != nil || !strings.Contains(ans, "Carol Lee") {
		t.Errorf("Ask after import = %s, %v; want Carol Lee", ans, err)
	}
	for _, path := range []string{engine.Dir(dir).Index(), engine.Dir(dir).Episodes(), engine.Dir(dir).Changes()} {
		stored, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stored), "Carol") {
			t.Errorf("%s holds a name in plain text under a key", path)
		}
	}
	if _, err := os.Stat(engine.Dir(dir).Sources()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("import wrote a sources sidecar: %v", err)
	}

	later := now.Add(3 * time.Minute)
	raw, err = h.Status(later.Unix())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var st statusView
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if !st.Sample || st.LoadedAt != now.Format(time.RFC3339) || st.AgeSeconds != 180 {
		t.Errorf("status sample %v, loaded %q, age %d; want true, %s, 180", st.Sample, st.LoadedAt, st.AgeSeconds, now)
	}
	if len(st.Scopes) != 8 || st.Scopes[0] != (scopeView{
		Source: "codeowners", Scope: "sample", RanAt: now.Format(time.RFC3339), AgeSeconds: 180,
	}) {
		t.Errorf("status scopes = %+v, want eight sample scopes", st.Scopes)
	}

	if _, err := h.Index("org-csv", "--file=x.csv", later.Unix()); !errors.Is(err, engine.ErrSample) {
		t.Errorf("Index over sample = %v, want ErrSample", err)
	}
	if _, err := h.Refresh("", later.Unix()); !errors.Is(err, engine.ErrSample) {
		t.Errorf("Refresh over sample = %v, want ErrSample", err)
	}

	before, err := h.ChangesSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, `"entries":1`) {
		t.Errorf("changes after import = %s, want one entry", before)
	}
	if _, err := h.ImportSample(bundled, later.Unix()); err != nil {
		t.Fatalf("second ImportSample: %v", err)
	}
	if after, err := h.ChangesSince(0); err != nil || after != before {
		t.Errorf("a second import logged changes: %s, %v; want %s", after, err, before)
	}

	wiped, err := Wipe(dir)
	if err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	if !strings.Contains(wiped, "sample.json") {
		t.Errorf("wipe %s does not name the marker", wiped)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("wipe left the directory: %v", err)
	}
}

// TestImportSampleRefused verifies a load over real data or from a missing sample
// fails and changes nothing, and a real index reads as no sample.
func TestImportSampleRefused(t *testing.T) {
	t.Parallel()
	bundled := t.TempDir()
	copySample(t, engine.Dir(newTestDataDir(t, nil)), bundled)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		Want     error
		WantText string
		Real     bool
		From     string
	}{{ // Test 0: Real data stays.
		Real: true, From: bundled, Want: engine.ErrRealIndex,
	}, { // Test 1: A sample that is not there leaves no marker behind.
		From: filepath.Join(bundled, "missing"), WantText: "core: load sample: ",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			h, err := Open(dir, "")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if test.Real {
				if _, err := h.Index("git", "--repo-path\n"+newGitRepo(t)+"\n--git-since-days=10000\n", now.Unix()); err != nil {
					t.Fatalf("Index: %v", err)
				}
			}
			_, err = h.ImportSample(test.From, now.Unix())
			if test.Want != nil && !errors.Is(err, test.Want) {
				t.Errorf("ImportSample = %v, want %v", err, test.Want)
			}
			if err == nil || !strings.Contains(err.Error(), test.WantText) {
				t.Errorf("ImportSample = %v, want text %q", err, test.WantText)
			}
			if _, err := os.Stat(engine.Dir(dir).Sample()); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused import left the marker: %v", err)
			}
			st, err := h.Status(now.Unix())
			if err != nil || strings.Contains(st, `"sample":true`) {
				t.Errorf("status %s, %v; want no sample", st, err)
			}
		})
	}
}

// TestImportSampleCutShort verifies an import that stops partway still reads as
// sample data, so it can never pass for a real index, and a second load finishes it.
func TestImportSampleCutShort(t *testing.T) {
	t.Parallel()
	bundled := t.TempDir()
	copySample(t, engine.Dir(newTestDataDir(t, nil)), bundled)
	dir := filepath.Join(t.TempDir(), "data")
	h, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.MkdirAll(engine.Dir(dir).Episodes(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ImportSample(bundled, 100); err == nil {
		t.Fatal("ImportSample with no way to save conversations returned nil")
	}
	st, err := h.Status(100)
	if err != nil || !strings.Contains(st, `"sample":true`) {
		t.Errorf("status %s, %v; want sample", st, err)
	}
	if err := os.Remove(engine.Dir(dir).Episodes()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ImportSample(bundled, 200); err != nil {
		t.Errorf("ImportSample after the fault cleared: %v", err)
	}
}

// TestHandleLostRepo verifies a recorded repository the app can no longer read
// fails its scope instead of passing as an empty refresh, and leaves the index
// it already had untouched.
func TestHandleLostRepo(t *testing.T) {
	t.Parallel()
	repo := newGitRepo(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	h, err := Open(dir, testKeyB64)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := h.Index("git", "--repo-path\n"+repo+"\n--git-since-days=10000\n", now.Unix()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	before, err := os.ReadFile(engine.Dir(dir).Index())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	raw, err := h.Refresh("", later.Unix())
	if err != nil {
		t.Fatalf("Refresh = %v, want the failure reported per scope", err)
	}
	var rv refreshView
	if err := json.Unmarshal([]byte(raw), &rv); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if rv.OK || len(rv.Results) != 1 || rv.Results[0].OK ||
		!strings.Contains(rv.Results[0].Error, "none of the repository paths could be read") {
		t.Errorf("refresh = %s, want the git scope failed as unreadable", raw)
	}
	_, err = h.Index("git", "--repo-path\n"+repo+"\n--git-since-days=10000\n", later.Unix())
	if !errors.Is(err, connector.ErrNoReadableRepo) {
		t.Errorf("Index error = %v, want ErrNoReadableRepo", err)
	}
	after, err := os.ReadFile(engine.Dir(dir).Index())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a failed scope rewrote the index")
	}
	st, err := h.Status(later.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, `"people":2`) || !strings.Contains(st, `"built_at":"`+now.Format(time.RFC3339)+`"`) {
		t.Errorf("status after the failed refresh %s, want the original two-person index", st)
	}
}

// TestHandleIndexRefuses verifies flag lists the core cannot run are refused
// before anything is read or written.
func TestHandleIndexRefuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want    error
		Source  string
		Flags   string
		WantMsg string
	}{{ // Test 0: A policy flag, since the core is strict only.
		Source: "org-csv", Flags: "--file=x.csv\n--policy=open", Want: engine.ErrBadArgs, WantMsg: "strict policy",
	}, { // Test 1: A source reading standard input with no file.
		Source: "json", Want: engine.ErrBadArgs, WantMsg: "standard input",
	}, { // Test 2: A source reading standard input from a dash.
		Source: "json", Flags: "--file=-", Want: engine.ErrBadArgs, WantMsg: "standard input",
	}, { // Test 3: A flag the index does not take.
		Source: "org-csv", Flags: "--data-dir=/tmp", Want: engine.ErrBadArgs, WantMsg: "unknown flag",
	}, { // Test 4: A bare argument.
		Source: "org-csv", Flags: "people.csv", Want: engine.ErrBadArgs, WantMsg: "unexpected argument",
	}, { // Test 5: A space-separated list is one argument, not three.
		Source: "org-csv", Flags: "--file x.csv --merge", Want: engine.ErrBadArgs, WantMsg: "flags",
	}, { // Test 6: An unknown source.
		Source: "nosuch", Want: engine.ErrUnknownSource, WantMsg: "nosuch",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			h, err := Open(dir, "")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_, err = h.Index(test.Source, test.Flags, 0)
			if !errors.Is(err, test.Want) || !strings.Contains(fmt.Sprint(err), test.WantMsg) {
				t.Errorf("Index error = %v, want %v containing %q", err, test.Want, test.WantMsg)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a refused index wrote %d files", len(entries))
			}
		})
	}
}

// authJira serves a fake Jira Server site that answers only the bearer token
// good, so a run can be made with a right and a wrong token.
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

// TestHandleSecretsReachFetch verifies a token the host's Secrets holds is the
// one a source fetches with: the right token indexes Jira, a wrong one fails
// the index with the auth error, and a token gone stale between runs fails
// that scope of a refresh by name while the answer still comes back.
func TestHandleSecretsReachFetch(t *testing.T) {
	t.Parallel()
	jira := authJira(t)
	flags := "--jira-server\n--jira-url=" + jira + "\n--jira-project\nKAFKA"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
	tests := []struct {
		Token        string
		RefreshToken string
		WantIndexErr error
		WantOK       bool
	}{{ // Test 0: The right token indexes and refreshes.
		Token: "good", RefreshToken: "good", WantOK: true,
	}, { // Test 1: A wrong token fails the index with the auth error.
		Token: "wrong", WantIndexErr: engine.ErrAuth,
	}, { // Test 2: A token that went bad fails the refresh scope, not the call.
		Token: "good", RefreshToken: "wrong",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			h, err := Open(t.TempDir(), "")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: test.Token})
			_, err = h.Index("jira", flags, now)
			if !errors.Is(err, test.WantIndexErr) {
				t.Fatalf("Index error = %v, want %v", err, test.WantIndexErr)
			}
			if err != nil {
				return
			}
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: test.RefreshToken})
			raw, err := h.Refresh("jira", now+3600)
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			var rv refreshView
			if err := json.Unmarshal([]byte(raw), &rv); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			if rv.OK != test.WantOK || len(rv.Results) != 1 || rv.Results[0].OK != test.WantOK {
				t.Fatalf("refresh = %s, want ok %v on one scope", raw, test.WantOK)
			}
			if !test.WantOK && !strings.Contains(rv.Results[0].Error, "401") &&
				!strings.Contains(rv.Results[0].Error, "token") {
				t.Errorf("refresh scope error %q does not name the auth failure", rv.Results[0].Error)
			}
			if !test.WantOK && !strings.Contains(rv.Log, "refresh jira (project:KAFKA) failed: ") {
				t.Errorf("refresh log does not name the failed scope:\n%s", rv.Log)
			}
		})
	}
}

// TestHandleRefreshErrors verifies Refresh fails as a call only when nothing
// could run.
func TestHandleRefreshErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want    error
		Sources string
		WantMsg string
	}{{ // Test 0: Nothing recorded.
		Want: engine.ErrNothingRecorded, WantMsg: "nothing to refresh",
	}, { // Test 1: A source name the registry does not know.
		Sources: "org-csv, nosuch", Want: engine.ErrUnknownSource, WantMsg: "nosuch",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			h, err := Open(t.TempDir(), "")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_, err = h.Refresh(test.Sources, 0)
			if !errors.Is(err, test.Want) || !strings.Contains(fmt.Sprint(err), test.WantMsg) {
				t.Errorf("Refresh error = %v, want %v containing %q", err, test.Want, test.WantMsg)
			}
		})
	}
}

// TestHandleAskDuringRefresh verifies questions asked while a refresh runs
// answer without a race, from the index as it was or as it became.
func TestHandleAskDuringRefresh(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := h.Index("org-csv", "--file="+people, 0); err != nil {
		t.Fatalf("Index: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 5 {
			if _, err := h.Refresh("", int64(i+1)*3600); err != nil {
				t.Errorf("Refresh: %v", err)
				return
			}
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		if _, err := h.Search("a", 3); err != nil {
			t.Fatalf("Search during refresh: %v", err)
		}
		if _, err := h.Status(0); err != nil {
			t.Fatalf("Status during refresh: %v", err)
		}
	}
}

// TestWipe verifies a wipe removes every file an index wrote and the data
// directory with them, needs no key, and refuses a symlink without removing
// anything.
func TestWipe(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		Symlink     bool
		WantMsg     string
		WantRemoved int
	}{{ // Test 0: Everything an index wrote goes, then the directory.
		WantRemoved: 5,
	}, { // Test 1: A symlink at a kept path stops the wipe.
		Symlink: true, WantMsg: "not a regular file",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "data")
			h, err := Open(dir, testKeyB64)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := h.Index("org-csv", "--file="+people, 0); err != nil {
				t.Fatalf("Index: %v", err)
			}
			if test.Symlink {
				if err := os.Symlink(people, engine.Dir(dir).Facts()); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(dir)
			raw, err := Wipe(dir)
			if test.WantMsg != "" {
				if err == nil || !strings.Contains(err.Error(), test.WantMsg) {
					t.Fatalf("Wipe error = %v, want one containing %q", err, test.WantMsg)
				}
				if after, _ := os.ReadDir(dir); len(after) != len(before) {
					t.Errorf("a refused wipe removed %d files", len(before)-len(after))
				}
				return
			}
			if err != nil {
				t.Fatalf("Wipe: %v", err)
			}
			var got wipeView
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			if len(got.Removed) != test.WantRemoved || len(got.Removed) != len(before) {
				t.Errorf("removed %v, want %d files (the directory held %d)", got.Removed, test.WantRemoved, len(before))
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the data directory is still there: %v", err)
			}
		})
	}
	if _, err := Wipe(" "); err == nil {
		t.Error("Wipe of an empty path succeeded")
	}
}

// panicSecrets is a Secrets whose every read panics, standing in for a bug below a call.
type panicSecrets struct{}

// Get panics.
func (panicSecrets) Get(string) string { panic("secret read blew up") }

// panicCodec is a Codec whose every use panics, standing in for a bug below a read or write.
type panicCodec struct{}

// Encode panics.
func (panicCodec) Encode([]byte) ([]byte, error) { panic("encode blew up") }

// Decode panics.
func (panicCodec) Decode([]byte) ([]byte, error) { panic("decode blew up") }

// TestPanicBecomesError verifies a panic under each exported call comes back as an error
// naming the call, with no answer, and never reaches the host.
func TestPanicBecomesError(t *testing.T) {
	t.Parallel()
	const now = int64(1_800_000_000)
	noEngine := func(t *testing.T) *Handle {
		t.Helper()
		return &Handle{store: engine.NewStore(engine.Dir(t.TempDir()), nil), loaded: &loaded{}}
	}
	badCodec := func(t *testing.T) *Handle {
		t.Helper()
		dir := engine.Dir(t.TempDir())
		if err := os.WriteFile(dir.Changes(), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		return &Handle{store: engine.NewStore(dir, panicCodec{})}
	}
	tests := []struct {
		Handle  func(*testing.T) *Handle
		Call    func(*testing.T, *Handle) (string, error)
		WantMsg string
	}{{ // Test 0: Ask.
		Handle: noEngine, WantMsg: "ask: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.Ask("terraform", 1) },
	}, { // Test 1: Search.
		Handle: noEngine, WantMsg: "search: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.Search("terraform", 1) },
	}, { // Test 2: Person.
		Handle: noEngine, WantMsg: "person: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.Person("a@example.com") },
	}, { // Test 3: Recall.
		Handle: noEngine, WantMsg: "recall: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.Recall("a@example.com", "x", 1) },
	}, { // Test 4: Status.
		Handle: noEngine, WantMsg: "status: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.Status(now) },
	}, { // Test 5: ChangesSince.
		Handle: badCodec, WantMsg: "changes: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.ChangesSince(0) },
	}, { // Test 6: ImportSample.
		Handle: badCodec, WantMsg: "load sample: ",
		Call: func(t *testing.T, h *Handle) (string, error) {
			t.Helper()
			bundled := t.TempDir()
			copySample(t, engine.Dir(newTestDataDir(t, nil)), bundled)
			return h.ImportSample(bundled, now)
		},
	}, { // Test 7: People.
		Handle: noEngine, WantMsg: "people: ",
		Call: func(_ *testing.T, h *Handle) (string, error) { return h.People(0) },
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			out, err := test.Call(t, test.Handle(t))
			if !errors.Is(err, errPanic) || !strings.Contains(err.Error(), test.WantMsg) {
				t.Errorf("error = %v, want %v naming %q", err, errPanic, test.WantMsg)
			}
			if out != "" {
				t.Errorf("answer = %q, want none", out)
			}
		})
	}
}

// TestPanicInWriteLeavesHandleUsable verifies a Secrets that panics during Index and
// Refresh fails those calls and holds no lock, so the next write on the handle runs.
func TestPanicInWriteLeavesHandleUsable(t *testing.T) {
	t.Parallel()
	flags := "--jira-server\n--jira-url=" + authJira(t) + "\n--jira-project\nKAFKA"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.SetSecrets(panicSecrets{})
	if _, err := h.Index("jira", flags, now); !errors.Is(err, errPanic) || !strings.Contains(err.Error(), "secret read blew up") {
		t.Errorf("Index error = %v, want %v naming the panic", err, errPanic)
	}
	h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
	if _, err := h.Index("jira", flags, now); err != nil {
		t.Fatalf("Index after a panic: %v", err)
	}
	h.SetSecrets(panicSecrets{})
	if _, err := h.Refresh("jira", now+60); !errors.Is(err, errPanic) {
		t.Errorf("Refresh error = %v, want %v", err, errPanic)
	}
	h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
	raw, err := h.Refresh("jira", now+120)
	if err != nil || !strings.Contains(raw, `"ok":true`) {
		t.Errorf("Refresh after a panic = %s, %v; want ok", raw, err)
	}
	if _, err := h.Ask("rebalance", 1); err != nil {
		t.Errorf("Ask after a panic: %v", err)
	}
}

// TestEveryExportedCallRescues verifies each exported function in core.go that can return an
// error opens with defer rescue, so a new call cannot leave a way for a panic to reach the
// host. Open and Wipe have no seam to panic under, so this is what covers them.
func TestEveryExportedCallRescues(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "core.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !fn.Name.IsExported() || fn.Type.Results == nil || !onHandle(fn) {
			continue
		}
		last := fn.Type.Results.List[len(fn.Type.Results.List)-1]
		if id, ok := last.Type.(*ast.Ident); !ok || id.Name != "error" {
			continue
		}
		got = append(got, fn.Name.Name)
		if !opensWithRescue(fn) {
			t.Errorf("%s does not open with defer rescue", fn.Name.Name)
		}
	}
	want := []string{"Open", "Ask", "Search", "People", "Person", "Recall", "Status", "ChangesSince", "Index", "Refresh",
		"ForgetScopes", "ImportSample", "Wipe"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("exported calls mismatch (-want +got):\n%s", diff)
	}
}

// onHandle reports whether fn crosses to the host: a plain function, or a method on Handle
// and not on a type gomobile never binds.
func onHandle(fn *ast.FuncDecl) bool {
	if fn.Recv == nil {
		return true
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Handle"
}

// opensWithRescue reports whether fn's first statement is a deferred call to rescue.
func opensWithRescue(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	deferred, ok := fn.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	id, ok := deferred.Call.Fun.(*ast.Ident)
	return ok && id.Name == "rescue"
}

// TestKeyedDataDirHoldsNoPlainScope verifies a planted project and site address appear in
// no file of a keyed data directory, through an index, a refresh that fails and records its
// error, and one that succeeds. The same run with no key finds them, so the scan can fail.
func TestKeyedDataDirHoldsNoPlainScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Key       string
		WantPlain bool
	}{{ // Test 0: Under a key nothing shows.
		Key: testKeyB64,
	}, { // Test 1: With no key the scan finds the same strings.
		WantPlain: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			jira := authJira(t)
			dir := filepath.Join(t.TempDir(), "data")
			flags := "--jira-server\n--jira-url=" + jira + "\n--jira-project\nKAFKA"
			now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
			h, err := Open(dir, test.Key)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
			if _, err := h.Index("jira", flags, now); err != nil {
				t.Fatalf("Index: %v", err)
			}
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "wrong"})
			if _, err := h.Refresh("jira", now+60); err != nil {
				t.Fatalf("failing Refresh: %v", err)
			}
			scanFor(t, dir, test.WantPlain, "KAFKA", strings.TrimPrefix(jira, "http://"))
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
			if _, err := h.Refresh("jira", now+120); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			scanFor(t, dir, test.WantPlain, "KAFKA", strings.TrimPrefix(jira, "http://"))
			for _, path := range []string{engine.Dir(dir).Refresh(), engine.Dir(dir).State()} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("the run left no %s to scan: %v", filepath.Base(path), err)
				}
			}
			reopened, err := Open(dir, test.Key)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			raw, err := reopened.Status(now + 180)
			if err != nil || !strings.Contains(raw, `"scope":"project:KAFKA"`) {
				t.Errorf("Status after reopening = %s, %v; want the scope", raw, err)
			}
		})
	}
}

// scanFor reads every file under dir and fails when one of the planted strings shows and
// wantFound is false, or when none shows and wantFound is true.
func scanFor(t *testing.T, dir string, wantFound bool, planted ...string) {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range planted {
			if bytes.Contains(raw, []byte(s)) {
				found = true
				if !wantFound {
					t.Errorf("%s holds %q in plain text under a key", filepath.Base(path), s)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if wantFound && !found {
		t.Errorf("no file under %s holds any of %q", dir, planted)
	}
}

// heardLines is a Progress that keeps what it hears.
type heardLines struct {
	// mu guards lines.
	mu sync.Mutex
	// lines are the lines heard, in order.
	lines []string
}

// Line keeps text.
func (l *heardLines) Line(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, text)
}

// got returns the lines heard so far.
func (l *heardLines) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// TestTailLogLines verifies a log tells its Progress each complete line once, in order,
// however the writes split them.
func TestTailLogLines(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxLog+1)
	tests := []struct {
		Writes    []string
		WantLines []string
	}{{ // Test 0: One line in one write.
		Writes: []string{"one\n"}, WantLines: []string{"one"},
	}, { // Test 1: A line split across writes is told once, whole.
		Writes: []string{"on", "e\ntw", "o\n"}, WantLines: []string{"one", "two"},
	}, { // Test 2: Many lines in one write keep their order.
		Writes: []string{"one\ntwo\nthree\n"}, WantLines: []string{"one", "two", "three"},
	}, { // Test 3: Blank lines are skipped and space is trimmed.
		Writes: []string{"\n  \n  one  \r\n\n"}, WantLines: []string{"one"},
	}, { // Test 4: A line with no ending yet is not told.
		Writes: []string{"one\ntw"}, WantLines: []string{"one"},
	}, { // Test 5: A line past the cap is dropped and the next one is whole.
		Writes: []string{long, "\nnext\n"}, WantLines: []string{"next"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			heard := &heardLines{}
			log := &tailLog{max: maxLog, progress: heard}
			for _, w := range test.Writes {
				if n, err := log.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if diff := cmp.Diff(test.WantLines, heard.got(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("lines mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestProgressHearsIndex verifies Progress hears the lines of a real index in the order
// the answer's log holds them, and a nil Progress is safe.
func TestProgressHearsIndex(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	heard := &heardLines{}
	h.SetProgress(heard)
	raw, err := h.Index("org-csv", "--file="+people, 0)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	var v indexView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	var want []string
	for line := range strings.Lines(v.Log) {
		if s := strings.TrimSpace(line); s != "" {
			want = append(want, s)
		}
	}
	if len(want) == 0 {
		t.Fatalf("index logged nothing: %s", raw)
	}
	if diff := cmp.Diff(want, heard.got()); diff != "" {
		t.Errorf("lines mismatch (-want +got):\n%s", diff)
	}
	h.SetProgress(nil)
	if _, err := h.Index("org-csv", "--file="+people, 60); err != nil {
		t.Errorf("Index with no Progress: %v", err)
	}
}

// stallingJira serves a fake Jira Server site that, once stall is set, holds every request
// until the client goes away, and closes reached when the first one arrives.
func stallingJira(t *testing.T) (site string, stall *atomic.Bool, reached <-chan struct{}) {
	t.Helper()
	target, err := url.Parse(authJira(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	stall = &atomic.Bool{}
	arrived := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stall.Load() {
			proxy.ServeHTTP(w, r)
			return
		}
		once.Do(func() { close(arrived) })
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv.URL, stall, arrived
}

// finishes returns the result of run, failing the test if it takes more than a few seconds.
func finishes(t *testing.T, run <-chan error) error {
	t.Helper()
	select {
	case err := <-run:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not stop on Cancel")
		return nil
	}
}

// TestCancelIndex verifies Cancel stops an index stuck on a server: it returns ErrCanceled
// within seconds with no index and no recorded run, and the next run is not canceled.
func TestCancelIndex(t *testing.T) {
	t.Parallel()
	site, stall, reached := stallingJira(t)
	dir := t.TempDir()
	h, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
	flags := "--jira-server\n--jira-url=" + site + "\n--jira-project\nKAFKA"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
	stall.Store(true)
	run := make(chan error, 1)
	go func() {
		_, err := h.Index("jira", flags, now)
		run <- err
	}()
	<-reached
	h.Cancel()
	if err := finishes(t, run); !errors.Is(err, engine.ErrCanceled) {
		t.Errorf("Index error = %v, want %v", err, engine.ErrCanceled)
	}
	for _, file := range engine.Dir(dir).Files() {
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a canceled index left %s: %v", filepath.Base(file), err)
		}
	}
	stall.Store(false)
	if _, err := h.Index("jira", flags, now); err != nil {
		t.Errorf("Index after a cancel: %v", err)
	}
}

// TestCancelRefresh verifies a cancel records no attempt for the scope it stopped, keeps what
// finished, and says which of the two it was: a run with nothing finished is ErrCanceled.
func TestCancelRefresh(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
	const later = 3600
	tests := []struct {
		WantLine     string
		Want         error
		WithGit      bool
		WantFinished int
	}{{ // Test 0: The only scope stops, so nothing finished.
		Want: engine.ErrCanceled, WantLine: "refreshing jira (project:KAFKA)...",
	}, { // Test 1: Git sorts first and finishes, then jira stops.
		WithGit: true, WantFinished: 1, WantLine: "refreshing jira (project:KAFKA)...",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			site, stall, reached := stallingJira(t)
			h, err := Open(t.TempDir(), "")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			h.SetSecrets(mapSecrets{engine.JiraTokenEnv: "good"})
			if test.WithGit {
				if _, err := h.Index("git", "--repo-path="+newGitRepo(t), start); err != nil {
					t.Fatalf("Index git: %v", err)
				}
			}
			flags := "--jira-server\n--jira-url=" + site + "\n--jira-project\nKAFKA"
			if _, err := h.Index("jira", flags, start); err != nil {
				t.Fatalf("Index jira: %v", err)
			}
			heard := &heardLines{}
			h.SetProgress(heard)
			stall.Store(true)
			var raw string
			run := make(chan error, 1)
			go func() {
				var err error
				raw, err = h.Refresh("", start+later)
				run <- err
			}()
			<-reached
			if !slices.Contains(heard.got(), test.WantLine) {
				t.Errorf("no live line %q before the cancel: %q", test.WantLine, heard.got())
			}
			h.Cancel()
			err = finishes(t, run)
			if !errors.Is(err, test.Want) {
				t.Fatalf("Refresh error = %v, want %v", err, test.Want)
			}
			if test.Want == nil {
				var rv refreshView
				if err := json.Unmarshal([]byte(raw), &rv); err != nil {
					t.Fatalf("unmarshal %s: %v", raw, err)
				}
				if !rv.Canceled || rv.OK || len(rv.Results) != test.WantFinished {
					t.Errorf("refresh = %s, want canceled and %d finished", raw, test.WantFinished)
				}
			}
			inv, err := h.store.LoadInvocations()
			if err != nil {
				t.Fatalf("LoadInvocations: %v", err)
			}
			got := inv["jira"]["project:KAFKA"]
			if got.Error != "" || got.AttemptedAt.Unix() != start || got.RanAt.Unix() != start {
				t.Errorf("stopped scope wrote an attempt: %+v", got)
			}
			if test.WithGit {
				for _, g := range inv["git"] {
					if g.RanAt.Unix() != start+later {
						t.Errorf("finished scope RanAt = %v, want the refresh time", g.RanAt)
					}
				}
			}
		})
	}
}

// TestCancelWithNoRun verifies Cancel with nothing running does nothing and does not reach
// the next run.
func TestCancelWithNoRun(t *testing.T) {
	t.Parallel()
	people, err := filepath.Abs("../examples/people.csv")
	if err != nil {
		t.Fatal(err)
	}
	var none *Handle
	none.Cancel()
	none.SetProgress(nil)
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.Cancel()
	if _, err := h.Index("org-csv", "--file="+people, 0); err != nil {
		t.Errorf("Index after a stale cancel: %v", err)
	}
	h.Cancel()
	raw, err := h.Refresh("", 60)
	if err != nil {
		t.Fatalf("Refresh after a stale cancel: %v", err)
	}
	if strings.Contains(raw, `"canceled"`) {
		t.Errorf("a stale cancel reached the refresh: %s", raw)
	}
}

// TestForgetScopes verifies a forgotten scope drops out of Refresh, keep holds on to the one
// named, and the last scope leaving means nothing to refresh, with the index still answering.
func TestForgetScopes(t *testing.T) {
	t.Parallel()
	kept, moved := newGitRepo(t), newGitRepo(t)
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const now = int64(1_800_000_000)
	for _, repo := range []string{kept, moved} {
		if _, err := h.Index("git", "--repo-path\n"+repo+"\n--git-since-days=10000", now); err != nil {
			t.Fatalf("Index %s: %v", repo, err)
		}
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Refresh("git", now+60)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("refresh with a lost repo = %s, want a failure", raw)
	}
	if err := h.ForgetScopes("git", "repo:"+kept); err != nil {
		t.Fatalf("ForgetScopes keep: %v", err)
	}
	raw, err = h.Refresh("git", now+120)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var rv refreshView
	if err := json.Unmarshal([]byte(raw), &rv); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if !rv.OK || len(rv.Results) != 1 || rv.Results[0].Scope != "repo:"+kept {
		t.Errorf("refresh after forgetting the lost repo = %s, want only the kept one, ok", raw)
	}
	if err := h.ForgetScopes("git", ""); err != nil {
		t.Fatalf("ForgetScopes all: %v", err)
	}
	if _, err := h.Refresh("", now+180); !errors.Is(err, engine.ErrNothingRecorded) {
		t.Errorf("Refresh with every scope forgotten = %v, want %v", err, engine.ErrNothingRecorded)
	}
	if raw, err := h.Status(now + 180); err != nil || !strings.Contains(raw, `"people":`) || strings.Contains(raw, `"people":0`) {
		t.Errorf("forgetting a scope touched the index: %v: %s", err, raw)
	}
	var none *Handle
	if err := none.ForgetScopes("git", ""); !errors.Is(err, errNotOpen) {
		t.Errorf("ForgetScopes on no handle = %v, want %v", err, errNotOpen)
	}
}

// TestForgetScopesRescues verifies a panic while forgetting comes back as an error.
func TestForgetScopesRescues(t *testing.T) {
	t.Parallel()
	dir := engine.Dir(t.TempDir())
	if err := os.WriteFile(dir.Refresh(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handle{store: engine.NewStore(dir, panicCodec{})}
	if err := h.ForgetScopes("git", ""); !errors.Is(err, errPanic) || !strings.Contains(err.Error(), "forget scopes: ") {
		t.Errorf("error = %v, want %v naming forget scopes", err, errPanic)
	}
}

// TestHandlePeople verifies People lists everyone sorted by name with the team's
// name, honors a limit, and needs an index.
func TestHandlePeople(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	csv := filepath.Join(t.TempDir(), "people.csv")
	roster := "name,email,title,team,topics\n" +
		"Zoe Park,zoe@x.com,Engineer,,retries\n" +
		"ann lee,ann@x.com,,,ledger\n" +
		"Bob Ray,bob@x.com,Staff Engineer,Billing,billing\n"
	if err := os.WriteFile(csv, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := h.People(0); err == nil {
		t.Error("People with no index returned nil")
	}
	if _, err := h.Index("org-csv", "--file="+csv, now.Unix()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	tests := []struct {
		WantNames []string
		WantTeams []string
		Limit     int
	}{{ // Test 0: Everyone, sorted by name without regard to case.
		Limit: 0, WantNames: []string{"ann lee", "Bob Ray", "Zoe Park"}, WantTeams: []string{"", "Billing", ""},
	}, { // Test 1: A limit keeps the first by name.
		Limit: 2, WantNames: []string{"ann lee", "Bob Ray"}, WantTeams: []string{"", "Billing"},
	}, { // Test 2: A limit past the count keeps everyone.
		Limit: 10, WantNames: []string{"ann lee", "Bob Ray", "Zoe Park"}, WantTeams: []string{"", "Billing", ""},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			raw, err := h.People(test.Limit)
			if err != nil {
				t.Fatalf("People: %v", err)
			}
			var got peopleView
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			var names, teams []string
			for _, p := range got.People {
				names, teams = append(names, p.Name), append(teams, p.Team)
				if p.ID == "" || p.Links == nil {
					t.Errorf("person %+v lacks an id or a links list", p)
				}
			}
			if diff := cmp.Diff(test.WantNames, names); diff != "" {
				t.Errorf("names mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantTeams, teams); diff != "" {
				t.Errorf("teams mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestHandlePeopleSample verifies the sample company lists in full, with titles,
// teams, and the links the search opens.
func TestHandlePeopleSample(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, nil), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	raw, err := h.People(0)
	if err != nil {
		t.Fatalf("People: %v", err)
	}
	var got peopleView
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	status, err := h.Status(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	var st statusView
	if err := json.Unmarshal([]byte(status), &st); err != nil {
		t.Fatal(err)
	}
	if len(got.People) != st.People {
		t.Errorf("People listed %d, Status counts %d", len(got.People), st.People)
	}
	titled, teamed := 0, 0
	for _, p := range got.People {
		if p.Title != "" {
			titled++
		}
		if p.Team != "" {
			teamed++
		}
	}
	if titled == 0 || teamed == 0 {
		t.Errorf("sample lists %d with titles and %d with teams, want some of each", titled, teamed)
	}
}
