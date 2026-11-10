package connector

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/confluence"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/fakeapi"
	"github.com/dcadolph/whotoask/internal/github"
	"github.com/dcadolph/whotoask/internal/jira"
	"github.com/dcadolph/whotoask/internal/pagerduty"
	"github.com/dcadolph/whotoask/internal/slack"
)

// These tests run the real connectors against servers that enforce the parts
// of each API contract a hand-written stub ignores. A stub answers with
// whatever its author typed no matter what was asked for, which is how whotoask
// once shipped a Jira client that produced no episodes at all against a real
// site while every test passed.

// TestJiraAgainstFaithfulSite verifies whotoask reads a Jira site that returns
// only the fields it was asked for. Nothing here hands back a field the client
// forgot to request, so a query missing one shows up as absent data rather
// than as a passing test.
func TestJiraAgainstFaithfulSite(t *testing.T) {
	t.Parallel()
	site := &fakeapi.Jira{Issues: []fakeapi.JiraIssue{{
		Key: "OPS-7", Summary: "Retry storm on billing",
		Description:   "Raised the retry ceiling and redeployed the worker.",
		AssigneeEmail: "jane@x.com", ReporterEmail: "pat@x.com",
		CommentAuthorEmails: []string{"carol@x.com"},
		Components:          []string{"billing"}, Labels: []string{"retries"},
		ProjectKey: "OPS", ProjectName: "Operations",
		Updated:        "2026-06-20T09:30:00.000-0500",
		ResolutionDate: "2026-06-21T09:30:00.000-0500",
		StatusName:     "Done", StatusCategory: "done",
	}, {
		Key: "OPS-8", Summary: "Still open", AssigneeEmail: "bob@x.com",
		ProjectKey: "OPS", ProjectName: "Operations",
		Updated:    "2026-06-22T09:30:00.000-0500",
		StatusName: "In Progress", StatusCategory: "indeterminate",
	}}}
	srv := site.Server()
	t.Cleanup(srv.Close)

	j := NewJiraWithClient(jira.New(srv.URL, "me@x.com", "token"),
		JiraOptions{Projects: []string{"OPS"}, Episodes: true})
	recs, err := j.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no people came back from a site holding two issues")
	}

	eps := j.Episodes()
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want only the resolved issue. A site that returns "+
			"only requested fields exposes a query missing resolutiondate or status: %+v", len(eps), eps)
	}
	ep := eps[0]
	if ep.ID != "jira:OPS-7" {
		t.Errorf("episode = %+v", ep)
	}
	// The description is served as the rich-text tree Jira Cloud really sends,
	// so this also proves whotoask reads that shape rather than a plain string.
	if !strings.Contains(ep.Body, "retry ceiling") {
		t.Errorf("episode body = %q, want the issue description flattened into it", ep.Body)
	}
	if !hasParticipant(ep, "jane@x.com") || !hasParticipant(ep, "pat@x.com") {
		t.Errorf("participants = %v", ep.Participants)
	}
	// carol only commented, never assigned, so capturing her proves comment
	// authors become participants and recall can find who actually helped.
	if !hasParticipant(ep, "carol@x.com") {
		t.Errorf("participants = %v, want the comment author carol", ep.Participants)
	}
}

// TestJiraPagesThroughFaithfulSite verifies paging works against a Cloud site
// that pages by nextPageToken rather than returning everything at once.
func TestJiraPagesThroughFaithfulSite(t *testing.T) {
	t.Parallel()
	var issues []fakeapi.JiraIssue
	for i := range 250 {
		issues = append(issues, fakeapi.JiraIssue{
			Key: "OPS-" + strconv.Itoa(i), Summary: "issue", AssigneeEmail: "jane@x.com",
			ProjectKey: "OPS", ProjectName: "Operations",
			Updated:        "2026-06-20T09:30:00.000-0500",
			ResolutionDate: "2026-06-21T09:30:00.000-0500",
			StatusName:     "Done", StatusCategory: "done",
		})
	}
	site := &fakeapi.Jira{Issues: issues}
	srv := site.Server()
	t.Cleanup(srv.Close)

	j := NewJiraWithClient(jira.New(srv.URL, "me@x.com", "token"),
		JiraOptions{Projects: []string{"OPS"}, MaxIssues: 250, Episodes: true})
	if _, err := j.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := len(j.Episodes()); got != 250 {
		t.Errorf("episodes = %d across pages, want 250", got)
	}
	if len(site.Queries) < 3 {
		t.Errorf("site saw %d searches, want the client to page", len(site.Queries))
	}
}

