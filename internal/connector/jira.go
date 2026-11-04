package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/jira"
	"github.com/dcadolph/peepwho/internal/util"
)

// JiraOptions configures the Jira connector.
type JiraOptions struct {
	// Projects scopes the search to these project keys.
	Projects []string
	// JQL overrides the query entirely when set.
	JQL string
	// MaxIssues caps issues read; zero uses a default.
	MaxIssues int
	// Episodes records resolved issues, so recall can point back at the
	// ticket that settled something.
	Episodes bool
	// Server selects a self-hosted Jira Server or Data Center deployment, which
	// speaks the v2 API and authenticates with a bearer token or not at all,
	// rather than Cloud's v3 API and email-plus-token.
	Server bool
	// Since, when set, limits the search to issues updated at or after it, read
	// oldest first, for an incremental re-index. It is ignored when JQL overrides
	// the query, since that query is authoritative.
	Since time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// withDefaults fills the log writer and issue cap when unset.
func (o JiraOptions) withDefaults() JiraOptions {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.MaxIssues <= 0 {
		o.MaxIssues = 1000
	}
	return o
}

// jiraProgressEvery reports progress each time this many more issues arrive, so
// a long search shows movement without flooding the log.
const jiraProgressEvery = 100

// Jira is a Source that ingests issues and weights the assignee and reporter by
// the components, labels, summary words, and project of the issues they handle.
type Jira struct {
	// client calls the Jira API.
	client *jira.Client
	// opts holds the resolved options.
	opts JiraOptions
	// episodes holds the resolved issues seen by the last Fetch.
	episodes []episode.Episode
}

// NewJira returns a Jira connector for the site, authenticating with an email
// and API token.
func NewJira(baseURL, email, token string, opts JiraOptions) *Jira {
	o := opts.withDefaults()
	progress := jira.WithProgress(util.ProgressWriter(o.Log, "jira: fetched", jiraProgressEvery))
	var client *jira.Client
	if o.Server {
		// Server and Data Center: v2 API, bearer token, or anonymous for a
		// public tracker where email and token are both empty.
		client = jira.NewServer(baseURL, token, progress)
	} else {
		client = jira.New(baseURL, email, token, progress)
	}
	return &Jira{client: client, opts: o}
}

// NewJiraWithClient returns a Jira connector using a preconfigured client.
// Tests use it to inject a client pointed at a mock server.
func NewJiraWithClient(client *jira.Client, opts JiraOptions) *Jira {
	if client == nil {
		panic("connector: NewJiraWithClient requires a non-nil client")
	}
	return &Jira{client: client, opts: opts.withDefaults()}
}

// Ping verifies the credentials with a cheap current-user call, so a wizard can
// confirm the site, email, and token before committing to a full index.
func (j *Jira) Ping(ctx context.Context) error {
	return j.client.Ping(ctx)
}

// Fetch searches issues and returns one record per person, weighted by topic.
func (j *Jira) Fetch(ctx context.Context) ([]Record, error) {
	j.episodes = nil
	query := j.jql(j.client.UserLocation(ctx))
	issues, err := j.client.Search(ctx, query, j.opts.MaxIssues)
	switch {
	case errors.Is(err, jira.ErrTruncated):
		// The read is partial, not broken: index what came back and say what
		// did not, because a tracker silently indexed as whole is the failure
		// mode this product cannot afford.
		fmt.Fprintf(j.opts.Log,
			"jira: %v. Everything past the --max-issues cap of %d is invisible to this index; "+
				"raise --max-issues to read it\n", err, j.opts.MaxIssues)
	case err != nil:
		return nil, fmt.Errorf("jira search: %w", err)
	}
	fmt.Fprintf(j.opts.Log, "jira: %d issues for %q\n", len(issues), query)

	counts := make(map[string]map[string]int)
	ties := newTogether()
	users := make(map[string]jira.User)
	latest := make(map[string]time.Time)
	// Tokens any issue stated as a label or component. Everything else mined from
	// a summary or description stays a weak topic.
	curated := make(map[string]bool)
	bump := func(u *jira.User, tokens []string, t time.Time) {
		if u == nil {
			return
		}
		key := jiraUserKey(*u)
		if key == "" {
			return
		}
		c := counts[key]
		if c == nil {
			c = make(map[string]int)
			counts[key] = c
		}
		for _, tok := range tokens {
			if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
				c[tok]++
			}
		}
		if t.After(latest[key]) {
			latest[key] = t
		}
		users[key] = *u
	}

	for _, is := range issues {
		if j.opts.Episodes {
			if ep, ok := issueEpisode(j.client.BaseURL(), is); ok {
				j.episodes = append(j.episodes, ep)
			}
		}
		tokens := issueTopics(is)
		updated := jiraTime(is.Fields.Updated)
		bump(is.Fields.Assignee, tokens, updated)
		// One credit per person per issue: a ticket somebody filed and assigned
		// to themselves is one piece of work, and counting the roles separately
		// doubled every token of it. Confluence draws the same line for a page
		// created and last edited by one person.
		assigneeKey := jiraUserKey(userOrEmpty(is.Fields.Assignee))
		if jiraUserKey(userOrEmpty(is.Fields.Reporter)) != assigneeKey || assigneeKey == "" {
			bump(is.Fields.Reporter, tokens, updated)
		}
		stated := make([]string, 0, 4)
		for _, tok := range issueCuratedTopics(is) {
			if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
				curated[tok] = true
				stated = append(stated, tok)
			}
		}
		// What one issue states is worked on together, and whoever it fell to
		// is who worked across it. Only the stated subjects count: the words of
		// a summary are prose, and pairing those would tie a subject to every
		// turn of phrase somebody used near it.
		who := jiraUserKey(userOrEmpty(is.Fields.Assignee))
		if who == "" {
			who = jiraUserKey(userOrEmpty(is.Fields.Reporter))
		}
		ties.note(stated, who, is.Fields.Project.Key)
	}
	records := make([]Record, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		c := counts[key]
		rec := jiraPersonRecord(users[key], nil)
		rec.Topics, rec.WeakTopics = splitCurated(expandTopics(c), curated)
		rec.Time = latest[key]
		records = append(records, rec)
	}
	records = append(records, ties.records("jira")...)
	return records, nil
}

