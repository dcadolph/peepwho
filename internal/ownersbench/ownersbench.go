// Package ownersbench scores a whotoask index against a repository's own
// human-authored ownership: the OWNERS files projects in the Kubernetes
// ecosystem maintain for real governance. Maintainers wrote those lists for
// their own reasons, reviewed by other maintainers, which makes them the
// closest thing to independent ground truth that exists in public.
//
// The bench measures ownership recovery, not expertise. It reports its result
// against a naive baseline, git's top committers per directory, and splits the
// directories where those two disagree into their own cohort, because a tool
// that only finds the maintainers who are also the top committers is a slower
// git shortlog.
package ownersbench

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Truth is one directory's human-authored ownership, resolved to individuals.
type Truth struct {
	// Dir is the directory relative to the repository root.
	Dir string
	// Approvers are the individual logins named as approvers, aliases expanded.
	Approvers []string
}

// aliasRe matches an alias group header in OWNERS_ALIASES.
var aliasRe = regexp.MustCompile(`^\s{2}([\w.-]+):\s*$`)

// itemRe matches one list item in an OWNERS or OWNERS_ALIASES file.
var itemRe = regexp.MustCompile(`^\s*-\s+(\S+)`)

// sectionRe matches a section header such as "approvers:" in an OWNERS file.
var sectionRe = regexp.MustCompile(`^([a-z_]+):\s*$`)

// LoadTruth walks a repository for OWNERS files and returns each directory's
// approvers with OWNERS_ALIASES expanded. Entries that stay group names after
// expansion are dropped: a truth entry must be a person.
func LoadTruth(repo string) ([]Truth, error) {
	aliases := loadAliases(filepath.Join(repo, "OWNERS_ALIASES"))
	var out []Truth
	err := filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "OWNERS" {
			return nil
		}
		approvers := parseOwners(path, aliases)
		if len(approvers) == 0 {
			return nil
		}
		rel, err := filepath.Rel(repo, filepath.Dir(path))
		if err != nil {
			return err
		}
		out = append(out, Truth{Dir: filepath.ToSlash(rel), Approvers: approvers})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ownersbench: walk: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	if len(out) == 0 {
		return nil, fmt.Errorf("ownersbench: %s holds no OWNERS files with individual approvers", repo)
	}
	return out, nil
}

// loadAliases reads OWNERS_ALIASES; a missing file is an empty table.
func loadAliases(path string) map[string][]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := make(map[string][]string)
	current := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		if m := aliasRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if current == "" {
			continue
		}
		if m := itemRe.FindStringSubmatch(line); m != nil {
			out[current] = append(out[current], normalizeLogin(m[1]))
		}
	}
	return out
}

// parseOwners reads one OWNERS file and returns its approvers, aliases
// expanded, group names dropped.
func parseOwners(path string, aliases map[string][]string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	section := ""
	var raw []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := sectionRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil && !strings.HasPrefix(line, " ") {
			section = m[1]
			continue
		}
		if section != "approvers" {
			continue
		}
		if m := itemRe.FindStringSubmatch(line); m != nil {
			raw = append(raw, normalizeLogin(m[1]))
		}
	}
	var out []string
	seen := make(map[string]bool)
	for _, r := range raw {
		expanded, ok := aliases[r]
		if !ok {
			expanded = []string{r}
		}
		for _, p := range expanded {
			// A name that is still a group after expansion is not a person.
			if p == "" || seen[p] || strings.HasPrefix(p, "sig-") || strings.HasPrefix(p, "wg-") ||
				strings.Contains(p, "@") {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// normalizeLogin lowercases and strips list punctuation from a login token.
func normalizeLogin(s string) string {
	return strings.ToLower(strings.Trim(s, `"',`))
}

// Identities maps ground-truth logins to the author names a git history uses
// for the same humans, via GitHub noreply addresses and normalized-name
// matching. Coverage is partial by nature and the bench reports how partial.
type Identities struct {
	// byLogin maps a login to every author name seen for it.
	byLogin map[string][]string
	// nameFallback maps a normalized name key to an index display name, so a
	// login like "bentheelder" still matches an author signing "Ben Elder"
	// when no noreply address ties them.
	nameFallback map[string]string
}

// nameKey reduces a name or login to its letters and digits.
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sinceDate renders a window as an absolute date. Git's approxidate parser
// silently matches nothing for very large "N days ago" values, which turned a
// wide-open window into an empty history once already.
func sinceDate(days int) string {
	return time.Now().AddDate(0, 0, -days).Format("2006-01-02")
}

// sinceArgs returns the git log window flag for a history window, or nothing
// when the window reaches far enough back to mean all of history.
//
// Asking git for a cutoff a century old does not mean "everything". git parses
// a date that old through approxidate, which handles it unreliably: the same
// repository answers a 1926 cutoff with every commit on one day and with none
// on the next, because the string this function returns moves forward daily.
// A caller passing 36500 days wants no filter at all, so it gets none, and the
// answer stops depending on what day the question is asked.
func sinceArgs(days int) []string {
	if days <= 0 || days >= allHistoryDays {
		return nil
	}
	return []string{"--since=" + sinceDate(days)}
}

// allHistoryDays is the window past which a caller means all of history rather
// than a date. Git's own timestamps start in 1970, so a window reaching before
// that can only be a request for everything.
const allHistoryDays = 20000

// noreplyRe extracts the login from a GitHub noreply commit address.
var noreplyRe = regexp.MustCompile(`(?i)^(?:\d+\+)?([a-z0-9-]+)@users\.noreply\.github\.com$`)

// LoadIdentities reads the repository log once and builds the login-to-author
// mapping. indexNames are every display name the index knows, used for the
// normalized-name fallback.
func LoadIdentities(repo string, sinceDays int, indexNames []string) (*Identities, error) {
	out := &Identities{byLogin: make(map[string][]string)}
	args := append([]string{"-C", repo, "log"}, sinceArgs(sinceDays)...)
	args = append(args, "--no-merges", "--format=%aN|%aE")
	cmd := exec.Command("git", args...)
	var errb strings.Builder
	cmd.Stderr = &errb
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ownersbench: git log: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(string(raw), "\n") {
		name, email, ok := strings.Cut(line, "|")
		if !ok || seen[line] {
			continue
		}
		seen[line] = true
		if m := noreplyRe.FindStringSubmatch(strings.TrimSpace(email)); m != nil {
			login := strings.ToLower(m[1])
			out.byLogin[login] = append(out.byLogin[login], name)
		}
	}
	byKey := make(map[string]string, len(indexNames))
	for _, n := range indexNames {
		byKey[nameKey(n)] = n
	}
	out.nameFallback = byKey
	return out, nil
}

// Names returns every author name a login is known by, including the
// normalized-name fallback against the index's own people.
func (ids *Identities) Names(login string) []string {
	out := append([]string(nil), ids.byLogin[login]...)
	if n, ok := ids.nameFallback[nameKey(login)]; ok {
		out = append(out, n)
	}
	return out
}

// Resolvable reports whether any git identity is known for the login.
func (ids *Identities) Resolvable(login string) bool { return len(ids.Names(login)) > 0 }