// TestSlackAgainstFaithfulSite verifies whotoask reads a Slack workspace through
// the restricted history tier: each history page returns only a few objects and
// the first calls answer 429, so the client must page and retry rather than
// under-read or abort. Slack was the one major source with no contract
// simulator, the gap that let the Jira drift hide.
func TestSlackAgainstFaithfulSite(t *testing.T) {
	t.Parallel()
	var msgs []fakeapi.SlackMessage
	for i := range 40 {
		msgs = append(msgs, fakeapi.SlackMessage{
			User: "U1", Text: "checkout latency note " + strconv.Itoa(i),
			TS: fmt.Sprintf("172000%04d.000000", i),
		})
	}
	// A thread whose solution and sixth helper live only in the replies, to prove
	// the replies are read and folded in, not left as the parent alone.
	replies := make([]fakeapi.SlackMessage, 0, 6)
	for i := range 6 {
		replies = append(replies, fakeapi.SlackMessage{
			User: "U" + strconv.Itoa(i+2), Text: "raised the pool and added a circuit breaker",
			TS: fmt.Sprintf("172001%04d.000000", i),
		})
	}
	thread := fakeapi.SlackMessage{User: "U1", Text: "checkout is 500ing", TS: "1720010000.000000", Replies: replies}
	site := &fakeapi.Slack{
		URL: "https://acme.slack.com", Team: "acme", BotUserID: "UBOT",
		HistoryPageCap: 5, ThrottleFirst: 2,
		Users: []fakeapi.SlackUser{
			{ID: "U1", Name: "jane", RealName: "Jane Roe", Email: "jane@x.com", Title: "SRE"},
		},
		Channels: []fakeapi.SlackChannel{
			{ID: "C1", Name: "billing", Topic: "billing ops", IsMember: true, Messages: msgs},
			{ID: "C2", Name: "incidents", IsMember: true, Messages: []fakeapi.SlackMessage{thread}},
		},
	}
	srv := site.Server()
	t.Cleanup(srv.Close)

	s := NewSlackWithClient(slack.New("xoxb-x", slack.WithBaseURL(srv.URL)),
		SlackOptions{Episodes: true, Log: io.Discard})
	recs, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	janeText := ""
	for _, r := range recs {
		if r.Kind == KindPerson && r.PersonID == "jane@x.com" {
			janeText += " " + r.Text
		}
	}
	if !strings.Contains(janeText, "latency note 39") {
		t.Errorf("author text = %q, want the last message, proving every 5-object page was read", janeText)
	}
	// 40 messages at 5 per page is 8 pages, plus the 2 calls that 429'd first.
	if got := site.Calls("conversations.history"); got < 8 {
		t.Errorf("history calls = %d, want at least the 8 pages", got)
	}
	var thr *episode.Episode
	for i := range s.Episodes() {
		if s.Episodes()[i].Kind == episode.KindThread {
			thr = &s.Episodes()[i]
		}
	}
	if thr == nil {
		t.Fatal("no thread episode formed")
		return
	}
	// The word "circuit" is only in the replies, so its presence proves the
	// replies were read and folded into the searchable body.
	if !strings.Contains(thr.Body, "circuit") {
		t.Errorf("thread body = %q, want the reply text folded in, not the parent alone", thr.Body)
	}
	// Slack lists only five repliers on the parent; reading the replies recovers
	// the sixth, so the thread carries all seven participants.
	if len(thr.Participants) < 7 {
		t.Errorf("participants = %d, want 7 (opener plus six repliers past the Slack cap of 5)",
			len(thr.Participants))
	}
}

