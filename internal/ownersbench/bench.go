package ownersbench

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// Config bounds one benchmark run.
type Config struct {
	// Repo is the repository root holding the OWNERS ground truth.
	Repo string
	// SinceDays is the history window, matching how the index was built.
	SinceDays int
	// MinCommits is the least git activity a directory needs to be judged.
	MinCommits int
	// TopK is how many names each side may offer.
	TopK int
	// MaxLeafShare is the most directories a leaf name may be shared by
	// before the directory is excluded as unjudgeable: peepwho's subjects are
	// names, and a name sixty directories share identifies none of them.
	MaxLeafShare int
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// DirWork, when set, is the git connector's per-directory work tally, and
	// becomes the primary ranking: ownership is asked about places, and the
	// place-scoped tally answers the question the subject pool only
	// approximates. Keys are author emails; WorkTotals must accompany it.
	DirWork map[string]map[string]float64
	// WorkTotals is each author's total work, the breadth discount base.
	WorkTotals map[string]float64
}

// withDefaults fills unset bounds.
func (c Config) withDefaults() Config {
	if c.SinceDays <= 0 {
		c.SinceDays = 730
	}
	if c.MinCommits <= 0 {
		c.MinCommits = 30
	}
	if c.TopK <= 0 {
		c.TopK = 3
	}
	if c.MaxLeafShare <= 0 {
		c.MaxLeafShare = 3
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
	return c
}

// DirResult is one judged directory.
type DirResult struct {
	// Dir is the directory relative to the repository root.
	Dir string `json:"dir"`
	// Approvers are the humans the project's own OWNERS file names.
	Approvers []string `json:"approvers"`
	// GitTop are the top committers by files touched, the naive baseline.
	GitTop []string `json:"gitTop"`
	// PeepwhoTop are the people peepwho says the subject rests on.
	PeepwhoTop []string `json:"peepwhoTop"`
	// GitHit reports whether an approver is among the top committers.
	GitHit bool `json:"gitHit"`
	// PeepwhoHit reports whether peepwho named an approver.
	PeepwhoHit bool `json:"peepwhoHit"`
}

// Result is a full benchmark run.
type Result struct {
	// Dirs are the judged directories.
	Dirs []DirResult `json:"dirs"`
	// TruthDirs is how many directories carried individual approvers at all.
	TruthDirs int `json:"truthDirs"`
	// DroppedQuiet were below the activity floor.
	DroppedQuiet int `json:"droppedQuiet"`
	// DroppedAmbiguous had leaf names too widely shared to identify a subject.
	DroppedAmbiguous int `json:"droppedAmbiguous"`
	// DroppedUnmappable had no approver joinable to any git identity.
	DroppedUnmappable int `json:"droppedUnmappable"`
}

// Score tallies hits for one predicate over the judged directories.
func (r *Result) Score(hit func(DirResult) bool) (n, of int) {
	for _, d := range r.Dirs {
		of++
		if hit(d) {
			n++
		}
	}
	return n, of
}

// CohortC returns the judged directories where the baseline missed: the
// approver is not a top committer, which is the only cohort that can tell
// this tool apart from counting commits.
func (r *Result) CohortC() []DirResult {
	var out []DirResult
	for _, d := range r.Dirs {
		if !d.GitHit {
			out = append(out, d)
		}
	}
	return out
}

// Run scores an index against the repository's OWNERS truth.
func Run(ix *index.Index, cfg Config) (*Result, error) {
	cfg = cfg.withDefaults()
	truth, err := LoadTruth(cfg.Repo)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(cfg.Log, "ownersbench: %d directories carry individual approvers\n", len(truth))

	names := make([]string, 0, len(ix.Graph.People))
	for _, p := range ix.Graph.People {
		names = append(names, p.Name)
	}
	ids, err := LoadIdentities(cfg.Repo, cfg.SinceDays, names)
	if err != nil {
		return nil, err
	}

	activity, err := dirActivity(cfg.Repo, cfg.SinceDays)
	if err != nil {
		return nil, err
	}
	leafShare := leafCounts(cfg.Repo)
	leaders := resolve.Leaders(ix, cfg.TopK)

	res := &Result{TruthDirs: len(truth)}
	for _, t := range truth {
		act := activity[t.Dir]
		if act.total < cfg.MinCommits {
			res.DroppedQuiet++
			continue
		}
		leaf := leafOf(t.Dir)
		if leafShare[strings.ToLower(leaf)] > cfg.MaxLeafShare {
			res.DroppedAmbiguous++
			continue
		}
		approverKeys := make(map[string]bool)
		var approvers []string
		for _, login := range t.Approvers {
			if !ids.Resolvable(login) {
				continue
			}
			approvers = append(approvers, login)
			approverKeys[nameKey(login)] = true
			for _, n := range ids.Names(login) {
				approverKeys[nameKey(n)] = true
			}
		}
		if len(approvers) == 0 {
			res.DroppedUnmappable++
			continue
		}

		gitTop := act.top(cfg.TopK)
		topic := model.ID(strings.ToLower(leaf))
		var subjectTop []string
		for _, l := range leaders[topic] {
			subjectTop = append(subjectTop, l.Name)
		}
		peepwhoTop := resolve.FuseRanks(cfg.TopK, dirRanked(ix, cfg, t.Dir), subjectTop)
		res.Dirs = append(res.Dirs, DirResult{
			Dir: t.Dir, Approvers: approvers, GitTop: gitTop, PeepwhoTop: peepwhoTop,
			GitHit:     anyKeyed(gitTop, approverKeys),
			PeepwhoHit: anyKeyed(peepwhoTop, approverKeys),
		})
	}
	if len(res.Dirs) == 0 {
		return nil, fmt.Errorf("ownersbench: no judgeable directories; %d quiet, %d ambiguous, %d unmappable",
			res.DroppedQuiet, res.DroppedAmbiguous, res.DroppedUnmappable)
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return res.Dirs[i].Dir < res.Dirs[j].Dir })
	return res, nil
}

