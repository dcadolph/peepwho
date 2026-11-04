// Reading one repository: scanning commits from the resume mark, diffing them
// in parallel, and tallying who touched what, where, and when.

package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/dcadolph/peepwho/internal/util"
)

// readRepo walks one repository's log, accumulating per-author topic counts,
// display names, and latest activity. Authors are canonicalized through the
// repository's .mailmap so one person's several emails merge. It returns the
// number of commits read.
func (g *GitHistory) readRepo(
	ctx context.Context,
	path string,
	counts map[string]map[string]int,
	names map[string]map[string]int,
	latest map[string]time.Time,
	curated map[string]bool,
	recent map[string]map[string]int,
	direct map[string]map[string]int,
	ties *togetherIndex,
) (read, skipped int, err error) {
	jobs, mark, skips, err := g.scanCommits(ctx, path)
	if err != nil {
		return 0, 0, err
	}
	if mark != "" {
		g.marks[path] = mark
	}
	skips.report(g.opts.Log, path)
	jobs = unshareSquashMailboxes(jobs)
	return g.diffCommits(ctx, path, g.repoIDs[path], jobs,
		counts, names, latest, curated, recent, direct, ties)
}

// unshareSquashMailboxes re-keys commits caught in a shared squash mailbox: a
// GitHub noreply address that authored commits under two or more different
// names is not a person, it is an artifact of squash-merging, and every commit
// under it inflates whoever the address nominally belongs to with other
// people's work. Measured on esphome, one such address carried three real
// maintainers and cost four ownership answers.
//
// Each commit under a shared mailbox moves to the author name's own email when
// the window shows one: the email that authored commits under that name alone.
// A person with two such emails gets their most active one, which is the same
// human either way. A single-word name is never re-keyed, since a bare given
// name identifies nobody, and a commit whose name has no unique email stays
// where it was: mis-keyed but at least untouched by guesswork.
func unshareSquashMailboxes(jobs []commitJob) []commitJob {
	namesOf := mailboxNames(jobs)
	shared := sharedMailboxes(namesOf)
	if len(shared) == 0 {
		return jobs
	}
	own := ownMailboxes(jobs, namesOf, shared)
	for i, j := range jobs {
		if !shared[j.Email] {
			continue
		}
		nm := normName(j.Name)
		if nm == "" || !strings.Contains(strings.TrimSpace(j.Name), " ") {
			continue
		}
		if best := primaryMailbox(own[nm]); best != "" {
			jobs[i].Email = best
		}
	}
	return jobs
}

// mailboxNames returns, per author email, the normalized names that authored
// commits under it.
func mailboxNames(jobs []commitJob) map[string]map[string]bool {
	namesOf := make(map[string]map[string]bool)
	for _, j := range jobs {
		nm := normName(j.Name)
		if nm == "" {
			continue
		}
		if namesOf[j.Email] == nil {
			namesOf[j.Email] = make(map[string]bool)
		}
		namesOf[j.Email][nm] = true
	}
	return namesOf
}

// sharedMailboxes returns the GitHub noreply addresses that authored commits
// under two or more different names.
func sharedMailboxes(namesOf map[string]map[string]bool) map[string]bool {
	shared := make(map[string]bool)
	for email, nms := range namesOf {
		if _, ok := util.GitHubNoreplyLogin(email); ok && len(nms) >= 2 {
			shared[email] = true
		}
	}
	return shared
}

// ownMailboxes counts, per normalized name, the commits under each email that
// name alone authored under. An email uniquely named by one name, outside the
// shared set, is that person's own mailbox, and the counts pick a primary.
func ownMailboxes(
	jobs []commitJob, namesOf map[string]map[string]bool, shared map[string]bool,
) map[string]map[string]int {
	own := make(map[string]map[string]int)
	for _, j := range jobs {
		nm := normName(j.Name)
		if nm == "" || shared[j.Email] || len(namesOf[j.Email]) != 1 {
			continue
		}
		if own[nm] == nil {
			own[nm] = make(map[string]int)
		}
		own[nm][j.Email]++
	}
	return own
}