// jiraTime parses a Jira timestamp, returning the zero time when none of the
// accepted layouts match. Jira Cloud sends its own ISO 8601 form with a
// colon-less zone, such as "2026-07-05T12:34:56.789-0500", but some deployments
// and fixtures use RFC 3339 with a "Z" zone or without fractional seconds.
func jiraTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02T15:04:05.999-0700",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// jql returns the query: an explicit JQL, or a project scope, or all issues. An
// incremental read (Since set) restricts to issues updated at or after the
// watermark and orders them oldest first, so a capped read leaves the newest
// issues for the next run and never skips a gap; a full read keeps the
// newest-first order that best fills a fresh index up to the cap.
func (j *Jira) jql(loc *time.Location) string {
	if strings.TrimSpace(j.opts.JQL) != "" {
		return j.opts.JQL
	}
	var scope string
	if len(j.opts.Projects) > 0 {
		quoted := make([]string, len(j.opts.Projects))
		for i, p := range j.opts.Projects {
			quoted[i] = `"` + p + `"`
		}
		scope = "project in (" + strings.Join(quoted, ",") + ")"
	}
	if !j.opts.Since.IsZero() {
		// JQL reads the wall-clock below in the user's profile timezone, so the
		// instant is converted into that zone first. Without the zone the whole
		// window shifts by the user's UTC offset, and a watermark that advances
		// past the shift skips items for good. A nil location keeps the time as
		// given, the old behavior.
		since := j.opts.Since
		if loc != nil {
			since = since.In(loc)
		}
		clause := fmt.Sprintf(`updated >= "%s"`, jiraJQLTime(since))
		if scope != "" {
			scope += " AND " + clause
		} else {
			scope = clause
		}
		return scope + " ORDER BY updated ASC"
	}
	if scope != "" {
		return scope + " ORDER BY updated DESC"
	}
	return "ORDER BY updated DESC"
}

