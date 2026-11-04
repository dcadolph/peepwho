package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// testCommit returns a commit authored by name and email with the given
// message and number of parents.
func testCommit(hash, name, email, msg string, parents int) *object.Commit {
	c := &object.Commit{
		Hash:    plumbing.NewHash(hash),
		Author:  object.Signature{Name: name, Email: email, When: testScanWhen},
		Message: msg,
	}
	for range parents {
		c.ParentHashes = append(c.ParentHashes, plumbing.NewHash("ff"))
	}
	return c
}

// testScanWhen is the author time every testCommit carries.
var testScanWhen = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// TestCommitScanVisit verifies one step of the log walk: where it stops, what
// it keeps, and what it records as skipped.
func TestCommitScanVisit(t *testing.T) {
	t.Parallel()
	const (
		h1 = "1111111111111111111111111111111111111111"
		h2 = "2222222222222222222222222222222222222222"
	)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		Ctx            context.Context
		Commit         *object.Commit
		Want           error
		WantJobs       []commitJob
		WantSkips      scanSkips
		Scan           commitScan
		WantNewest     string
		WantAuthored   int
		WantPullMerges int
		WantReached    bool
	}{{ // Test 0: A root commit is kept under its normalized email.
		Commit:     testCommit(h1, "Ann", " ANN+ci@Corp.com ", "add parser\n\nbody", 0),
		WantNewest: h1, WantAuthored: 1,
		WantJobs: []commitJob{{
			Hash: plumbing.NewHash(h1), Email: "ann@corp.com", Name: "Ann", When: testScanWhen,
			Subject: "add parser", Root: true,
		}},
	}, { // Test 1: The resume mark stops the walk.
		Scan:       commitScan{stop: h2, maxCommits: 10},
		Commit:     testCommit(h2, "Ann", "ann@corp.com", "add parser", 1),
		Want:       storer.ErrStop,
		WantNewest: h2, WantReached: true,
	}, { // Test 2: The authored cap stops the walk.
		Scan:       commitScan{maxCommits: 1, authored: 1},
		Commit:     testCommit(h1, "Ann", "ann@corp.com", "add parser", 1),
		Want:       storer.ErrStop,
		WantNewest: h1, WantAuthored: 1,
	}, { // Test 3: A canceled walk returns the context's error.
		Ctx:        canceled,
		Commit:     testCommit(h1, "Ann", "ann@corp.com", "add parser", 1),
		Want:       context.Canceled,
		WantNewest: h1,
	}, { // Test 4: A pull request merge is kept with its number and no author.
		Commit:     testCommit(h1, "Ann", "ann@corp.com", "Merge pull request #42 from a/b", 2),
		WantNewest: h1, WantPullMerges: 1,
		WantJobs: []commitJob{{Hash: plumbing.NewHash(h1), When: testScanWhen, Pull: 42}},
	}, { // Test 5: A merge naming no pull request is dropped.
		Commit:     testCommit(h1, "Ann", "ann@corp.com", "Merge branch 'main'", 2),
		WantNewest: h1,
	}, { // Test 6: A bot commit is skipped by name.
		Commit:     testCommit(h1, "dependabot[bot]", "bot@x.com", "bump", 1),
		WantNewest: h1,
		WantSkips:  scanSkips{bots: map[string]int{"dependabot[bot] <bot@x.com>": 1}},
	}, { // Test 7: A commit with no email is skipped as uncreditable.
		Commit:     testCommit(h1, "No Email", "", "who", 1),
		WantNewest: h1,
		WantSkips:  scanSkips{noEmail: 1},
	}, { // Test 8: A later commit leaves the newest mark alone and keeps trailers.
		Scan: commitScan{newest: h2, maxCommits: 10},
		Commit: testCommit(h1, "Ann", "ann@corp.com",
			"pair on it\n\nCo-authored-by: Cy <CY@corp.com>\n", 1),
		WantNewest: h2, WantAuthored: 1,
		WantJobs: []commitJob{{
			Hash: plumbing.NewHash(h1), Email: "ann@corp.com", Name: "Ann", When: testScanWhen,
			Subject:  "pair on it",
			Trailers: []trailerPerson{{Email: "cy@corp.com", Name: "Cy", CoAuthor: true}},
		}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ctx := test.Ctx
			if ctx == nil {
				ctx = context.Background()
			}
			s := test.Scan
			if s.maxCommits == 0 {
				s.maxCommits = 10
			}
			err := s.visit(ctx, test.Commit)
			if !errors.Is(err, test.Want) {
				t.Errorf("visit error = %v, want %v", err, test.Want)
			}
			if diff := cmp.Diff(test.WantJobs, s.jobs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("jobs mismatch (-want +got):\n%s", diff)
			}
			opts := []cmp.Option{cmp.AllowUnexported(scanSkips{}), cmpopts.EquateEmpty()}
			if diff := cmp.Diff(test.WantSkips, s.skips, opts...); diff != "" {
				t.Errorf("skips mismatch (-want +got):\n%s", diff)
			}
			if s.newest != test.WantNewest || s.reached != test.WantReached {
				t.Errorf("newest, reached = %q, %v, want %q, %v",
					s.newest, s.reached, test.WantNewest, test.WantReached)
			}
			if s.authored != test.WantAuthored || s.pullMerges != test.WantPullMerges {
				t.Errorf("authored, pullMerges = %d, %d, want %d, %d",
					s.authored, s.pullMerges, test.WantAuthored, test.WantPullMerges)
			}
		})
	}
}