// primaryMailbox returns the most active of one person's own emails, ties
// falling to the alphabetically first, or empty when there are none.
func primaryMailbox(emails map[string]int) string {
	best, bestN := "", 0
	for email, n := range emails {
		if n > bestN || (n == bestN && email < best) {
			best, bestN = email, n
		}
	}
	return best
}

// normName reduces a display name to its letters, so J. Nick Koston and
// J.Nick Koston are one name.
func normName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// maxPullDirs bounds how many directories one pull request may credit its
// reviewers with. A release merge or a tree-wide rename touches everything,
// and reviewing it is not owning all of it, which is the same line the sweep
// gate draws for authorship.
const maxPullDirs = 25

// maxNamedSkips caps how many automation accounts one report line names.
const maxNamedSkips = 15

// scanSkips records what a scan left out on purpose, so the operator hears
// about every dropped commit by name rather than finding a person missing from
// the map and having to guess why.
type scanSkips struct {
	// bots counts skipped commits per automation account, keyed by the
	// account's "name <email>" form.
	bots map[string]int
	// noEmail counts commits with no author email, which cannot be credited to
	// anyone.
	noEmail int
}

// note records one skipped commit under the author it belonged to.
func (s *scanSkips) note(name, email string) {
	if email == "" {
		s.noEmail++
		return
	}
	if s.bots == nil {
		s.bots = make(map[string]int)
	}
	s.bots[name+" <"+email+">"]++
}

// report writes what was skipped and why, naming each automation account. A
// who-knows-what map that silently drops people is worse than one that says
// who it left out.
func (s *scanSkips) report(w io.Writer, path string) {
	if len(s.bots) > 0 {
		names := make([]string, 0, len(s.bots))
		total := 0
		for name, n := range s.bots {
			names = append(names, name)
			total += n
		}
		sort.Strings(names)
		if len(names) > maxNamedSkips {
			names = append(names[:maxNamedSkips],
				fmt.Sprintf("and %d more", len(s.bots)-maxNamedSkips))
		}
		fmt.Fprintf(w, "git: %s: skipped %d commits from automation accounts: %s\n",
			path, total, strings.Join(names, ", "))
	}
	if s.noEmail > 0 {
		fmt.Fprintf(w, "git: %s: %d commits have no author email and are credited to nobody\n",
			path, s.noEmail)
	}
}

// commitJob is one commit worth walking: everything cheap about it, read once
// during the scan so the parallel phase only has to do the expensive part.
type commitJob struct {
	// Hash identifies the commit to diff.
	Hash plumbing.Hash
	// Email is the author, already resolved through the mailmap.
	Email string
	// Name is the author's display name at that commit.
	Name string
	// When is when the commit was authored.
	When time.Time
	// Subject is the commit's first line, mined for vocabulary the filenames
	// do not carry.
	Subject string
	// Trailers are the other people the commit message credits: co-authors,
	// reviewers, ackers. They are the review signal a repository carries
	// offline, and the people they name are often exactly the ones a commit
	// count never surfaces.
	Trailers []trailerPerson
	// Root marks a commit with no parent, whose diff is its whole tree.
	Root bool
	// Pull is the pull request number a merge commit landed, or zero. A merge
	// credits nobody with authorship, but it is the one place the history
	// records which change belongs to which pull request, and that link is
	// what lets a reviewer be credited with the places they reviewed. The
	// walk pairs it with the repository identity into a PullRef, since the
	// number alone collides across repositories.
	Pull int
}

// pullMergeRe matches the merge subject GitHub writes when a pull request
// lands, which is where the number-to-change link lives.
var pullMergeRe = regexp.MustCompile(`^Merge pull request #(\d+)`)