// jiraJQLTime formats t as a JQL absolute timestamp, backed off by a small
// margin so minor clock skew re-reads a little rather than skips.
//
// It formats in t's OWN location, and that is the whole correctness argument.
// JQL accepts no timezone: "yyyy/MM/dd HH:mm" and nothing else, checked against
// a live server, so the boundary means whatever zone the site reads it in. What
// makes the round trip safe is that the cursor is the newest "updated" the site
// itself returned, parsed with the offset the site wrote, so formatting it back
// reproduces the site's own wall clock whatever zone that is.
//
// The invariant, then: NEVER normalize the cursor to UTC on the way to here. An
// instant is not enough, the zone carries the meaning. The same moment written
// +05:30 instead of Z moves this boundary five and a half hours into the future
// and every issue in between is skipped, which is a measured result and not a
// worry. TestJiraJQLTimeKeepsTheSiteZone holds the line.
func jiraJQLTime(t time.Time) string {
	return t.Add(-2 * time.Minute).Format("2006/01/02 15:04")
}

// userOrEmpty dereferences a user that a source may have left unset.
func userOrEmpty(u *jira.User) jira.User {
	if u == nil {
		return jira.User{}
	}
	return *u
}

// issueCuratedTopics returns the topics an issue states outright: its labels and
// its components. Somebody chose these, which makes them the strongest evidence
// the issue is about that subject.
func issueCuratedTopics(is jira.Issue) []string {
	f := is.Fields
	out := append([]string(nil), f.Labels...)
	for _, c := range f.Components {
		out = append(out, c.Name)
	}
	return out
}

// issueTopics derives topic tokens from an issue's components, labels, summary,
// and project name.
func issueTopics(is jira.Issue) []string {
	f := is.Fields
	var out []string
	for _, c := range f.Components {
		// The component itself is the subject, and it goes in whole. Only its
		// words used to, which threw away the compound and left nothing at all
		// for a component like "SQL" that tokenizes to nothing: the words of
		// "Build System / CI" are just "system". It also meant no component
		// ever matched the stated list, since that holds full names, so every
		// component in the tracker was filed as prose somebody happened to
		// write rather than a subject the tracker declares.
		out = append(out, c.Name)
		// The words as well, so a question asked in one of them still matches.
		out = append(out, titleTokens(c.Name)...)
	}
	out = append(out, f.Labels...)
	out = append(out, phraseTokens(f.Summary)...)
	out = append(out, titleTokens(f.Project.Name)...)
	// The description is where the substance of a ticket lives, so mine it into
	// the assignee's and reporter's topics too. Without this, ask can only match
	// the summary words: "who knows idempotency keys" misses the engineer whose
	// ticket summaries said "payment retries" but whose descriptions are all
	// about idempotency. It is redacted to stemmed terms on save like any text.
	out = append(out, titleTokens(is.Description())...)
	return out
}

// jiraUserKey returns a stable key for a user, preferring email and falling back
// to the site identity, which is the account id on Cloud or the username on
// Server and Data Center.
func jiraUserKey(u jira.User) string {
	return util.PersonKey("jira", u.EmailAddress, u.Identity())
}

// jiraPersonRecord builds a person record. The account id always keys the
// record and any email travels with it, so the indexer joins the two and work
// recorded under a Jira account is findable by the person who did it.
func jiraPersonRecord(u jira.User, topics []string) Record {
	rec := Record{Kind: KindPerson, Source: "jira", Weight: 1, Topics: topics, Name: u.DisplayName}
	if id := u.Identity(); id != "" {
		rec.PersonID = "jira:" + id
	}
	if u.EmailAddress != "" {
		rec.Email = util.NormalizeEmail(u.EmailAddress)
	}
	if rec.Name == "" {
		if rec.Email != "" {
			rec.Name = rec.Email
		} else {
			rec.Name = rec.PersonID
		}
	}
	return rec
}
