package connector

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// maxFolderRepos caps how many repositories one folder of repositories adds, so a pick of a
// whole home directory cannot turn into hours of history reading.
const maxFolderRepos = 100

// expandRepoPaths replaces each path that is a folder of repositories, rather than a repository
// itself, with the repositories directly inside it, sorted by name and capped at
// maxFolderRepos. Only one level is read, so vendored and nested checkouts are never walked. A
// path that is a repository, is missing, or holds no repository stays as given, so the read
// skips it and says why.
func expandRepoPaths(paths []string, log io.Writer) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, path := range paths {
		if isRepo(path) {
			add(path)
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			add(path)
			continue
		}
		var repos []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := filepath.Join(path, e.Name())
			if fi, err := os.Stat(child); err == nil && fi.IsDir() && isRepo(child) {
				repos = append(repos, child)
			}
		}
		if len(repos) == 0 {
			fmt.Fprintf(log, "git: %s is not a repository and holds none directly inside it\n", path)
			add(path)
			continue
		}
		if len(repos) > maxFolderRepos {
			fmt.Fprintf(log, "git: %s holds %d repositories, reading the first %d by name and "+
				"leaving out %d. Pick a narrower folder to read the rest\n",
				path, len(repos), maxFolderRepos, len(repos)-maxFolderRepos)
			repos = repos[:maxFolderRepos]
		}
		fmt.Fprintf(log, "git: %s is a folder of repositories, reading the %d directly inside it\n",
			path, len(repos))
		for _, r := range repos {
			add(r)
		}
	}
	return out
}

// isRepo reports whether path opens as a git repository, by the same rules the read uses.
func isRepo(path string) bool {
	if _, err := dotGit(path); err == nil {
		return true
	}
	_, err := git.PlainOpen(path)
	return err == nil
}
