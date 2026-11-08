package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/secret"
)

// errTestNoEntry is the test keychain's not-found error.
var errTestNoEntry = errors.New("not found")

// testKeychain is an in-memory keychain, so no test touches the real one.
type testKeychain struct {
	// data holds stored secrets by name.
	data map[string]string
}

// Get returns the stored value, or errTestNoEntry when absent.
func (k *testKeychain) Get(name string) (string, error) {
	v, ok := k.data[name]
	if !ok {
		return "", errTestNoEntry
	}
	return v, nil
}

// Set stores value under name.
func (k *testKeychain) Set(name, value string) error {
	k.data[name] = value
	return nil
}

// Delete removes name, or returns errTestNoEntry when absent.
func (k *testKeychain) Delete(name string) error {
	if _, ok := k.data[name]; !ok {
		return errTestNoEntry
	}
	delete(k.data, name)
	return nil
}

// newWipeFixture builds a data directory holding every file peepwho writes,
// with a lock and a leftover temporary file, and a keychain holding two
// credentials. It returns the directory and the keychain.
func newWipeFixture(t *testing.T) (string, *testKeychain) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	opts := &options{dataDir: dir}
	paths := append(opts.dir().Files(), opts.indexPath()+".lock", opts.indexPath()+".tmp-123")
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	kc := &testKeychain{data: map[string]string{
		slackTokenEnv: "xoxb-test", githubTokenEnv: "ghp-test", "UNRELATED": "keep",
	}}
	return dir, kc
}

// runWipeCmd runs the wipe command against dir and kc with stdin and args.
func runWipeCmd(t *testing.T, dir string, kc *testKeychain, stdin string, args ...string) (string, error) {
	t.Helper()
	opts := &options{dataDir: dir, keychain: secret.NewWith(kc)}
	cmd := newWipeCmd(opts)
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// dirEntries lists the names in dir, nil when dir does not exist.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestWipe verifies wipe removes every file peepwho writes and every stored
// credential once confirmed, and nothing when declined.
func TestWipe(t *testing.T) {
	t.Parallel()
	all := []string{
		"attest.key", "changes.json", "episodes.json", "facts.json", "feedback.json", "index.json",
		"index.json.lock", "index.json.tmp-123", "index.sources.json", "index.state.json",
		"refresh.json", "sample.json",
	}
	tests := []struct {
		Stdin       string
		Args        []string
		Foreign     bool
		WantFiles   []string
		WantKeys    []string
		WantDirGone bool
		WantOut     string
	}{{ // Test 0: Typing the word wipes files, credentials, and the empty directory.
		Stdin: "wipe\n", WantFiles: nil, WantKeys: []string{"UNRELATED"}, WantDirGone: true,
		WantOut: "wiped 12 files and 2 stored credentials",
	}, { // Test 1: --yes wipes without reading input.
		Args: []string{"--yes"}, WantFiles: nil, WantKeys: []string{"UNRELATED"}, WantDirGone: true,
		WantOut: "wiped 12 files and 2 stored credentials",
	}, { // Test 2: A y answer is not the word and declines.
		Stdin: "y\n", WantFiles: all, WantKeys: []string{"GITHUB", "SLACK", "UNRELATED"},
		WantOut: "left everything as it was",
	}, { // Test 3: Closed input declines.
		Stdin: "", WantFiles: all, WantKeys: []string{"GITHUB", "SLACK", "UNRELATED"},
		WantOut: "left everything as it was",
	}, { // Test 4: A file peepwho did not write survives, and so does its directory.
		Args: []string{"--yes"}, Foreign: true, WantFiles: []string{"notes.txt"},
		WantKeys: []string{"UNRELATED"}, WantOut: "wiped 12 files",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir, kc := newWipeFixture(t)
			if test.Foreign {
				if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600); err != nil {
					t.Fatalf("write foreign: %v", err)
				}
			}
			out, err := runWipeCmd(t, dir, kc, test.Stdin, test.Args...)
			if err != nil {
				t.Fatalf("wipe: %v\n%s", err, out)
			}
			if !strings.Contains(out, test.WantOut) {
				t.Errorf("output lacks %q:\n%s", test.WantOut, out)
			}
			if test.WantDirGone {
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("data dir still exists: %v", err)
				}
			}
			if diff := cmp.Diff(test.WantFiles, dirEntries(t, dir), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("files mismatch (-want +got):\n%s", diff)
			}
			got := make([]string, 0, len(kc.data))
			for name := range kc.data {
				got = append(got, strings.TrimSuffix(strings.TrimPrefix(name, "PEEPWHO_"), "_TOKEN"))
			}
			if diff := cmp.Diff(test.WantKeys, got, cmpopts.SortSlices(func(a, b string) bool {
				return a < b
			})); diff != "" {
				t.Errorf("keychain mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestWipeNothing verifies a wipe with nothing stored says so and creates nothing.
func TestWipeNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "absent")
	out, err := runWipeCmd(t, dir, &testKeychain{data: map[string]string{}}, "")
	if err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if !strings.Contains(out, "nothing to wipe") {
		t.Errorf("output lacks nothing to wipe:\n%s", out)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("wipe created the data dir: %v", err)
	}
}

// TestFindingsCarryNotForLine verifies the human output of risk, attest, and
// assess ends with the not-for line exactly once, and JSON output never has it.
func TestFindingsCarryNotForLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := writeOrgCSV(t, dir)
	if _, stderr, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	tests := []struct {
		Args      []string
		Stderr    bool
		WantCount int
	}{{ // Test 0: Human risk.
		Args: []string{"risk", "--human"}, WantCount: 1,
	}, { // Test 1: Human departure view.
		Args: []string{"risk", "--human", "Jane Roe"}, WantCount: 1,
	}, { // Test 2: Human attest.
		Args: []string{"attest", "--human", "--out", filepath.Join(dir, "finding")}, WantCount: 1,
	}, { // Test 3: Assess reports on stderr.
		Args:   []string{"assess", "--org-csv", csv, "--out", filepath.Join(dir, "assessment")},
		Stderr: true, WantCount: 1,
	}, { // Test 4: JSON risk is unchanged.
		Args: []string{"risk", "--json"}, WantCount: 0,
	}, { // Test 5: JSON attest is unchanged.
		Args: []string{"attest", "--json", "--out", filepath.Join(dir, "finding-json")}, WantCount: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			args := append([]string{"--data-dir", dir}, test.Args...)
			stdout, stderr, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", test.Args, err, stderr)
			}
			got := stdout
			if test.Stderr {
				got = stderr
			}
			if diff := cmp.Diff(test.WantCount, strings.Count(string(got), "performance or employment")); diff != "" {
				t.Errorf("not-for count mismatch (-want +got):\n%s\n%s", diff, got)
			}
		})
	}
}