// TestGitHubAgainstFaithfulAPI verifies whotoask reads merged pull requests from
// an API that defaults to open ones and pages with a Link header. A client
// that forgets state=all sees nothing merged here, which is what would happen
// in production.
func TestGitHubAgainstFaithfulAPI(t *testing.T) {
	t.Parallel()
	api := &fakeapi.GitHub{PerPage: 2, Repos: []fakeapi.GitHubRepo{{
		Owner: "acme", Name: "billing",
		Contributors: map[string]int{"jane": 40},
		Pulls: []fakeapi.GitHubPull{
			{
				Number: 42, Title: "Raise retry ceiling",
				Body:        "Fixes the retry storm seen during the billing outage.",
				AuthorLogin: "jane", ReviewerLogins: []string{"bob"},
				ReviewedBy: []string{"carol"}, CommentedBy: []string{"dave"},
				Labels:    []string{"billing"},
				UpdatedAt: "2026-06-20T09:30:00Z", MergedAt: "2026-06-20T10:00:00Z",
			},
			{
				Number: 43, Title: "Second landed change", AuthorLogin: "cy",
				UpdatedAt: "2026-06-21T09:30:00Z", MergedAt: "2026-06-21T10:00:00Z",
			},
			{
				Number: 44, Title: "Third landed change", AuthorLogin: "dee",
				UpdatedAt: "2026-06-22T09:30:00Z", MergedAt: "2026-06-22T10:00:00Z",
			},
			{
				Number: 45, Title: "Never landed", AuthorLogin: "ed",
				UpdatedAt: "2026-06-23T09:30:00Z",
			},
		},
	}}}
	srv := api.Server()
	t.Cleanup(srv.Close)

	g := NewGitHubWithClient(github.New("token", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"acme/billing"}, Episodes: true})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	eps := g.Episodes()
	if len(eps) != 3 {
		t.Fatalf("episodes = %d, want the three merged changes across paged results: %+v",
			len(eps), eps)
	}
	ep, ok := episodeByID(eps, "github:acme/billing:42")
	if !ok {
		t.Fatalf("first merged change missing: %+v", eps)
	}
	// The description is only on the list object because the real list
	// endpoint carries it; reading it costs no extra request.
	if !strings.Contains(ep.Body, "retry storm") {
		t.Errorf("episode body = %q, want the pull request description indexed", ep.Body)
	}
	if !hasParticipant(ep, "github:jane") || !hasParticipant(ep, "github:bob") {
		t.Errorf("participants = %v", ep.Participants)
	}
	// carol reviewed and dave commented; neither is on the list object's
	// requested reviewers, so capturing them proves the reviews and comments
	// endpoints were read, recovering the people who actually helped.
	if !hasParticipant(ep, "github:carol") || !hasParticipant(ep, "github:dave") {
		t.Errorf("participants = %v, want the actual reviewer and commenter", ep.Participants)
	}
}

// TestPagerDutyAgainstFaithfulAPI verifies whotoask reads resolved incidents from
// an API that actually applies the status filter and pages by offset, so a
// client asking for the wrong status gets nothing rather than everything.
func TestPagerDutyAgainstFaithfulAPI(t *testing.T) {
	t.Parallel()
	api := &fakeapi.PagerDuty{Incidents: []fakeapi.PagerDutyIncident{{
		ID: "I1", Number: 7, Title: "Billing retries exhausted", Status: "resolved",
		ServiceName: "Billing API", CreatedAt: "2026-08-01T10:00:00Z",
		ResolvedAt:     "2026-08-01T11:00:00Z",
		AssigneeEmails: []string{"jane@x.com"}, AcknowledgerEmails: []string{"bob@x.com"},
		Notes: []string{"bumped the connection pool and added a circuit breaker"},
	}, {
		ID: "I2", Number: 8, Title: "Still burning", Status: "triggered",
		ServiceName: "Billing API", CreatedAt: "2026-08-02T10:00:00Z",
		AssigneeEmails: []string{"jane@x.com"},
	}}}
	srv := api.Server()
	t.Cleanup(srv.Close)

	p := NewPagerDutyWithClient(
		pagerduty.New("token", pagerduty.WithBaseURL(srv.URL)),
		PagerDutyOptions{Episodes: true})
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	eps := p.Episodes()
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want only the resolved incident: %+v", len(eps), eps)
	}
	ep := eps[0]
	if ep.ID != "pagerduty:I1" || ep.Kind != episode.KindIncident {
		t.Errorf("episode = %+v", ep)
	}
	// The resolution note is read from the per-incident notes endpoint and folded
	// into the body, so recall can match how the incident was actually settled.
	if !strings.Contains(ep.Body, "circuit breaker") {
		t.Errorf("episode body = %q, want the resolution note folded in", ep.Body)
	}
	if !hasParticipant(ep, "jane@x.com") || !hasParticipant(ep, "bob@x.com") {
		t.Errorf("participants = %v, want the assignee and the acknowledger", ep.Participants)
	}
	if len(api.Queries) == 0 || !strings.Contains(api.Queries[0], "statuses") {
		t.Errorf("queries = %v, want the client to filter by status", api.Queries)
	}
}

