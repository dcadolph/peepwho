package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/fakeapi"
	"github.com/dcadolph/whotoask/internal/github"
	"github.com/dcadolph/whotoask/internal/jira"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/pagerduty"
)

// episodeByID finds an episode by ID so assertions do not depend on order.
func episodeByID(eps []episode.Episode, id string) (episode.Episode, bool) {
	for _, ep := range eps {
		if ep.ID == id {
			return ep, true
		}
	}
	return episode.Episode{}, false
}

// hasParticipant reports whether an episode lists a person.
func hasParticipant(ep episode.Episode, id model.ID) bool {
	for _, p := range ep.Participants {
		if p == id {
			return true
		}
	}
	return false
}

// TestJiraEpisodes verifies resolved issues become episodes and open ones do
// not, through the real client against a server speaking Jira's wire format.
// Jira is the source where a mismatch between the fields requested and the
// fields read once produced no episodes at all.
func TestJiraEpisodes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer with only the fields the caller asked for, which is what Jira
		// does. A query that forgets a field must fail this test.
		asked := r.URL.Query().Get("fields")
		resolved := `"resolutiondate":"2026-06-20T09:30:00.000-0500",` +
			`"status":{"name":"Done","statusCategory":{"key":"done"}},`
		if !strings.Contains(asked, "resolutiondate") {
			resolved = ""
		}
		io.WriteString(w, `{"total":2,"startAt":0,"issues":[`+
			`{"key":"OPS-7","fields":{"summary":"Retry storm on billing",`+
			resolved+
			`"assignee":{"accountId":"a1","displayName":"Jane Roe","emailAddress":"jane@x.com"},`+
			`"reporter":{"accountId":"p1","displayName":"Pat","emailAddress":"pat@x.com"},`+
			`"components":[{"name":"billing"}],"labels":["retries"],`+
			`"project":{"key":"OPS","name":"Operations"}}},`+
			`{"key":"OPS-8","fields":{"summary":"Still open",`+
			`"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},`+
			`"assignee":{"accountId":"b1","displayName":"Bob","emailAddress":"bob@x.com"},`+
			`"project":{"key":"OPS","name":"Operations"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	j := NewJiraWithClient(jira.New(srv.URL, "me@x.com", "token"),
		JiraOptions{Projects: []string{"OPS"}, Episodes: true})
	if _, err := j.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	eps := j.Episodes()
	if len(eps) != 1 {
		t.Fatalf("Episodes = %d, want 1 resolved issue and no open one: %+v", len(eps), eps)
	}
	ep := eps[0]
	if ep.ID != "jira:OPS-7" || ep.Source != "jira" || ep.Kind != episode.KindIssue {
		t.Errorf("episode identity = %+v", ep)
	}
	if ep.Place != "OPS" || ep.Permalink != srv.URL+"/browse/OPS-7" {
		t.Errorf("place %q permalink %q", ep.Place, ep.Permalink)
	}
	if ep.Occurred.IsZero() {
		t.Error("resolved issue has no occurred time")
	}
	if !hasParticipant(ep, "jane@x.com") || !hasParticipant(ep, "pat@x.com") {
		t.Errorf("participants = %v, want the assignee and reporter", ep.Participants)
	}
}

// TestJiraEpisodesOffByDefault verifies episodes are recorded only when asked
// for, so an install that only wants the people graph reads no issue history.
func TestJiraEpisodesOffByDefault(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":1,"startAt":0,"issues":[{"key":"OPS-7","fields":{`+
			`"summary":"Retry storm","resolutiondate":"2026-06-20T09:30:00.000-0500",`+
			`"status":{"name":"Done","statusCategory":{"key":"done"}},`+
			`"assignee":{"accountId":"a1","emailAddress":"jane@x.com"},`+
			`"project":{"key":"OPS","name":"Operations"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	j := NewJiraWithClient(jira.New(srv.URL, "me@x.com", "token"), JiraOptions{})
	if _, err := j.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if eps := j.Episodes(); len(eps) != 0 {
		t.Errorf("Episodes = %d without the option set, want 0", len(eps))
	}
}

// TestGitHubEpisodes verifies merged pull requests become episodes carrying
// author and reviewers, and that unmerged work is skipped because it records a
// proposal rather than how something was done.
func TestGitHubEpisodes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/billing", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"billing","full_name":"acme/billing"}`)
	})
	mux.HandleFunc("/repos/acme/billing/contributors", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"login":"jane","contributions":40}]`)
	})
	mux.HandleFunc("/repos/acme/billing/pulls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `[{"number":42,"title":"Raise retry ceiling",`+
			`"html_url":"https://github.com/acme/billing/pull/42",`+
			`"user":{"login":"Jane"},"labels":[{"name":"billing"}],`+
			`"requested_reviewers":[{"login":"bob"}],`+
			`"updated_at":"2026-06-20T09:30:00Z","merged_at":"2026-06-20T10:00:00Z"},`+
			`{"number":43,"title":"Never landed","user":{"login":"cy"},`+
			`"updated_at":"2026-06-21T09:30:00Z"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	g := NewGitHubWithClient(github.New("token", github.WithBaseURL(srv.URL)),
		GitHubOptions{Repos: []string{"acme/billing"}, Episodes: true})
	if _, err := g.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	eps := g.Episodes()
	if len(eps) != 1 {
		t.Fatalf("Episodes = %d, want only the merged pull request: %+v", len(eps), eps)
	}
	ep := eps[0]
	if ep.ID != "github:acme/billing:42" || ep.Kind != episode.KindChange {
		t.Errorf("episode identity = %+v", ep)
	}
	if ep.Permalink != "https://github.com/acme/billing/pull/42" || ep.Place != "acme/billing" {
		t.Errorf("permalink %q place %q", ep.Permalink, ep.Place)
	}
	if ep.Occurred.IsZero() {
		t.Error("merged pull request has no occurred time")
	}
	// The author's login is capitalized in the payload; participants must be
	// folded so they join the same person the people graph keys by.
	if !hasParticipant(ep, "github:jane") || !hasParticipant(ep, "github:bob") {
		t.Errorf("participants = %v, want the author and the reviewer, lowercased", ep.Participants)
	}
}

// TestPagerDutyEpisodes verifies resolved incidents become episodes with
// everyone who handled them, and that an unresolved incident is skipped.
func TestPagerDutyEpisodes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"more":false,"services":[{"id":"S1","name":"Billing API",`+
			`"escalation_policy":{"id":"EP1"}}]}`)
	})
	mux.HandleFunc("/oncalls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"more":false,"oncalls":[{"user":{"id":"U1","name":"Jane Roe",`+
			`"email":"jane@x.com"},"escalation_policy":{"id":"EP1"}}]}`)
	})
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"more":false,"incidents":[`+
			`{"id":"I1","incident_number":7,"title":"Billing retries exhausted",`+
			`"status":"resolved","html_url":"https://x.pagerduty.com/incidents/I1",`+
			`"created_at":"2026-08-01T10:00:00Z","resolved_at":"2026-08-01T11:00:00Z",`+
			`"service":{"id":"S1","summary":"Billing API"},`+
			`"assignments":[{"assignee":{"id":"U1","name":"Jane Roe","email":"jane@x.com"}}],`+
			`"acknowledgements":[{"acknowledger":{"id":"U2","name":"Bob","email":"bob@x.com"}}]},`+
			`{"id":"I2","incident_number":8,"title":"Still burning","status":"triggered",`+
			`"created_at":"2026-08-02T10:00:00Z",`+
			`"assignments":[{"assignee":{"id":"U1","email":"jane@x.com"}}]}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := NewPagerDutyWithClient(
		pagerduty.New("token", pagerduty.WithBaseURL(srv.URL)),
		PagerDutyOptions{Episodes: true})
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	eps := p.Episodes()
	if len(eps) != 1 {
		t.Fatalf("Episodes = %d, want only the resolved incident: %+v", len(eps), eps)
	}
	ep, ok := episodeByID(eps, "pagerduty:I1")
	if !ok {
		t.Fatalf("resolved incident missing, got %+v", eps)
	}
	if ep.Kind != episode.KindIncident || ep.Place != "Billing API" {
		t.Errorf("episode = %+v", ep)
	}
	if !hasParticipant(ep, "jane@x.com") || !hasParticipant(ep, "bob@x.com") {
		t.Errorf("participants = %v, want the assignee and the acknowledger", ep.Participants)
	}
	if ep.Occurred.IsZero() || ep.Permalink == "" {
		t.Errorf("occurred %v permalink %q", ep.Occurred, ep.Permalink)
	}
}