// pullNumber returns the pull request a merge commit landed, or zero.
func pullNumber(subject string) int {
	m := pullMergeRe.FindStringSubmatch(subject)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// originRepoID returns the lowercase "owner/name" a repository's remotes say
// it lives under on its forge, or empty when no remote parses. The origin
// remote wins; other remotes are tried in name order so two reads of the same
// clone agree.
func originRepoID(repo *git.Repository) string {
	remotes, err := repo.Remotes()
	if err != nil {
		return ""
	}
	sort.Slice(remotes, func(i, j int) bool {
		a, b := remotes[i].Config().Name, remotes[j].Config().Name
		if (a == "origin") != (b == "origin") {
			return a == "origin"
		}
		return a < b
	})
	for _, r := range remotes {
		for _, u := range r.Config().URLs {
			if id := remoteRepoID(u); id != "" {
				return id
			}
		}
	}
	return ""
}

// remoteRepoID extracts the lowercase "owner/name" from one git remote URL,
// or empty when the URL does not name a forge repository. It reads the https,
// ssh, git, and scp-like forms clones actually carry; a local path remote
// names no forge repository and yields nothing.
func remoteRepoID(raw string) string {
	s := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	s = strings.TrimSuffix(s, ".git")
	var path string
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		switch strings.ToLower(scheme) {
		case "http", "https", "ssh", "git":
		default:
			return ""
		}
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		path = strings.TrimPrefix(u.Path, "/")
	} else if host, after, ok := strings.Cut(s, ":"); ok && strings.Contains(host, "@") {
		// The scp-like form: git@host:owner/name. Requiring the user part keeps
		// a Windows drive path or a bare host:port from minting a repository.
		path = after
	} else {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return ""
	}
	return strings.ToLower(owner + "/" + name)
}

// trailerPerson is one person a commit trailer credits.
type trailerPerson struct {
	// Email is the person's address, mailmap-resolved and lowercased.
	Email string
	// Name is their display name at that trailer.
	Name string
	// CoAuthor marks a Co-authored-by trailer: they changed the files, so
	// their credit is the author's. A review trailer holds the area without
	// having changed it, which is the same distinction Direct draws.
	CoAuthor bool
}

// trailerRe matches the credit trailers a commit message carries. Only
// trailers that name a person who engaged with the change count: Cc and
// Signed-off-by are routing and process, not evidence of holding anything.
var trailerRe = regexp.MustCompile(
	"(?mi)^(Co-authored-by|Reviewed-by|Acked-by|Tested-by|Suggested-by):" +
		"[ \\t]*([^<\\n]*?)[ \\t]*<([^>\\n]+)>[ \\t]*$")

