// Diffing the scanned commits in parallel: each worker walks its share of the
// commits and credits what each one touched, and the shares are folded into the
// walk's totals once every worker is done.

package connector

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	git "github.com/go-git/go-git/v5"
)

// diffCommits computes what each commit touched, in parallel. Every commit is
// diffed against its own parent and nothing else, so the work divides cleanly;
// each worker opens the repository for itself rather than sharing one, since
// the saving comes from holding a packfile open and a shared one would only
// move the contention.
func (g *GitHistory) diffCommits(
	ctx context.Context,
	path, repoID string,
	jobs []commitJob,
	counts map[string]map[string]int,
	names map[string]map[string]int,
	latest map[string]time.Time,
	curated map[string]bool,
	recent map[string]map[string]int,
	direct map[string]map[string]int,
	ties *togetherIndex,
) (read, skipped int, err error) {
	if len(jobs) == 0 {
		return 0, 0, nil
	}
	tallies := g.runDiffWorkers(ctx, path, repoID, jobs)

	// The repository's label scopes directory keys when several repositories
	// are read, so twelve services' internal/ directories stay twelve places.
	// Pull request directories carry the same prefix, since review credit is
	// added onto these keys and an unprefixed entry would mint a phantom place.
	label := g.placeLabels[path]
	scoped := func(dir string) string {
		if label == "" {
			return dir
		}
		return label + "/" + dir
	}
	g.ramps = append(g.ramps, mergeArrivals(tallies, scoped)...)

	for _, t := range tallies {
		read += t.read
		skipped += t.skipped
		g.mergePlaces(&t, path, label, scoped)
		mergeSubjects(&t, counts, names, latest, curated, recent, direct, ties)
	}
	return read, skipped, ctx.Err()
}

// runDiffWorkers starts one diffShare per worker over the jobs, reports
// progress until they all finish, and returns each worker's tally in worker
// order.
func (g *GitHistory) runDiffWorkers(ctx context.Context, path, repoID string, jobs []commitJob) []tally {
	workers := g.opts.Workers
	if workers > len(jobs) {
		workers = len(jobs)
	}

	// One reading of the clock for the whole walk, the same way scanCommits
	// pins its window. Reading it per commit inside the workers made the
	// recent-window boundary drift within a single run.
	now := g.now()
	var done atomic.Int64
	tallies := make([]tally, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			tallies[w] = g.diffShare(ctx, path, repoID, jobs, w, workers, now, &done)
		}(w)
	}
	g.reportDiffProgress(path, len(jobs), &done, &wg)
	return tallies
}

// reportDiffProgress prints how many commits have been read every
// gitProgressEvery until the workers in wg finish. Reporting from here rather
// than from the workers keeps progress one steady line rather than several
// racing to print the same number.
func (g *GitHistory) reportDiffProgress(path string, total int, done *atomic.Int64, wg *sync.WaitGroup) {
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	ticker := time.NewTicker(gitProgressEvery)
	defer ticker.Stop()
	for waiting := true; waiting; {
		select {
		case <-finished:
			waiting = false
		case <-ticker.C:
			fmt.Fprintf(g.opts.Log, "git: %s: %d of %d commits read\n", path, done.Load(), total)
		}
	}
}

// mergeArrivals folds every worker's arrival times and first sightings into
// one view and returns the arrivals it describes. Arrivals are folded before
// the per-directory tallies so the merged view sees every worker's earliest
// times for a pair, not one worker's share.
func mergeArrivals(tallies []tally, scoped func(string) string) []Ramp {
	entered := make(map[rampKey][]int64)
	firstSeen := make(map[string]int64)
	for _, t := range tallies {
		for k, times := range t.entered {
			entered[k] = earliestTimes(append(entered[k], times...))
		}
		for email, at := range t.firstSeen {
			if firstSeen[email] == 0 || at < firstSeen[email] {
				firstSeen[email] = at
			}
		}
	}
	return arrivalsOf(entered, firstSeen, scoped)
}

// mergePlaces folds one worker's directory credit, commit totals, and pull
// request directories into the walk's place tallies, scoping every directory
// by the repository's label.
func (g *GitHistory) mergePlaces(t *tally, path, label string, scoped func(string) string) {
	for dir, people := range t.dirWork {
		addCounts(nested(g.dirWork, scoped(dir)), people)
	}
	addCounts(g.workTotals, t.workTotals)
	for pull, dirs := range t.pullDirs {
		if label != "" {
			prefixed := make([]string, len(dirs))
			for i, d := range dirs {
				prefixed[i] = scoped(d)
			}
			dirs = prefixed
		}
		g.claimPull(pull, path, dirs)
	}
}

