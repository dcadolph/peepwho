package connector

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/github"
	"github.com/dcadolph/peepwho/internal/jira"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/pagerduty"
	"github.com/dcadolph/peepwho/internal/slack"
)

// resolvedIssue returns a resolved Jira issue with the given assignee and
// reporter emails, either of which may be empty to leave the field unset.
func resolvedIssue(assignee, reporter string) jira.Issue {
	var is jira.Issue
	is.Key = "OPS-7"
	is.Fields.Summary = "Retry storm on billing"
	is.Fields.ResolutionDate = "2026-06-20T09:30:00.000-0500"
	is.Fields.Project.Key = "OPS"
	if assignee != "" {
		is.Fields.Assignee = &jira.User{AccountID: "a1", EmailAddress: assignee}
	}
	if reporter != "" {
		is.Fields.Reporter = &jira.User{AccountID: "p1", EmailAddress: reporter}
	}
	return is
}

// TestEpisodeAsked verifies what each source records about who opened the
// conversation, and that the answerers follow from it rather than from a
// position in the participant list. A source that cannot tell an ask from an
// answer records no asker rather than guessing at one.
func TestEpisodeAsked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Build        func() (episode.Episode, bool)
		Name         string
		WantAsked    model.ID
		WantAnswered []model.ID
	}{{ // Test 0: A Slack thread's asker is the parent author, and the repliers
		// Slack lists on the parent are the ones who answered.
		Name: "slack thread",
		Build: func() (episode.Episode, bool) {
			eps := collectEpisodes(testChannel, []slack.Message{{
				User: "U1", Text: "cert renewal is failing", TS: ts(1000), ThreadTS: ts(1000),
				ReplyCount: 3, ReplyUsers: []string{"U2", "U3"}, LatestReply: ts(1500),
			}}, episodeOpts{byID: testUsers})
			if len(eps) != 1 {
				return episode.Episode{}, false
			}
			return eps[0], true
		},
		WantAsked:    "ann@x.com",
		WantAnswered: []model.ID{"bo@x.com", "cy@x.com"},
	}, { // Test 1: A run of loose messages has no asker, so nobody is credited
		// with answering, however the first speaker happens to be ordered.
		Name: "slack window",
		Build: func() (episode.Episode, bool) {
			eps := collectEpisodes(testChannel, []slack.Message{
				{User: "U1", Text: "anyone seen this timeout", TS: ts(2000)},
				{User: "U2", Text: "bump the pool size", TS: ts(2100)},
				{User: "U1", Text: "that worked", TS: ts(2200)},
			}, episodeOpts{byID: testUsers})
			if len(eps) != 1 {
				return episode.Episode{}, false
			}
			return eps[0], true
		},
		WantAsked:    "",
		WantAnswered: nil,
	}, { // Test 2: A Jira issue's asker is the reporter, even though the
		// participant list leads with the assignee.
		Name: "jira issue",
		Build: func() (episode.Episode, bool) {
			return issueEpisode("https://acme.atlassian.net", resolvedIssue("jane@x.com", "pat@x.com"))
		},
		WantAsked:    "pat@x.com",
		WantAnswered: []model.ID{"jane@x.com"},
	}, { // Test 3: An issue with no reporter records no asker rather than falling
		// back to whoever else is on the ticket.
		Name: "jira issue without a reporter",
		Build: func() (episode.Episode, bool) {
			return issueEpisode("https://acme.atlassian.net", resolvedIssue("jane@x.com", ""))
		},
		WantAsked:    "",
		WantAnswered: nil,
	}, { // Test 4: A merged pull request's asker is its author, and the reviewers
		// are the ones who answered.
		Name: "github change",
		Build: func() (episode.Episode, bool) {
			pr := github.PullRequest{
				Number:   12,
				Title:    "Retry the billing call",
				HTMLURL:  "https://github.com/acme/pay/pull/12",
				MergedAt: time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC),
			}
			pr.User.Login = "Ann"
			return changeEpisode("acme", "pay", pr, []string{"bo"})
		},
		WantAsked:    "github:ann",
		WantAnswered: []model.ID{"github:bo"},
	}, { // Test 5: A PagerDuty incident records no asker. An alert opened it, and
		// assignees, acknowledgers, and the resolver all answered.
		Name: "pagerduty incident",
		Build: func() (episode.Episode, bool) {
			in := pagerduty.Incident{
				ID:         "PD1",
				Title:      "Billing latency",
				Status:     "resolved",
				ResolvedAt: time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC),
			}
			in.Assignments = append(in.Assignments, struct {
				Assignee pagerduty.User `json:"assignee"`
			}{Assignee: pagerduty.User{ID: "U1", Email: "ann@x.com"}})
			return incidentEpisode(in)
		},
		WantAsked:    "",
		WantAnswered: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ep, ok := test.Build()
			if !ok {
				t.Fatal("the source produced no episode")
			}
			if diff := cmp.Diff(test.WantAsked, ep.Asked); diff != "" {
				t.Errorf("Asked mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantAnswered, ep.Answered(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Answered mismatch (-want +got):\n%s", diff)
			}
			if ep.Asked != "" && !hasParticipant(ep, ep.Asked) {
				t.Errorf("asker %q is not a participant of %q", ep.Asked, ep.ID)
			}
		})
	}
}
