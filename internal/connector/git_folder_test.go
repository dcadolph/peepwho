package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// initRepoAt makes an empty repository at dir, creating dir first.
func initRepoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := git.PlainInit(dir, false); err != nil {
		t.Fatalf("init %s: %v", dir, err)
	}
}

// TestExpandRepoPaths checks which paths a folder of repositories turns into.
func TestExpandRepoPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "solo")
	initRepoAt(t, repo)
	parent := filepath.Join(root, "parent")
	initRepoAt(t, filepath.Join(parent, "zeta"))
	initRepoAt(t, filepath.Join(parent, "alpha"))
	initRepoAt(t, filepath.Join(parent, ".hidden"))
	initRepoAt(t, filepath.Join(parent, "plain", "nested"))
	if err := os.WriteFile(filepath.Join(parent, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(root, "gone")
	tests := []struct {
		WantPaths []string
		WantLog   string
		In        []string
	}{{ // Test 0: A repository stays as given.
		In: []string{repo}, WantPaths: []string{repo},
	}, { // Test 1: A folder of repositories becomes its direct children by name, without hidden or nested ones.
		In:        []string{parent},
		WantPaths: []string{filepath.Join(parent, "alpha"), filepath.Join(parent, "zeta")},
		WantLog:   "is a folder of repositories, reading the 2 directly inside it",
	}, { // Test 2: A folder with no repository stays, with a line saying so.
		In: []string{empty}, WantPaths: []string{empty},
		WantLog: "is not a repository and holds none directly inside it",
	}, { // Test 3: A missing path stays, so the read skips it.
		In: []string{missing}, WantPaths: []string{missing},
	}, { // Test 4: A repository named twice, once through its folder, is read once.
		In:        []string{filepath.Join(parent, "alpha"), parent},
		WantPaths: []string{filepath.Join(parent, "alpha"), filepath.Join(parent, "zeta")},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var log strings.Builder
			got := expandRepoPaths(test.In, &log)
			if diff := cmp.Diff(test.WantPaths, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("paths mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(log.String(), test.WantLog) {
				t.Errorf("log = %q, want it to contain %q", log.String(), test.WantLog)
			}
		})
	}
}

// TestExpandRepoPathsCaps checks a folder holding more than maxFolderRepos repositories reads
// the first ones by name and says how many it left out.
func TestExpandRepoPathsCaps(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	total := maxFolderRepos + 3
	for i := range total {
		initRepoAt(t, filepath.Join(parent, fmt.Sprintf("repo-%03d", i)))
	}
	var log strings.Builder
	got := expandRepoPaths([]string{parent}, &log)
	if len(got) != maxFolderRepos {
		t.Fatalf("paths = %d, want %d", len(got), maxFolderRepos)
	}
	if want := filepath.Join(parent, fmt.Sprintf("repo-%03d", maxFolderRepos-1)); got[len(got)-1] != want {
		t.Errorf("last path = %s, want %s", got[len(got)-1], want)
	}
	if want := fmt.Sprintf("holds %d repositories, reading the first %d by name and leaving out 3",
		total, maxFolderRepos); !strings.Contains(log.String(), want) {
		t.Errorf("log = %q, want it to contain %q", log.String(), want)
	}
}

// TestGitHistoryReadsAFolderOfRepos checks Fetch over a folder reads every repository inside it.
func TestGitHistoryReadsAFolderOfRepos(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	for name, author := range map[string]string{"one": "ann@corp.com", "two": "ben@corp.com"} {
		if err := os.Rename(newInternalRepo(t, "", author), filepath.Join(parent, name)); err != nil {
			t.Fatalf("move repo: %v", err)
		}
	}
	recs, err := NewGitHistory(GitOptions{Paths: []string{parent}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Email)
	}
	if diff := cmp.Diff([]string{"ann@corp.com", "ben@corp.com"}, got,
		cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("authors mismatch (-want +got):\n%s", diff)
	}
}