// claimPull records the directories one pull request changed in the clone at
// path. An empty-repo key claimed by two different clones means two
// repositories with no identity share a number. Keeping either would hand one
// clone's reviewers the other's directories on path order, so the number is
// retired from review credit. The same clone claiming again, a re-merge or a
// re-read, keeps the old last-wins behavior.
func (g *GitHistory) claimPull(pull PullRef, path string, dirs []string) {
	if pull.Repo == "" {
		if g.ambiguousPulls[pull] {
			return
		}
		if owner, taken := g.pullClaims[pull]; taken && owner != path {
			delete(g.pullDirs, pull)
			g.ambiguousPulls[pull] = true
			return
		}
		g.pullClaims[pull] = path
	}
	g.pullDirs[pull] = dirs
}

// mergeSubjects folds one worker's subject tallies, display names, latest
// activity, and ties into the walk's totals.
func mergeSubjects(
	t *tally,
	counts map[string]map[string]int,
	names map[string]map[string]int,
	latest map[string]time.Time,
	curated map[string]bool,
	recent map[string]map[string]int,
	direct map[string]map[string]int,
	ties *togetherIndex,
) {
	for tok := range t.curated {
		curated[tok] = true
	}
	addNested(recent, t.recent)
	addNested(direct, t.direct)
	ties.absorb(t.ties)
	addNested(counts, t.counts)
	for email, when := range t.latest {
		if when.After(latest[email]) {
			latest[email] = when
		}
	}
	addNested(names, t.names)
}

// nested returns the inner map outer holds under key, creating and storing an
// empty one when there is none yet.
func nested[V int | float64](outer map[string]map[string]V, key string) map[string]V {
	m := outer[key]
	if m == nil {
		m = make(map[string]V)
		outer[key] = m
	}
	return m
}

// addCounts adds every count in from onto the same key in into.
func addCounts[V int | float64](into, from map[string]V) {
	for k, n := range from {
		into[k] += n
	}
}

// addNested adds every inner count in from onto into, creating the inner maps
// into lacks. An inner map present but empty in from still creates its key in
// into, the same as a non-empty one.
func addNested[V int | float64](into, from map[string]map[string]V) {
	for k, inner := range from {
		addCounts(nested(into, k), inner)
	}
}

// newTally returns an empty tally with every map ready to write.
func newTally() tally {
	return tally{
		counts:     make(map[string]map[string]int),
		names:      make(map[string]map[string]int),
		latest:     make(map[string]time.Time),
		curated:    make(map[string]bool),
		recent:     make(map[string]map[string]int),
		direct:     make(map[string]map[string]int),
		ties:       newTogether(),
		dirWork:    make(map[string]map[string]float64),
		workTotals: make(map[string]float64),
		pullDirs:   make(map[PullRef][]string),
		entered:    make(map[rampKey][]int64),
		firstSeen:  make(map[string]int64),
	}
}

// diffShare walks every worker'th commit, so each worker covers a spread of the
// history rather than one contiguous era of it, and the shares stay even when
// some stretch of the history is much heavier than the rest.
func (g *GitHistory) diffShare(
	ctx context.Context,
	path, repoID string,
	jobs []commitJob,
	worker, workers int,
	now time.Time,
	done *atomic.Int64,
) tally {
	t := newTally()
	repo, closeRepo, err := openRepo(path)
	if err != nil {
		t.skipped = len(jobs)/workers + 1
		return t
	}
	defer func() { _ = closeRepo() }()

	for i := worker; i < len(jobs); i += workers {
		if ctx.Err() != nil {
			return t
		}
		t.walkJob(repo, jobs[i], path, repoID, now, done)
	}
	return t
}

// walkJob diffs one commit against its parent and credits what it touched. A
// commit that cannot be read or diffed counts as skipped.
func (t *tally) walkJob(
	repo *git.Repository, job commitJob, path, repoID string, now time.Time, done *atomic.Int64,
) {
	commit, err := repo.CommitObject(job.Hash)
	if err != nil {
		t.skipped++
		return
	}
	paths, err := changedPaths(commit)
	if err != nil {
		t.skipped++
		return
	}
	if job.Pull > 0 {
		t.notePull(PullRef{Repo: repoID, Number: job.Pull}, paths)
		return
	}
	if job.Root && len(paths) > maxRootCommitFiles {
		return
	}
	t.read++
	done.Add(1)
	t.creditCommit(job, paths, path, now)
}