// TestResumeMark verifies where the next run resumes: nowhere for a window
// that stops short of today, the old mark when nothing new was read, and the
// newest commit otherwise.
func TestResumeMark(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Scan      commitScan
		WantMark  string
		UntilDays int
	}{{ // Test 0: The newest commit is the mark.
		Scan: commitScan{stop: "old", newest: "new"}, WantMark: "new",
	}, { // Test 1: Nothing new keeps the old mark.
		Scan: commitScan{stop: "old"}, WantMark: "old",
	}, { // Test 2: A window short of today records no mark.
		Scan: commitScan{stop: "old", newest: "new"}, UntilDays: 3, WantMark: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			g := &GitHistory{opts: GitOptions{UntilDays: test.UntilDays}}
			if got := g.resumeMark(&test.Scan); got != test.WantMark {
				t.Errorf("resumeMark = %q, want %q", got, test.WantMark)
			}
		})
	}
}

// TestLogWindow verifies the log bounds are measured back from one reading of
// the clock, with an upper bound only when UntilDays is set.
func TestLogWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		WantUntil *time.Time
		WantSince time.Time
		UntilDays int
	}{{ // Test 0: No upper bound by default.
		WantSince: now.AddDate(0, 0, -30),
	}, { // Test 1: UntilDays bounds the window above.
		UntilDays: 7, WantSince: now.AddDate(0, 0, -30),
		WantUntil: func() *time.Time { u := now.AddDate(0, 0, -7); return &u }(),
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			g := &GitHistory{
				opts: GitOptions{SinceDays: 30, UntilDays: test.UntilDays},
				now:  func() time.Time { return now },
			}
			got := g.logWindow()
			if got.Since == nil || !got.Since.Equal(test.WantSince) {
				t.Errorf("Since = %v, want %v", got.Since, test.WantSince)
			}
			if diff := cmp.Diff(test.WantUntil, got.Until); diff != "" {
				t.Errorf("Until mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReportScan verifies the scan names a lost resume mark and where its pull
// request merges are keyed, and says nothing when there is nothing to say.
func TestReportScan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Scan    commitScan
		RepoID  string
		WantLog string
	}{{ // Test 0: Nothing to report.
		Scan: commitScan{stop: "abc", reached: true},
	}, { // Test 1: A mark the history lacks is named.
		Scan:    commitScan{stop: "0123456789abcdef"},
		WantLog: "git: /r has no commit 0123456789ab, reading the whole window\n",
	}, { // Test 2: Merges keyed to a repository name it.
		Scan: commitScan{pullMerges: 2}, RepoID: "acme/widget",
		WantLog: "git: /r: 2 pull request merges keyed to acme/widget\n",
	}, { // Test 3: Merges with no repository say so.
		Scan: commitScan{pullMerges: 1},
		WantLog: "git: /r: 1 pull request merges found, but no remote names the owner/name " +
			"this repository lives under, so their numbers join forge review data only " +
			"where a number is unambiguous across repositories\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var log bytes.Buffer
			g := &GitHistory{opts: GitOptions{Log: &log}}
			g.reportScan("/r", test.RepoID, &test.Scan)
			if diff := cmp.Diff(test.WantLog, log.String()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestScanRepoID verifies a known identity is kept and an unknown one is read
// from the remotes and remembered.
func TestScanRepoID(t *testing.T) {
	t.Parallel()
	repo, err := git.PlainInit(t.TempDir(), false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "origin", URLs: []string{"https://github.com/Acme/Widget.git"},
	}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	tests := []struct {
		Known  map[string]string
		WantID string
	}{{ // Test 0: An unknown identity comes from the origin remote.
		Known: map[string]string{}, WantID: "acme/widget",
	}, { // Test 1: A known identity, even an empty one, is kept.
		Known: map[string]string{"/r": ""}, WantID: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			g := &GitHistory{repoIDs: test.Known}
			if got := g.scanRepoID("/r", repo); got != test.WantID {
				t.Errorf("scanRepoID = %q, want %q", got, test.WantID)
			}
			if got := g.repoIDs["/r"]; got != test.WantID {
				t.Errorf("remembered %q, want %q", got, test.WantID)
			}
		})
	}
}

// testSquashJobs is a window where one GitHub noreply address authored under
// two people's names and each also committed from an address of their own.
func testSquashJobs() []commitJob {
	const shared = "1+squash@users.noreply.github.com"
	return []commitJob{
		{Email: shared, Name: "Ann Lee"},
		{Email: shared, Name: "Bob Ray"},
		{Email: shared, Name: "Cy"},
		{Email: "ann@x.com", Name: "Ann Lee"},
		{Email: "ann@x.com", Name: "Ann Lee"},
		{Email: "ann@old.com", Name: "Ann Lee"},
		{Email: "bob@x.com", Name: "Bob Ray"},
		{Email: "cy@x.com", Name: "Cy"},
		{Email: "anon@x.com", Name: "?!"},
	}
}

// TestMailboxNames verifies names are gathered per email in their letters-only
// form, and a name with no letters is ignored.
func TestMailboxNames(t *testing.T) {
	t.Parallel()
	got := mailboxNames(testSquashJobs())
	want := map[string]map[string]bool{
		"1+squash@users.noreply.github.com": {"annlee": true, "bobray": true, "cy": true},
		"ann@x.com":                         {"annlee": true},
		"ann@old.com":                       {"annlee": true},
		"bob@x.com":                         {"bobray": true},
		"cy@x.com":                          {"cy": true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestSharedMailboxes verifies only a GitHub noreply address with two or more
// names counts as shared.
func TestSharedMailboxes(t *testing.T) {
	t.Parallel()
	got := sharedMailboxes(map[string]map[string]bool{
		"1+squash@users.noreply.github.com": {"annlee": true, "bobray": true},
		"2+solo@users.noreply.github.com":   {"annlee": true},
		"team@x.com":                        {"annlee": true, "bobray": true},
	})
	want := map[string]bool{"1+squash@users.noreply.github.com": true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestOwnMailboxes verifies each name's own addresses are counted, leaving out
// shared ones and any address used under more than one name.
func TestOwnMailboxes(t *testing.T) {
	t.Parallel()
	jobs := append(testSquashJobs(), commitJob{Email: "bob@x.com", Name: "Bobby"})
	namesOf := mailboxNames(jobs)
	got := ownMailboxes(jobs, namesOf, sharedMailboxes(namesOf))
	want := map[string]map[string]int{
		"annlee": {"ann@x.com": 2, "ann@old.com": 1},
		"cy":     {"cy@x.com": 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestPrimaryMailbox verifies the most active address wins and a tie falls to
// the alphabetically first.
func TestPrimaryMailbox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Emails    map[string]int
		WantEmail string
	}{{ // Test 0: The most active address wins.
		Emails: map[string]int{"a@x.com": 1, "b@x.com": 3}, WantEmail: "b@x.com",
	}, { // Test 1: A tie falls to the alphabetically first.
		Emails: map[string]int{"b@x.com": 2, "a@x.com": 2}, WantEmail: "a@x.com",
	}, { // Test 2: No addresses yields none.
		Emails: nil, WantEmail: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := primaryMailbox(test.Emails); got != test.WantEmail {
				t.Errorf("primaryMailbox = %q, want %q", got, test.WantEmail)
			}
		})
	}
}

// TestUnshareSquashMailboxesSteps verifies the steps combine: a multi-word
// name under a shared address moves to its own primary address, while a
// single-word name stays put.
func TestUnshareSquashMailboxesSteps(t *testing.T) {
	t.Parallel()
	got := unshareSquashMailboxes(testSquashJobs())
	want := testSquashJobs()
	want[0].Email = "ann@x.com"
	want[1].Email = "bob@x.com"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