// TestConfluenceAgainstFaithfulSite verifies whotoask reads a Confluence site
// that withholds anything it was not asked to expand. Space, labels, history,
// and version all arrive only when named, so an expansion dropped from the
// query shows up as missing people rather than as a passing test.
func TestConfluenceAgainstFaithfulSite(t *testing.T) {
	t.Parallel()
	site := &fakeapi.Confluence{Pages: []fakeapi.ConfluencePage{{
		Title: "Billing retry runbook", SpaceKey: "ENG", SpaceName: "Engineering",
		Labels:         []string{"billing", "runbook"},
		CreatedByEmail: "jane@x.com", CreatedAt: "2026-06-01T09:30:00.000Z",
		EditedByEmail: "bob@x.com", EditedAt: "2026-06-20T09:30:00.000Z",
		Body: "<p>Raise the retry ceiling and clear the queue idempotently.</p>",
	}}}
	srv := site.Server()
	t.Cleanup(srv.Close)

	c := NewConfluenceWithClient(
		confluence.New(srv.URL, "me@x.com", "token"),
		ConfluenceOptions{Spaces: []string{"ENG"}})
	recs, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no people came back; the creator and last editor both expand from history and version")
	}
	var sawCreator, sawEditor, sawBodyTopic bool
	for _, r := range recs {
		switch r.Email {
		case "jane@x.com":
			sawCreator = true
			// Body words are mined as weak topics: they still carry affinity, but
			// only a stated label establishes a subject on its own.
			for _, tp := range append(append([]string(nil), r.Topics...), r.WeakTopics...) {
				if tp == "idempotently" {
					sawBodyTopic = true
				}
			}
		case "bob@x.com":
			sawEditor = true
		}
		if r.Team == "" && r.Text == "" && len(r.Topics) == 0 && len(r.WeakTopics) == 0 {
			t.Errorf("record %+v carries nothing from the expanded page", r)
		}
	}
	if !sawCreator || !sawEditor {
		t.Errorf("records = %+v, want both the page creator and the last editor", recs)
	}
	// The word "idempotently" is only in the page body, so its presence in the
	// author's topics proves the body was fetched and mined, not just the title.
	if !sawBodyTopic {
		t.Error("the page body was not mined into topics; ask cannot match page content")
	}
}

// TestJiraReportsProgressWhilePaging verifies the connector shows movement as
// it pages rather than printing one line after every issue has already
// arrived. A long index left a user staring at a still screen otherwise.
func TestJiraReportsProgressWhilePaging(t *testing.T) {
	t.Parallel()
	var issues []fakeapi.JiraIssue
	for i := range 350 {
		issues = append(issues, fakeapi.JiraIssue{
			Key: "OPS-" + strconv.Itoa(i), Summary: "issue", AssigneeEmail: "jane@x.com",
			ProjectKey: "OPS", ProjectName: "Operations",
			Updated:        "2026-06-20T09:30:00.000-0500",
			ResolutionDate: "2026-06-21T09:30:00.000-0500",
			StatusName:     "Done", StatusCategory: "done",
		})
	}
	srv := (&fakeapi.Jira{Issues: issues}).Server()
	t.Cleanup(srv.Close)

	var log strings.Builder
	src := NewJira(srv.URL, "me@x.com", "token", JiraOptions{
		Projects: []string{"OPS"}, MaxIssues: 350, Log: &log,
	})
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Four pages of 100, so several interim progress lines, not just the final
	// summary.
	if got := strings.Count(log.String(), "fetched"); got < 3 {
		t.Errorf("saw %d progress lines in:\n%s", got, log.String())
	}
}

// TestConfluenceServerAgainstFaithfulSite verifies whotoask reads a self-hosted
// Server or Data Center wiki: the REST API at the site root, and username-based
// creators with no account id or email. This is the shape Apache's public
// Confluence returns, where identity must key on the username.
func TestConfluenceServerAgainstFaithfulSite(t *testing.T) {
	t.Parallel()
	site := &fakeapi.Confluence{ServerMode: true, Pages: []fakeapi.ConfluencePage{{
		Title: "KRaft controller design", SpaceKey: "KAFKA", SpaceName: "Kafka",
		Labels:         []string{"kraft", "controller"},
		CreatedByEmail: "showuon@apache.invalid", CreatedAt: "2026-06-01T09:30:00.000Z",
		EditedByEmail: "bbejeck@apache.invalid", EditedAt: "2026-06-20T09:30:00.000Z",
	}}}
	srv := site.Server()
	t.Cleanup(srv.Close)

	c := NewConfluence(srv.URL, "", "", ConfluenceOptions{Spaces: []string{"KAFKA"}, Server: true})
	recs, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no people from a Server wiki; the creator and editor expand from history and version")
	}
	var sawCreator, sawEditor bool
	for _, r := range recs {
		if r.PersonID == "confluence:showuon" {
			sawCreator = true
		}
		if r.PersonID == "confluence:bbejeck" {
			sawEditor = true
		}
	}
	if !sawCreator || !sawEditor {
		t.Errorf("records = %+v, want the creator and editor keyed by username", recs)
	}
}
