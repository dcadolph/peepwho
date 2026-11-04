package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// testDirPaths returns n paths, one file in each of n distinct directories.
func testDirPaths(n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("d%02d/f.go", i)
	}
	return out
}

// TestDirPrefixes verifies every directory prefix of every path is listed once
// and a root file contributes none.
func TestDirPrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantPrefixes map[string]bool
		Paths        []string
	}{{ // Test 0: A nested path yields each of its directories.
		Paths: []string{"a/b/c.go"}, WantPrefixes: map[string]bool{"a": true, "a/b": true},
	}, { // Test 1: A root file has no directory.
		Paths: []string{"root.go"}, WantPrefixes: map[string]bool{},
	}, { // Test 2: Shared prefixes are listed once.
		Paths:        []string{"a/b/c.go", "a/d.go", "x/y.go"},
		WantPrefixes: map[string]bool{"a": true, "a/b": true, "x": true},
	}, { // Test 3: No paths yields nothing.
		Paths: nil, WantPrefixes: map[string]bool{},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := dirPrefixes(test.Paths)
			if diff := cmp.Diff(test.WantPrefixes, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestIsSweep verifies the sweep gate counts changed directories, not files,
// and admits exactly maxTogether of them.
func TestIsSweep(t *testing.T) {
	t.Parallel()
	many := make([]string, 0, 3*maxTogether)
	for i := range 3 * maxTogether {
		many = append(many, fmt.Sprintf("one/f%d.go", i))
	}
	tests := []struct {
		Paths     []string
		WantSweep bool
	}{{ // Test 0: Exactly maxTogether directories is not a sweep.
		Paths: testDirPaths(maxTogether), WantSweep: false,
	}, { // Test 1: One directory more is a sweep.
		Paths: testDirPaths(maxTogether + 1), WantSweep: true,
	}, { // Test 2: Many files in one directory is not a sweep.
		Paths: many, WantSweep: false,
	}, { // Test 3: No paths is not a sweep.
		Paths: nil, WantSweep: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := isSweep(test.Paths); got != test.WantSweep {
				t.Errorf("isSweep = %v, want %v", got, test.WantSweep)
			}
		})
	}
}

// TestNotePull verifies a merge records its sorted directories only when it
// changed some and no more than maxPullDirs of them.
func TestNotePull(t *testing.T) {
	t.Parallel()
	ref := PullRef{Repo: "acme/widget", Number: 7}
	capped := testDirPaths(maxPullDirs)
	cappedDirs := make([]string, len(capped))
	for i, p := range capped {
		cappedDirs[i] = strings.TrimSuffix(p, "/f.go")
	}
	tests := []struct {
		WantPulls map[PullRef][]string
		Paths     []string
	}{{ // Test 0: Directories are recorded sorted.
		Paths:     []string{"b/x/f.go", "a/g.go"},
		WantPulls: map[PullRef][]string{ref: {"a", "b", "b/x"}},
	}, { // Test 1: Root files alone record nothing.
		Paths: []string{"root.go"}, WantPulls: map[PullRef][]string{},
	}, { // Test 2: Exactly maxPullDirs directories are recorded.
		Paths: capped, WantPulls: map[PullRef][]string{ref: cappedDirs},
	}, { // Test 3: One directory past maxPullDirs records nothing.
		Paths: testDirPaths(maxPullDirs + 1), WantPulls: map[PullRef][]string{},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			tl := newTally()
			tl.notePull(ref, test.Paths)
			if diff := cmp.Diff(test.WantPulls, tl.pullDirs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// testPullClaim is one claimPull call in a TestClaimPull scenario.
type testPullClaim struct {
	// Pull is the pull request claimed.
	Pull PullRef
	// Path is the clone claiming it.
	Path string
	// Dirs are the directories claimed.
	Dirs []string
}

// newTestPlaceHistory returns a GitHistory with only the place maps set.
func newTestPlaceHistory() *GitHistory {
	return &GitHistory{
		dirWork:        make(map[string]map[string]float64),
		workTotals:     make(map[string]float64),
		pullDirs:       make(map[PullRef][]string),
		pullClaims:     make(map[PullRef]string),
		ambiguousPulls: make(map[PullRef]bool),
	}
}

// TestClaimPull verifies pull request claims: a keyed or same-clone claim is
// last-wins, and an unkeyed number claimed by two clones is retired for good.
func TestClaimPull(t *testing.T) {
	t.Parallel()
	keyed := PullRef{Repo: "acme/widget", Number: 3}
	bare := PullRef{Number: 3}
	tests := []struct {
		WantPulls     map[PullRef][]string
		WantAmbiguous map[PullRef]bool
		Claims        []testPullClaim
	}{{ // Test 0: A keyed pull claimed by two clones is last-wins.
		Claims: []testPullClaim{
			{Pull: keyed, Path: "/a", Dirs: []string{"x"}},
			{Pull: keyed, Path: "/b", Dirs: []string{"y"}},
		},
		WantPulls: map[PullRef][]string{keyed: {"y"}},
	}, { // Test 1: An unkeyed pull claimed twice by one clone is last-wins.
		Claims: []testPullClaim{
			{Pull: bare, Path: "/a", Dirs: []string{"x"}},
			{Pull: bare, Path: "/a", Dirs: []string{"z"}},
		},
		WantPulls: map[PullRef][]string{bare: {"z"}},
	}, { // Test 2: An unkeyed pull claimed by two clones is retired.
		Claims: []testPullClaim{
			{Pull: bare, Path: "/a", Dirs: []string{"x"}},
			{Pull: bare, Path: "/b", Dirs: []string{"y"}},
		},
		WantAmbiguous: map[PullRef]bool{bare: true},
	}, { // Test 3: A retired pull stays retired when its first clone claims again.
		Claims: []testPullClaim{
			{Pull: bare, Path: "/a", Dirs: []string{"x"}},
			{Pull: bare, Path: "/b", Dirs: []string{"y"}},
			{Pull: bare, Path: "/a", Dirs: []string{"x"}},
		},
		WantAmbiguous: map[PullRef]bool{bare: true},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			g := newTestPlaceHistory()
			for _, c := range test.Claims {
				g.claimPull(c.Pull, c.Path, c.Dirs)
			}
			if diff := cmp.Diff(test.WantPulls, g.pullDirs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("pulls mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantAmbiguous, g.ambiguousPulls, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ambiguous mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMergePlaces verifies a worker's directory credit, totals, and pull
// directories fold into the walk, scoped by the repository's label.
func TestMergePlaces(t *testing.T) {
	t.Parallel()
	pull := PullRef{Repo: "acme/widget", Number: 9}
	tests := []struct {
		WantDirWork    map[string]map[string]float64
		WantWorkTotals map[string]float64
		WantPulls      map[PullRef][]string
		Label          string
	}{{ // Test 0: No label keeps bare directory names.
		WantDirWork:    map[string]map[string]float64{"api": {"ann@x.com": 3, "bob@x.com": 1}},
		WantWorkTotals: map[string]float64{"ann@x.com": 4, "bob@x.com": 1},
		WantPulls:      map[PullRef][]string{pull: {"api", "api/v1"}},
	}, { // Test 1: A label prefixes directory work and pull directories alike.
		Label: "svc",
		WantDirWork: map[string]map[string]float64{
			"svc/api": {"ann@x.com": 2, "bob@x.com": 1},
			"api":     {"ann@x.com": 1},
		},
		WantWorkTotals: map[string]float64{"ann@x.com": 4, "bob@x.com": 1},
		WantPulls:      map[PullRef][]string{pull: {"svc/api", "svc/api/v1"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			g := newTestPlaceHistory()
			// An earlier repository's bare entry, to show scoping keeps them apart.
			g.dirWork["api"] = map[string]float64{"ann@x.com": 1}
			g.workTotals["ann@x.com"] = 1
			tl := newTally()
			tl.dirWork["api"] = map[string]float64{"ann@x.com": 2, "bob@x.com": 1}
			tl.workTotals["ann@x.com"] = 3
			tl.workTotals["bob@x.com"] = 1
			tl.pullDirs[pull] = []string{"api", "api/v1"}
			scoped := func(dir string) string {
				if test.Label == "" {
					return dir
				}
				return test.Label + "/" + dir
			}
			g.mergePlaces(&tl, "/repo", test.Label, scoped)
			if diff := cmp.Diff(test.WantDirWork, g.dirWork); diff != "" {
				t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantWorkTotals, g.workTotals); diff != "" {
				t.Errorf("workTotals mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPulls, g.pullDirs); diff != "" {
				t.Errorf("pulls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMergeArrivals verifies two workers' partial arrival times for one pair
// combine into the pair's earliest RampChanges, and first sightings into the
// earliest of each person's.
func TestMergeArrivals(t *testing.T) {
	t.Parallel()
	k := rampKey{Dir: "api", Email: "ann@x.com"}
	a, b := newTally(), newTally()
	a.entered[k] = []int64{100, 300, 500}
	b.entered[k] = []int64{200, 400, 600, 700}
	a.firstSeen["ann@x.com"] = 90
	b.firstSeen["ann@x.com"] = 50
	b.entered[rampKey{Dir: "web", Email: "bob@x.com"}] = []int64{1000}
	b.firstSeen["bob@x.com"] = 1000

	got := mergeArrivals([]tally{a, b}, func(d string) string { return "svc/" + d })
	want := []Ramp{{
		Dir: "svc/api", Email: "ann@x.com",
		First:       time.Unix(100, 0).UTC(),
		Established: time.Unix(500, 0).UTC(),
		Changes:     RampChanges,
		Newcomer:    true,
	}, {
		Dir: "svc/web", Email: "bob@x.com",
		First:   time.Unix(1000, 0).UTC(),
		Changes: 1,
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestAddNested verifies nested counts add onto existing ones and an empty
// inner map still creates its key.
func TestAddNested(t *testing.T) {
	t.Parallel()
	into := map[string]map[string]int{"ann": {"go": 1}}
	addNested(into, map[string]map[string]int{
		"ann": {"go": 2, "sql": 1},
		"bob": {},
	})
	want := map[string]map[string]int{"ann": {"go": 3, "sql": 1}, "bob": {}}
	if diff := cmp.Diff(want, into); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestMergeSubjects verifies a worker's subject tallies, names, latest times,
// and curated tokens fold into the walk's totals.
func TestMergeSubjects(t *testing.T) {
	t.Parallel()
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	tl := newTally()
	tl.counts["ann"] = map[string]int{"go": 2}
	tl.names["ann"] = map[string]int{"Ann": 1}
	tl.latest["ann"] = early
	tl.latest["bob"] = late
	tl.curated["go"] = true
	tl.recent["ann"] = map[string]int{"go": 1}
	tl.direct["ann"] = map[string]int{}

	counts := map[string]map[string]int{"ann": {"go": 1}}
	names := map[string]map[string]int{}
	latest := map[string]time.Time{"ann": late, "bob": early}
	curated := map[string]bool{}
	recent := map[string]map[string]int{}
	direct := map[string]map[string]int{}
	mergeSubjects(&tl, counts, names, latest, curated, recent, direct, newTogether())

	if diff := cmp.Diff(map[string]map[string]int{"ann": {"go": 3}}, counts); diff != "" {
		t.Errorf("counts mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]map[string]int{"ann": {"Ann": 1}}, names); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]time.Time{"ann": late, "bob": late}, latest); diff != "" {
		t.Errorf("latest mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]bool{"go": true}, curated); diff != "" {
		t.Errorf("curated mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]map[string]int{"ann": {"go": 1}}, recent); diff != "" {
		t.Errorf("recent mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]map[string]int{"ann": {}}, direct); diff != "" {
		t.Errorf("direct mismatch (-want +got):\n%s", diff)
	}
}

// TestNotePerson verifies a commit counts towards a person's total, moves
// their latest time only forward, and counts a non-empty display name.
func TestNotePerson(t *testing.T) {
	t.Parallel()
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	tests := []struct {
		WantLatest time.Time
		WantNames  map[string]map[string]int
		Name       string
		When       time.Time
	}{{ // Test 0: A later commit with a name moves latest and counts the name.
		Name: "Ann", When: late, WantLatest: late,
		WantNames: map[string]map[string]int{"ann": {"Ann": 1}},
	}, { // Test 1: An earlier commit leaves latest alone.
		Name: "Ann", When: early.Add(-time.Hour), WantLatest: early,
		WantNames: map[string]map[string]int{"ann": {"Ann": 1}},
	}, { // Test 2: An empty name is not counted.
		Name: "", When: late, WantLatest: late,
		WantNames: map[string]map[string]int{},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			tl := newTally()
			tl.latest["ann"] = early
			tl.notePerson("ann", test.Name, test.When)
			if got := tl.workTotals["ann"]; got != 1 {
				t.Errorf("workTotals = %v, want 1", got)
			}
			if got := tl.latest["ann"]; !got.Equal(test.WantLatest) {
				t.Errorf("latest = %v, want %v", got, test.WantLatest)
			}
			if diff := cmp.Diff(test.WantNames, tl.names); diff != "" {
				t.Errorf("names mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCountSubjects verifies each subject counts once, recent counts only
// when lately, and both entries exist even for a commit that touched nothing.
func TestCountSubjects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		InCommit   map[string]bool
		WantCounts map[string]map[string]int
		WantRecent map[string]map[string]int
		Lately     bool
	}{{ // Test 0: An old commit counts subjects but not recent work.
		InCommit:   map[string]bool{"go": true, "sql": true},
		WantCounts: map[string]map[string]int{"ann": {"go": 1, "sql": 1}},
		WantRecent: map[string]map[string]int{},
	}, { // Test 1: A recent commit counts both.
		InCommit: map[string]bool{"go": true}, Lately: true,
		WantCounts: map[string]map[string]int{"ann": {"go": 1}},
		WantRecent: map[string]map[string]int{"ann": {"go": 1}},
	}, { // Test 2: A recent commit with no subjects still creates both entries.
		InCommit: map[string]bool{}, Lately: true,
		WantCounts: map[string]map[string]int{"ann": {}},
		WantRecent: map[string]map[string]int{"ann": {}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			tl := newTally()
			tl.countSubjects("ann", test.Lately, test.InCommit)
			if diff := cmp.Diff(test.WantCounts, tl.counts); diff != "" {
				t.Errorf("counts mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantRecent, tl.recent); diff != "" {
				t.Errorf("recent mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSubjectsOf verifies per-file subject attribution: directory subjects per
// path, file-name words counted towards the commit only, and only a path's
// stated names counted as direct.
func TestSubjectsOf(t *testing.T) {
	t.Parallel()
	got := subjectsOf([]string{"zwave_js/light.py", "README.md"})
	want := commitSubjects{
		byPath:  [][]string{{"zwave_js", "zwave", "python"}, {"markdown"}},
		deepest: [][]string{{"zwave_js", "zwave"}, nil},
		inCommit: map[string]bool{
			"zwave_js": true, "zwave": true, "python": true, "light": true,
			"markdown": true, "readme": true,
		},
		directHere: map[string]bool{"zwave_js": true, "python": true, "markdown": true},
	}
	opts := []cmp.Option{cmp.AllowUnexported(commitSubjects{}), cmpopts.EquateEmpty()}
	if diff := cmp.Diff(want, got, opts...); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestStatedNames verifies a whole directory name and the file's technology
// are stated, while the words inside a compound directory name are not.
func TestStatedNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantStated map[string]bool
		Path       string
	}{{ // Test 0: A plain directory and its technology.
		Path: "infra/main.tf", WantStated: map[string]bool{"infra": true, "terraform": true},
	}, { // Test 1: A compound directory is stated whole, not by its words.
		Path: "zwave_js/light.py", WantStated: map[string]bool{"zwave_js": true, "python": true},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantStated, statedNames(test.Path)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCreditCommit verifies one authored commit's credit: the author and every
// trailer get its subjects and places, only the author and a co-author get
// direct credit, and a sweep gets no direct or place credit at all.
func TestCreditCommit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	trailers := []trailerPerson{
		{Email: "co@x.com", Name: "Co", CoAuthor: true},
		{Email: "rev@x.com", Name: "Rev"},
	}
	tests := []struct {
		WantDirect  map[string]map[string]int
		WantDirWork map[string]map[string]float64
		WantRecent  []string
		Paths       []string
		Age         time.Duration
	}{{ // Test 0: A narrow recent commit credits places and direct work.
		Paths: []string{"zwave_js/light.py"}, Age: time.Hour,
		WantDirect: map[string]map[string]int{
			"ann@x.com": {"zwave_js": 1, "python": 1},
			"co@x.com":  {"zwave_js": 1, "python": 1},
		},
		WantDirWork: map[string]map[string]float64{
			"zwave_js": {"ann@x.com": 1, "co@x.com": 1, "rev@x.com": 1},
		},
		WantRecent: []string{"ann@x.com", "co@x.com", "rev@x.com"},
	}, { // Test 1: A sweep leaves only the author's empty direct entry.
		Paths: testDirPaths(maxTogether + 1), Age: time.Hour,
		WantDirect:  map[string]map[string]int{"ann@x.com": {}},
		WantDirWork: map[string]map[string]float64{},
		WantRecent:  []string{"ann@x.com", "co@x.com", "rev@x.com"},
	}, { // Test 2: An old commit counts no recent work.
		Paths: []string{"zwave_js/light.py"}, Age: recentWindow + time.Hour,
		WantDirect: map[string]map[string]int{
			"ann@x.com": {"zwave_js": 1, "python": 1},
			"co@x.com":  {"zwave_js": 1, "python": 1},
		},
		WantDirWork: map[string]map[string]float64{
			"zwave_js": {"ann@x.com": 1, "co@x.com": 1, "rev@x.com": 1},
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			when := now.Add(-test.Age)
			job := commitJob{
				Email: "ann@x.com", Name: "Ann", When: when,
				Subject: "fix rate limiter", Trailers: trailers,
			}
			tl := newTally()
			tl.creditCommit(job, test.Paths, "/repo", now)
			if diff := cmp.Diff(test.WantDirect, tl.direct); diff != "" {
				t.Errorf("direct mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantDirWork, tl.dirWork, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
			}
			gotRecent := make([]string, 0, len(tl.recent))
			for email := range tl.recent {
				gotRecent = append(gotRecent, email)
			}
			if diff := cmp.Diff(test.WantRecent, gotRecent, cmpopts.EquateEmpty(),
				cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("recent mismatch (-want +got):\n%s", diff)
			}
			wantTotals := map[string]float64{"ann@x.com": 1, "co@x.com": 1, "rev@x.com": 1}
			if diff := cmp.Diff(wantTotals, tl.workTotals); diff != "" {
				t.Errorf("workTotals mismatch (-want +got):\n%s", diff)
			}
			// Only the author's first sighting is recorded.
			if diff := cmp.Diff(map[string]int64{"ann@x.com": when.Unix()}, tl.firstSeen); diff != "" {
				t.Errorf("firstSeen mismatch (-want +got):\n%s", diff)
			}
			// The subject line credits the author alone.
			if tl.counts["ann@x.com"]["limiter"] != 1 || tl.counts["co@x.com"]["limiter"] != 0 {
				t.Errorf("subject tokens: author %v, co-author %v",
					tl.counts["ann@x.com"], tl.counts["co@x.com"])
			}
		})
	}
}

// newTestWalkRepo builds a repository with git: a root commit of
// maxRootCommitFiles+1 files, a git mv of billing/invoices.py to
// ledger/invoices.py, and a pull request merge touching feature/. It returns
// the path and the root, rename, and merge hashes.
func newTestWalkRepo(t *testing.T) (dir, root, rename, merge string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, nil, "init", "-q", "-b", "main")
	write := func(rel string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(rel+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	head := func() string {
		t.Helper()
		return strings.TrimSpace(runGit(t, dir, nil, "rev-parse", "HEAD"))
	}
	write("billing/invoices.py")
	for i := range maxRootCommitFiles {
		write(fmt.Sprintf("pad/f%03d.txt", i))
	}
	runGit(t, dir, nil, "add", "-A")
	runGit(t, dir, authorEnv("Ann", "ann@x.com", "2026-01-05T10:00:00Z"),
		"commit", "-q", "-m", "lay out")
	root = head()

	if err := os.MkdirAll(filepath.Join(dir, "ledger"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, dir, nil, "mv", "billing/invoices.py", "ledger/invoices.py")
	runGit(t, dir, authorEnv("Bob", "bob@x.com", "2026-01-06T10:00:00Z"), "commit", "-q", "-m", "move")
	rename = head()

	runGit(t, dir, nil, "checkout", "-q", "-b", "feature")
	write("feature/flag.go")
	runGit(t, dir, nil, "add", "-A")
	runGit(t, dir, authorEnv("Cy", "cy@x.com", "2026-01-07T10:00:00Z"), "commit", "-q", "-m", "flag")
	runGit(t, dir, nil, "checkout", "-q", "main")
	runGit(t, dir, authorEnv("Cy", "cy@x.com", "2026-01-08T10:00:00Z"),
		"merge", "-q", "--no-ff", "-m", "Merge pull request #7 from cy/feature", "feature")
	merge = head()
	return dir, root, rename, merge
}

// TestWalkJob verifies one commit's walk against a real repository: a rename
// credits both the old and the new place, a missing commit is skipped, a pull
// request merge records its directories and credits nobody, and an oversized
// root commit is left out.
func TestWalkJob(t *testing.T) {
	t.Parallel()
	dir, root, rename, merge := newTestWalkRepo(t)
	when := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC)
	pull := PullRef{Repo: "acme/widget", Number: 7}
	tests := []struct {
		WantDirWork map[string]map[string]float64
		WantPulls   map[PullRef][]string
		Job         commitJob
		WantRead    int
		WantSkipped int
	}{{ // Test 0: A rename credits the place it left and the place it went.
		Job: commitJob{Hash: plumbing.NewHash(rename), Email: "bob@x.com", When: when},
		WantDirWork: map[string]map[string]float64{
			"billing": {"bob@x.com": 1}, "ledger": {"bob@x.com": 1},
		},
		WantRead: 1,
	}, { // Test 1: A commit the repository lacks is skipped.
		Job:         commitJob{Hash: plumbing.NewHash(strings.Repeat("ab", 20)), Email: "bob@x.com"},
		WantSkipped: 1,
	}, { // Test 2: A pull request merge records its directories and credits nobody.
		Job:       commitJob{Hash: plumbing.NewHash(merge), When: when, Pull: 7},
		WantPulls: map[PullRef][]string{pull: {"feature"}},
	}, { // Test 3: A root commit past maxRootCommitFiles is left out.
		Job: commitJob{Hash: plumbing.NewHash(root), Email: "ann@x.com", When: when, Root: true},
	}, { // Test 4: The same commit walked as a non-root one is read.
		Job: commitJob{Hash: plumbing.NewHash(root), Email: "ann@x.com", When: when},
		WantDirWork: map[string]map[string]float64{
			"billing": {"ann@x.com": 1}, "pad": {"ann@x.com": 1},
		},
		WantRead: 1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			// Each subtest opens its own copy, the way each worker does, since a
			// repository handle is not safe to share across goroutines.
			repo, closeRepo, err := openRepo(dir)
			if err != nil {
				t.Fatalf("openRepo: %v", err)
			}
			defer func() { _ = closeRepo() }()
			var done atomic.Int64
			tl := newTally()
			tl.walkJob(repo, test.Job, dir, "acme/widget", when, &done)
			if tl.read != test.WantRead || tl.skipped != test.WantSkipped {
				t.Errorf("read, skipped = %d, %d, want %d, %d",
					tl.read, tl.skipped, test.WantRead, test.WantSkipped)
			}
			if got := int(done.Load()); got != test.WantRead {
				t.Errorf("done = %d, want %d", got, test.WantRead)
			}
			if diff := cmp.Diff(test.WantDirWork, tl.dirWork, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dirWork mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPulls, tl.pullDirs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("pulls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDiffShareStops verifies a worker that cannot open the repository counts
// its share as skipped, and a canceled walk reads nothing.
func TestDiffShareStops(t *testing.T) {
	t.Parallel()
	dir, _, rename, _ := newTestWalkRepo(t)
	jobs := []commitJob{
		{Hash: plumbing.NewHash(rename), Email: "bob@x.com"},
		{Hash: plumbing.NewHash(rename), Email: "bob@x.com"},
		{Hash: plumbing.NewHash(rename), Email: "bob@x.com"},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		Ctx         context.Context
		Path        string
		WantRead    int
		WantSkipped int
	}{{ // Test 0: An unopenable repository skips the worker's share.
		Ctx: context.Background(), Path: filepath.Join(dir, "missing"), WantSkipped: 2,
	}, { // Test 1: A canceled walk reads nothing.
		Ctx: canceled, Path: dir,
	}, { // Test 2: A live walk reads every commit in the share.
		Ctx: context.Background(), Path: dir, WantRead: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var done atomic.Int64
			g := &GitHistory{}
			tl := g.diffShare(test.Ctx, test.Path, "", jobs, 0, 2, time.Now(), &done)
			if tl.read != test.WantRead || tl.skipped != test.WantSkipped {
				t.Errorf("read, skipped = %d, %d, want %d, %d",
					tl.read, tl.skipped, test.WantRead, test.WantSkipped)
			}
		})
	}
}