// parseTrailers extracts credited people from a commit message, resolved
// through the mailmap, with automation accounts skipped the same way authors
// are, and the author themselves never double-credited. Emails are
// canonicalized the way author emails are, so a plus-tagged trailer address
// never becomes a second holder beside the untagged one.
func parseTrailers(msg string, mm mailmap, authorEmail string, skips *scanSkips) []trailerPerson {
	matches := trailerRe.FindAllStringSubmatch(msg, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []trailerPerson
	seen := map[string]bool{authorEmail: true}
	for _, m := range matches {
		kind, name, email := strings.ToLower(m[1]), strings.TrimSpace(m[2]), m[3]
		if mm != nil {
			name, email = mm.resolve(name, email)
		}
		email = util.NormalizeEmail(email)
		if email == "" || seen[email] {
			continue
		}
		if isBotAuthor(name, email) {
			skips.note(name, email)
			seen[email] = true
			continue
		}
		seen[email] = true
		out = append(out, trailerPerson{
			Email: email, Name: name, CoAuthor: kind == "co-authored-by",
		})
	}
	return out
}

// scanCommits walks the log and keeps the commits worth diffing. Reading the
// log is thousands of commits a second because it touches only commit objects;
// everything slow happens afterwards, per commit and independently, which is
// what makes the second phase worth running in parallel.
func (g *GitHistory) scanCommits(ctx context.Context, path string) ([]commitJob, string, scanSkips, error) {
	var skips scanSkips
	repo, closeRepo, err := openRepo(path)
	if err != nil {
		return nil, "", skips, fmt.Errorf("git: open %s: %w", path, err)
	}
	defer func() { _ = closeRepo() }()

	mm := loadMailmap(path)
	repoID := g.scanRepoID(path, repo)
	iter, err := repo.Log(g.logWindow())
	if err != nil {
		return nil, "", skips, fmt.Errorf("git: log %s: %w", path, err)
	}
	defer iter.Close()

	s := commitScan{mm: mm, stop: g.opts.StopAt[path], maxCommits: g.opts.MaxCommits}
	err = iter.ForEach(func(c *object.Commit) error { return s.visit(ctx, c) })
	// A shallow clone ends at a commit whose parent was never fetched, which
	// the walker reports as a missing object. That is where the history this
	// copy has runs out, not a broken repository, so keep what was read.
	if err != nil && isShallow(path) && errors.Is(err, plumbing.ErrObjectNotFound) {
		err = nil
	}
	if err != nil {
		return nil, "", s.skips, fmt.Errorf("git: log %s: %w", path, err)
	}
	g.reportScan(path, repoID, &s)
	return s.jobs, g.resumeMark(&s), s.skips, nil
}

// scanRepoID returns the repository identity for path. The identity keys pull
// request merges, so review data fetched from the forge under "owner/name"
// joins the directories those merges changed here. Fetch resolves it up front;
// the fallback covers a walk driven outside Fetch.
func (g *GitHistory) scanRepoID(path string, repo *git.Repository) string {
	repoID, ok := g.repoIDs[path]
	if !ok {
		repoID = originRepoID(repo)
		g.repoIDs[path] = repoID
	}
	return repoID
}

// logWindow returns the log options that bound a scan to the configured
// window, measured back from one reading of the clock.
func (g *GitHistory) logWindow() *git.LogOptions {
	now := g.now()
	since := now.AddDate(0, 0, -g.opts.SinceDays)
	opts := &git.LogOptions{Since: &since}
	if g.opts.UntilDays > 0 {
		until := now.AddDate(0, 0, -g.opts.UntilDays)
		opts.Until = &until
	}
	return opts
}

// commitScan is the state one log walk builds up as it visits commits newest
// first.
type commitScan struct {
	// mm resolves author and trailer identities, nil when the repository has no
	// mailmap.
	mm mailmap
	// stop is the commit an earlier run read up to, empty for a full read.
	stop string
	// maxCommits caps how many authored commits the scan keeps.
	maxCommits int
	// jobs are the commits kept for diffing, newest first.
	jobs []commitJob
	// skips records the commits left out on purpose.
	skips scanSkips
	// authored counts the authored commits kept so far.
	authored int
	// pullMerges counts the pull request merges kept so far.
	pullMerges int
	// newest is the first commit visited, before any filtering.
	newest string
	// reached reports whether the walk arrived at stop.
	reached bool
}

// visit takes in one commit of the log walk, keeping it as a job when it is
// worth diffing, and returns storer.ErrStop when the walk should end.
func (s *commitScan) visit(ctx context.Context, c *object.Commit) error {
	// The newest commit seen, before any filtering, is where the next run
	// stops. Marking the newest one KEPT would re-read the merges and bot
	// commits above it every time.
	if s.newest == "" {
		s.newest = c.Hash.String()
	}
	// Everything from here down was read by an earlier run. Commits are
	// walked newest first, so this is the whole of the saving.
	if s.stop != "" && c.Hash.String() == s.stop {
		s.reached = true
		return storer.ErrStop
	}
	// The cap counts authored commits, which is what it is about. Merge
	// commits are walked only to learn which pull request changed which
	// places, and letting them consume the cap starved a busy repository
	// of the authorship it exists to read.
	if s.authored >= s.maxCommits {
		return storer.ErrStop
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name, email := c.Author.Name, c.Author.Email
	if s.mm != nil {
		name, email = s.mm.resolve(name, email)
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if c.NumParents() > 1 {
		s.keepMerge(c)
		return nil
	}
	if email == "" || isBotAuthor(name, email) {
		s.skips.note(name, email)
		return nil
	}
	s.authored++
	// The tally keys on the canonical mailbox, the same form the identity
	// graph stores, so a plus-tagged commit address never stands beside the
	// untagged one as a second holder in the systems file.
	email = util.NormalizeEmail(email)
	s.jobs = append(s.jobs, commitJob{
		Hash: c.Hash, Email: email, Name: name, When: c.Author.When,
		Subject:  commitSubject(c.Message),
		Trailers: parseTrailers(c.Message, s.mm, email, &s.skips),
		Root:     c.NumParents() == 0,
	})
	return nil
}

// keepMerge keeps a merge commit when it names a pull request. A merge is not
// authorship, so nobody is credited for it. It is walked only when it names a
// pull request, to learn which places that pull request changed. It is kept
// even when the clone's remotes name no repository: the resolver may still
// match its number when that number is unambiguous, and dropping it here would
// silently cost an unremoted data-room copy all its review credit.
func (s *commitScan) keepMerge(c *object.Commit) {
	if pull := pullNumber(commitSubject(c.Message)); pull > 0 {
		s.pullMerges++
		s.jobs = append(s.jobs, commitJob{Hash: c.Hash, When: c.Author.When, Pull: pull})
	}
}

// reportScan writes what a finished scan found worth saying: a resume mark the
// history no longer holds, and where its pull request merges are keyed.
func (g *GitHistory) reportScan(path, repoID string, s *commitScan) {
	// A mark this history no longer contains means it was rewritten under us,
	// so what was just read is the whole window and not an increment.
	if s.stop != "" && !s.reached {
		fmt.Fprintf(g.opts.Log, "git: %s has no commit %.12s, reading the whole window\n", path, s.stop)
	}
	// Name where the pull request link points, or that it points nowhere: a
	// clone whose remote disagrees with --github-repo silently loses review
	// credit otherwise, and silence is the failure mode this line exists for.
	switch {
	case s.pullMerges > 0 && repoID != "":
		fmt.Fprintf(g.opts.Log, "git: %s: %d pull request merges keyed to %s\n",
			path, s.pullMerges, repoID)
	case s.pullMerges > 0:
		fmt.Fprintf(g.opts.Log,
			"git: %s: %d pull request merges found, but no remote names the owner/name this "+
				"repository lives under, so their numbers join forge review data only where a "+
				"number is unambiguous across repositories\n", path, s.pullMerges)
	}
}

// resumeMark returns the commit the next run of a finished scan should stop at.
func (g *GitHistory) resumeMark(s *commitScan) string {
	// A window that stops short of today did not walk to the tip, so its newest
	// commit is not a safe place to resume: commits between it and the tip are
	// never reached by either run. Rather than record a position that would skip
	// them, record none, and let the next run read the window again.
	if g.opts.UntilDays > 0 {
		return ""
	}
	// Nothing new means the previous mark still stands.
	if s.newest == "" {
		return s.stop
	}
	return s.newest
}

// tally is one worker's share of the walk, kept to itself so the workers never
// contend over the same maps and are merged once at the end.
type tally struct {
	// counts is per-author topic weight.
	counts map[string]map[string]int
	// names counts how often each author signed with each display name.
	names map[string]map[string]int
	// latest is each author's most recent commit time.
	latest map[string]time.Time
	// curated are the tokens this worker saw in a file path rather than only in
	// a commit subject, which is the difference between demonstrated work and
	// a word somebody wrote.
	curated map[string]bool
	// recent counts, per author, the subjects they have worked on lately. See
	// recentWindowDays.
	recent map[string]map[string]int
	// direct counts, per author, the subjects their own changed directories
	// named, as against a file elsewhere carrying the name.
	direct map[string]map[string]int
	// ties is this worker's share of what was worked on together.
	ties *togetherIndex
	// dirWork counts, per directory prefix and per author, the commits that
	// touched it: the directory-scoped view of the same walk. Subjects answer
	// what somebody knows; directories answer what they own, and conflating
	// the two made ownership questions pool every path sharing a name.
	dirWork map[string]map[string]float64
	// workTotals counts every commit per author, sweeps included, which is
	// the breadth a directory score is discounted by.
	workTotals map[string]float64
	// pullDirs records which directories each pull request changed, learned
	// from the merge commit that landed it.
	pullDirs map[PullRef][]string
	// entered holds, per directory and author, the earliest commit times that
	// author landed there, kept only up to RampChanges. It is what lets a
	// reader be told how long getting established in an unfamiliar area has
	// actually taken in their own history, rather than being told a forecast.
	entered map[rampKey][]int64
	// firstSeen is each author's earliest commit anywhere in the repository,
	// which is what separates somebody moving into a new area from somebody
	// whose history simply starts at the window's edge.
	firstSeen map[string]int64
	// read is how many commits this worker took in.
	read int
	// skipped is how many it could not diff.
	skipped int
}

// commitSubject returns the first line of a commit message, the summary that
// carries the domain vocabulary, without the body or trailers such as
// Signed-off-by that would only add name and email noise.
func commitSubject(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i]
	}
	return msg
}

// automationNames are automation accounts whose names carry no "bot" word at
// all. GitHub's coding agent commits as plain "Copilot", and on a real
// repository it was named as the sole holder of a subject: an assessment that
// tells an acquirer a machine is the only one who understands something is
// worse than useless. Names here are matched whole, case-insensitively, so a
// person called Robin Copilot-Smith is untouched.
var automationNames = map[string]bool{
	"copilot": true, "github-actions": true, "github actions": true,
	"semantic-release": true, "release-please": true, "snyk": true,
	"imgbot": true, "allcontributors": true, "codecov": true,
}

// automationEmails are the addresses coding agents sign their co-author
// trailers with. Their display names change with every model release and some
// are ordinary first names, so the address is the only stable mark. On go-git a
// coding agent credited through these trailers was ranked a holder of a
// subject.
var automationEmails = map[string]bool{
	"noreply@anthropic.com": true, "noreply@openai.com": true,
	"cursoragent@cursor.com": true,
}

// IsAutomationName reports whether a display name belongs to an automation
// account. It is the name-only half of the author check, exported because a
// benchmark judging peepwho has to hold its ground truth to the same standard:
// scoring a predictor against a future full of bots rewards whichever
// predictor failed to filter them.
func IsAutomationName(name string) bool {
	if strings.HasSuffix(name, "[bot]") {
		return true
	}
	if automationNames[strings.ToLower(strings.TrimSpace(name))] {
		return true
	}
	return hasBotWord(name)
}

// isBotAuthor reports whether a commit author is an automation account, whose
// activity says nothing about human expertise.
//
// The [bot] suffix is GitHub's convention and not everyone's. PyTorch lands
// every change re-authored as "PyTorch MergeBot", which made a robot the top
// committer of most of the repository and the ownership report name it as the
// person areas had drifted to. A name is automation when "bot" stands alone as
// a word in it, hyphens included, so facebook-github-bot and MergeBot are
// caught while Talbot and Abbott stay people.
func isBotAuthor(name, email string) bool {
	if strings.HasSuffix(name, "[bot]") || strings.Contains(email, "[bot]") {
		return true
	}
	if automationEmails[strings.ToLower(strings.TrimSpace(email))] {
		return true
	}
	local := email
	if i := strings.IndexByte(local, '@'); i >= 0 {
		local = local[:i]
	}
	// A GitHub noreply local part is "12345+login", and the login is what
	// names the account.
	if _, after, ok := strings.Cut(local, "+"); ok {
		local = after
	}
	if automationNames[strings.ToLower(strings.TrimSpace(name))] ||
		automationNames[strings.ToLower(strings.TrimSpace(local))] {
		return true
	}
	return hasBotWord(name) || hasBotWord(local)
}

// hasBotWord reports whether "bot" appears as its own word, where camel case,
// hyphens, dots, underscores and spaces all separate words.
func hasBotWord(s string) bool {
	s = strings.ToLower(s)
	for _, sep := range []string{"-", "_", ".", " ", "+"} {
		s = strings.ReplaceAll(s, sep, " ")
	}
	for _, w := range strings.Fields(s) {
		if w == "bot" || w == "robot" ||
			strings.HasSuffix(w, "bot") && (w == "mergebot" || w == "dependabot" || w == "renovatebot") {
			return true
		}
	}
	return false
}

// Ramp bounds. A person is "established" in an area once they have landed
// RampChanges changes in it, and rampPairs caps how many person-and-directory
// pairs are tracked so a repository with a wide tree cannot grow the walk's
// memory without bound.
const (
	// RampChanges is how many changes in an area count as an established
	// footing. It is exported because a report that says "five changes" must
	// take the number from the walk rather than restating it.
	RampChanges = 5
	rampPairs   = 250000
)

// rampKey is one person's arrival in one directory.
type rampKey struct {
	// Dir is the directory prefix, before any repository label is applied.
	Dir string
	// Email is the author, already resolved through the mailmap.
	Email string
}

// enter records that an author landed a change in a directory at a given time,
// keeping only the earliest RampChanges times for that pair.
//
// Only the earliest matter: the question is how long the stretch from a first
// change to an established footing took, and everything after that is a person
// already established.
func (t *tally) enter(dir, email string, when time.Time) {
	if email == "" {
		return
	}
	k := rampKey{Dir: dir, Email: email}
	times, known := t.entered[k]
	if !known && len(t.entered) >= rampPairs {
		return
	}
	at := when.Unix()
	if len(times) >= RampChanges && at >= times[len(times)-1] {
		return
	}
	times = append(times, at)
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) > RampChanges {
		times = times[:RampChanges]
	}
	t.entered[k] = times
}