// TestJiraServerEpisodes verifies whotoask reads a self-hosted Server or Data
// Center Jira: v2 shape, username identity, and no email. This is the shape a
// public tracker such as Apache's Jira returns, where an assignee has a name
// but no account id and no visible email, and it once produced zero episodes
// because identity keyed only on the account id.
func TestJiraServerEpisodes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/2/") {
			t.Errorf("Server mode hit %q, want the v2 API", r.URL.Path)
		}
		io.WriteString(w, `{"total":1,"startAt":0,"issues":[{"key":"KAFKA-9","fields":{`+
			`"summary":"consumer rebalance loop","description":"raised the session timeout",`+
			`"assignee":{"name":"jdoe","displayName":"Jane Doe"},`+
			`"reporter":{"name":"bsmith","displayName":"Bob Smith"},`+
			`"resolutiondate":"2026-06-20T09:30:00.000+0000",`+
			`"status":{"statusCategory":{"key":"done"}},`+
			`"project":{"key":"KAFKA","name":"Kafka"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	j := NewJira(srv.URL, "", "", JiraOptions{
		Projects: []string{"KAFKA"}, Episodes: true, Server: true})
	if _, err := j.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	eps := j.Episodes()
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want one from the resolved Server issue: %+v", len(eps), eps)
	}
	ep := eps[0]
	// Identity keys on the username since there is no email or account id.
	if !hasParticipant(ep, "jira:jdoe") || !hasParticipant(ep, "jira:bsmith") {
		t.Errorf("participants = %v, want the assignee and reporter by username", ep.Participants)
	}
	// The v2 string description is indexed, not dropped as an unreadable tree.
	if !strings.Contains(ep.Body, "session timeout") {
		t.Errorf("body = %q, want the string description indexed", ep.Body)
	}
}

// TestPagerDutyResolverBecomesParticipant verifies an incident resolved without
// a formal assignee still becomes an episode, attributed to whoever resolved
// it. Real PagerDuty incidents are often settled by hand with no assignment,
// and the resolver is the surest record of who actually solved the problem, so
// missing them meant no episode at all.
func TestPagerDutyResolverBecomesParticipant(t *testing.T) {
	t.Parallel()
	api := &fakeapi.PagerDuty{Incidents: []fakeapi.PagerDutyIncident{{
		ID: "I1", Number: 1, Title: "Disaster Struck", Status: "resolved",
		ServiceName: "billing", CreatedAt: "2026-08-18T10:00:00Z",
		ResolvedAt: "2026-08-18T10:30:00Z",
		// No assignee, no acknowledger: only a resolver, the manual-resolve case.
		ResolverEmail: "resolver@example.com",
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
		t.Fatalf("episodes = %d, want the resolved incident with its resolver: %+v", len(eps), eps)
	}
	if !hasParticipant(eps[0], "resolver@example.com") {
		t.Errorf("participants = %v, want the resolver", eps[0].Participants)
	}
}