// dirPlace scores one directory through the same place model the product
// ships in resolve, so a benchmark judges exactly what a buyer is shown. It
// returns false when the directory has no tally to score.
func dirPlace(ix *index.Index, cfg Config, dir string) (resolve.Place, bool) {
	if len(cfg.DirWork[dir]) == 0 {
		return resolve.Place{}, false
	}
	places := resolve.PlaceLeads(ix,
		map[string]map[string]float64{dir: cfg.DirWork[dir]}, cfg.WorkTotals, 1, cfg.TopK)
	if len(places) == 0 {
		return resolve.Place{}, false
	}
	return places[0], true
}

// dirRanked ranks the people a directory's work rests on, through the same
// place model the product ships in resolve, so the benchmark scores exactly
// what a buyer sees.
func dirRanked(ix *index.Index, cfg Config, dir string) []string {
	place, ok := dirPlace(ix, cfg, dir)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(place.Holders))
	for _, h := range place.Holders {
		names = append(names, h.Name)
	}
	return names
}

// anyKeyed reports whether any name matches the key set.
func anyKeyed(names []string, keys map[string]bool) bool {
	for _, n := range names {
		if keys[nameKey(n)] {
			return true
		}
	}
	return false
}

// leafOf returns a directory's most specific segment.
func leafOf(dir string) string {
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

// humans returns every human author who touched the directory at all, keyed
// for name comparison. Presence is a different question from volume: whether
// the person you would be sent to is still working here does not depend on
// them out-committing everybody.
func (d dirCount) humans() map[string]bool {
	out := make(map[string]bool, len(d.byAuthor))
	for n := range d.byAuthor {
		if connector.IsAutomationName(n) {
			continue
		}
		out[nameKey(n)] = true
	}
	return out
}

// dirCount is one directory's activity: which authors touched files under it.
type dirCount struct {
	// byAuthor counts files touched per author name.
	byAuthor map[string]int
	// total is the sum over authors.
	total int
}

// top returns the k most active human authors, ties broken by name.
// Automation is excluded: a robot that opens a hundred dependency bumps is
// not a person the work rests on, and leaving it in rewards whichever side
// of a comparison failed to filter it.
func (d dirCount) top(k int) []string {
	type pair struct {
		n string
		c int
	}
	out := make([]pair, 0, len(d.byAuthor))
	for n, c := range d.byAuthor {
		if connector.IsAutomationName(n) {
			continue
		}
		out = append(out, pair{n, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].c != out[j].c {
			return out[i].c > out[j].c
		}
		return out[i].n < out[j].n
	})
	if len(out) > k {
		out = out[:k]
	}
	names := make([]string, len(out))
	for i, p := range out {
		names[i] = p.n
	}
	return names
}

// dirActivity reads the log once and counts, for every directory prefix, how
// many file touches each author made under it.
func dirActivity(repo string, sinceDays int) (map[string]dirCount, error) {
	return dirActivityRange(repo, sinceDays, 0)
}

// dirActivityRange is dirActivity over a window that may stop short of today,
// so a holdout can read the past a prediction was made from and the future it
// is judged against with the same code.
func dirActivityRange(repo string, sinceDays, untilDays int) (map[string]dirCount, error) {
	args := append([]string{"-C", repo, "log"}, sinceArgs(sinceDays)...)
	args = append(args, "--no-merges", "--format=@@%aN", "--name-only")
	if untilDays > 0 {
		args = append(args, "--until="+sinceDate(untilDays))
	}
	cmd := exec.Command("git", args...)
	var errb strings.Builder
	cmd.Stderr = &errb
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ownersbench: git log: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	out := make(map[string]dirCount)
	author := ""
	bump := func(dir string) {
		d, ok := out[dir]
		if !ok {
			d = dirCount{byAuthor: make(map[string]int)}
		}
		d.byAuthor[author]++
		d.total++
		out[dir] = d
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(line, "@@") {
			author = line[2:]
			continue
		}
		if line == "" || author == "" {
			continue
		}
		parts := strings.Split(line, "/")
		for i := 1; i < len(parts); i++ {
			bump(strings.Join(parts[:i], "/"))
		}
	}
	return out, nil
}

// leafCounts counts, for every directory basename in the working tree, how
// many directories carry it.
func leafCounts(repo string) map[string]int {
	out := make(map[string]int)
	_ = filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // Unreadable entries are skipped.
		}
		name := d.Name()
		if name == ".git" || name == "vendor" || name == "node_modules" {
			return filepath.SkipDir
		}
		out[strings.ToLower(name)]++
		return nil
	})
	return out
}