// Ramp is one person's arrival in one directory: when they first changed it,
// and when they reached an established footing there, if they ever did.
type Ramp struct {
	// Dir is the directory, carrying its repository label when several were
	// read.
	Dir string
	// Email is the person, as the commit history spells them.
	Email string
	// First is when they first changed anything under it.
	First time.Time
	// Established is when they landed their RampChanges'th change there, zero
	// when they never did within the window.
	Established time.Time
	// Changes is how many changes were counted toward that, capped at
	// RampChanges.
	Changes int
	// Newcomer reports whether they were already committing elsewhere in the
	// repository before this arrival.
	//
	// It is the whole honesty of the measurement. Without it, somebody whose
	// history simply begins at the window's edge looks like a newcomer who
	// arrived and got established instantly, and the reported time collapses
	// toward nothing. Only a person observed working before they first touched
	// the area is known to have been new to it.
	Newcomer bool
}

// Ramps returns every arrival the walk observed, for a caller that wants to
// say how long getting established has taken in this history rather than
// predicting how long it would take next time.
func (g *GitHistory) Ramps() []Ramp { return g.ramps }

// earliestTimes sorts and keeps the earliest RampChanges entries, which is how
// two workers' partial views of one pair combine into the pair's real start.
func earliestTimes(times []int64) []int64 {
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if len(times) > RampChanges {
		times = times[:RampChanges]
	}
	return times
}

// arrivalsOf turns the merged arrival tally into the reportable form, scoping
// each directory the way the place tally scopes it so the two name the same
// places.
func arrivalsOf(
	entered map[rampKey][]int64, firstSeen map[string]int64, scoped func(string) string,
) []Ramp {
	out := make([]Ramp, 0, len(entered))
	for k, times := range entered {
		if len(times) == 0 {
			continue
		}
		r := Ramp{
			Dir:     scoped(k.Dir),
			Email:   k.Email,
			First:   time.Unix(times[0], 0).UTC(),
			Changes: len(times),
			// Strictly before: a person's very first commit in the repository
			// landing in this directory says nothing about moving into it.
			Newcomer: firstSeen[k.Email] != 0 && firstSeen[k.Email] < times[0],
		}
		if len(times) >= RampChanges {
			r.Established = time.Unix(times[RampChanges-1], 0).UTC()
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Email < out[j].Email
	})
	return out
}