// notePull records which directories a pull request's merge changed. A merge
// credits nobody. It is here to record which places the pull request touched,
// so whoever reviewed it can be credited with those places rather than with
// the words of its title.
func (t *tally) notePull(ref PullRef, paths []string) {
	dirs := dirPrefixes(paths)
	if len(dirs) > 0 && len(dirs) <= maxPullDirs {
		t.pullDirs[ref] = slices.Sorted(maps.Keys(dirs))
	}
}

// dirPrefixes returns every directory prefix of every path: a/b/c.go yields a
// and a/b.
func dirPrefixes(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths)*2)
	for _, name := range paths {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			out[strings.Join(parts[:i], "/")] = true
		}
	}
	return out
}

// creditCommit credits one authored commit's author and the people its
// trailers name with the subjects and places it touched.
func (t *tally) creditCommit(job commitJob, paths []string, path string, now time.Time) {
	t.notePerson(job.Email, job.Name, job.When)
	if at := job.When.Unix(); t.firstSeen[job.Email] == 0 || at < t.firstSeen[job.Email] {
		t.firstSeen[job.Email] = at
	}
	lately := now.Sub(job.When) <= recentWindow
	subs := subjectsOf(paths)
	for tok := range subs.directHere {
		t.curated[tok] = true
	}
	// Created even when nothing lands in it, so the author always has a direct
	// entry, the same as an author with direct work.
	direct := nested(t.direct, job.Email)
	t.countSubjects(job.Email, lately, subs.inCommit)
	narrow := !isSweep(paths)
	if narrow {
		bumpTokens(direct, subs.directHere)
		t.creditPlaces(job, dirPrefixes(paths))
	}
	// Which of this commit's subjects name something of their own, so the
	// words of one compound name are not read as subjects meeting.
	for _, p := range paths {
		t.ties.standing(segmentNames(p))
	}
	noteTogether(t, subs.byPath, subs.deepest, job.Email, path)
	// The commit subject carries the domain vocabulary the filenames often
	// hide: "fix rate-limiter backoff" against a generically named limiter.go.
	// Mine it once per commit, so it weighs less than the per-file paths but
	// still lets ask match what the work was about, not just which files moved.
	m := nested(t.counts, job.Email)
	for _, tok := range phraseTokens(job.Subject) {
		m[tok]++
	}
	// The people the message credits engaged with the same change: a
	// co-author wrote it, a reviewer or acker held the area well enough to
	// judge it. Each gets the commit's subjects the way the author does,
	// because trailers are exactly where the people a commit count never
	// surfaces show up. Only a co-author counts as direct, the same line
	// Direct draws everywhere: they changed the files, a reviewer did not.
	for _, tp := range job.Trailers {
		t.notePerson(tp.Email, tp.Name, job.When)
		t.countSubjects(tp.Email, lately, subs.inCommit)
		if tp.CoAuthor && narrow {
			bumpTokens(nested(t.direct, tp.Email), subs.directHere)
		}
	}
}

// notePerson counts one commit towards a person's total, moves their latest
// activity forward to when, and counts the display name they used, if any.
func (t *tally) notePerson(email, name string, when time.Time) {
	t.workTotals[email]++
	if when.After(t.latest[email]) {
		t.latest[email] = when
	}
	if name != "" {
		nested(t.names, email)[name]++
	}
}

// countSubjects credits a person once with each subject a commit touched, and
// with the same subjects as recent work when the commit is lately. The
// person's counts, and their recent counts when lately, are created even when
// the commit touched nothing.
//
// A commit counts once towards each subject it touched, however many files it
// moved inside that subject. Counting per file made one wide change
// indistinguishable from a history of narrow ones, and that is not a detail:
// on home-assistant/core it reported ownership of ecovacs as having moved to
// somebody with a single twelve-file commit, over the maintainer with
// twenty-seven separate ones. What makes somebody the person to ask is having
// come back to an area, not having touched a lot of it once.
func (t *tally) countSubjects(email string, lately bool, inCommit map[string]bool) {
	m := nested(t.counts, email)
	var fresh map[string]int
	if lately {
		fresh = nested(t.recent, email)
	}
	for tok := range inCommit {
		m[tok]++
		if fresh != nil {
			fresh[tok]++
		}
	}
}

// bumpTokens adds one to each of toks in m.
func bumpTokens(m map[string]int, toks map[string]bool) {
	for tok := range toks {
		m[tok]++
	}
}

// isSweep reports whether a commit changed more directories than direct work
// and directory credit accept.
//
// A sweep is not direct work anywhere. The tie graph already refuses commits
// this broad, and the ownership report leans on Direct being clean of them:
// with sweeps inside it, the ranking needed a discount by career breadth to
// keep sweepers from out-holding every owner, and that discount then handed
// areas to whoever had done least elsewhere. Gate the sweep out here and
// neither correction is needed. Gated on the directories the commit changed,
// not on how many survived into stated tokens: the token count runs lower than
// the directory count whenever names are filtered as scaffolding, and a
// cleanup pass over twenty-five directories was slipping under a token gate of
// eighteen and collecting ownership credit across all of them.
func isSweep(paths []string) bool {
	changedDirs := make(map[string]bool, len(paths))
	for _, name := range paths {
		changedDirs[dirPart(name)] = true
	}
	return len(changedDirs) > maxTogether
}

// creditPlaces gives directory credit, once per commit per prefix, to the
// author and everyone the message credits. Ownership is asked about places,
// not words, and word-level subjects pool every path sharing a name; this is
// the same walk read by place.
func (t *tally) creditPlaces(job commitJob, prefixes map[string]bool) {
	t.creditDirs(prefixes, job.Email, job.When)
	for _, tp := range job.Trailers {
		t.creditDirs(prefixes, tp.Email, job.When)
	}
}

// creditDirs counts one commit by email in each prefix and records the
// arrival there.
func (t *tally) creditDirs(prefixes map[string]bool, email string, when time.Time) {
	for pre := range prefixes {
		nested(t.dirWork, pre)[email]++
		t.enter(pre, email, when)
	}
}

// commitSubjects is what one commit's changed paths say it was about.
type commitSubjects struct {
	// byPath holds each path's directory subjects, in path order.
	byPath [][]string
	// deepest holds each path's deepest-directory subjects, in path order.
	deepest [][]string
	// inCommit is every subject any path touched, directory or file name, so
	// each is counted once for the commit no matter how many files moved.
	inCommit map[string]bool
	// directHere is the subjects the commit's own directories named, rather
	// than a file inside somebody else's area happening to carry the name.
	directHere map[string]bool
}

// subjectsOf reads the subjects a commit's changed paths name.
func subjectsOf(paths []string) commitSubjects {
	s := commitSubjects{
		byPath:     make([][]string, 0, len(paths)),
		deepest:    make([][]string, 0, len(paths)),
		inCommit:   make(map[string]bool, len(paths)*2),
		directHere: make(map[string]bool, len(paths)),
	}
	for _, name := range paths {
		dirs, leaf := pathSubjects(name)
		s.byPath = append(s.byPath, dirs)
		s.deepest = append(s.deepest, deepestSubjects(name))
		stated := statedNames(name)
		for _, tok := range dirs {
			s.inCommit[tok] = true
			if stated[tok] {
				s.directHere[tok] = true
			}
		}
		// The file's own name still counts towards the work, but it does
		// not establish a subject on its own.
		for _, tok := range leaf {
			s.inCommit[tok] = true
		}
	}
	return s
}

// statedNames returns the subjects a path's directories state outright.
//
// The directories a change landed in are where the work demonstrably went, so
// the subjects they name are stated rather than guessed at. Everything that
// tells a real subject from a passing word reads this: without it a repository
// full of expertise reports none of it.
//
// Only a WHOLE directory name is stated, though. The words inside a compound
// one are there so somebody who types "zwave" still finds the zwave_js expert,
// the same way the words of a ticket summary are, and they are subjects for
// exactly the same reason: none. A directory called data_grand_lyon is one
// integration, not four. A word that names a directory of its own somewhere
// else, like energy, is stated by that directory and keeps its standing here.
func statedNames(name string) map[string]bool {
	stated := make(map[string]bool, 8)
	for _, seg := range segmentNames(dirPart(name)) {
		stated[seg] = true
	}
	for _, seg := range patternNames(name) {
		stated[seg] = true
	}
	return stated
}
