package connector

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/slack"
)

// testChannel is the channel every episode case is collected from.
var testChannel = slack.Channel{ID: "C1", Name: "payments"}

// testUsers maps the Slack user IDs used below to people with emails, which is
// how the connector resolves a canonical person.
var testUsers = map[string]slack.User{
	"U1": {ID: "U1", Profile: slack.Profile{RealName: "Ann", Email: "ann@x.com"}},
	"U2": {ID: "U2", Profile: slack.Profile{RealName: "Bo", Email: "bo@x.com"}},
	"U3": {ID: "U3", Profile: slack.Profile{RealName: "Cy", Email: "cy@x.com"}},
}

// ts builds a Slack timestamp for the given epoch second.
func ts(sec int) string { return strconv.Itoa(sec) + ".000100" }

// TestCollectEpisodes verifies each conversation shape a channel produces: a
// thread with replies, a run of loose messages between several people, and the
// runs that are too thin or too solitary to remember.
func TestCollectEpisodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name             string
		Messages         []slack.Message
		WantIDs          []string
		WantKind         episode.Kind
		WantParticipants []model.ID
		WantMessages     int
	}{{ // Test 0: A thread parent with replies is one episode, and the repliers
		// Slack lists on the parent are its participants.
		Name: "thread",
		Messages: []slack.Message{{
			User: "U1", Text: "cert renewal is failing", TS: ts(1000), ThreadTS: ts(1000),
			ReplyCount: 3, ReplyUsers: []string{"U2", "U3"}, LatestReply: ts(1500),
		}},
		WantIDs:          []string{"slack:C1:" + ts(1000)},
		WantKind:         episode.KindThread,
		WantParticipants: []model.ID{"ann@x.com", "bo@x.com", "cy@x.com"},
		WantMessages:     4,
	}, { // Test 1: Loose messages from several people close in time are one
		// episode, since that is what guidance looks like without a thread.
		Name: "window",
		Messages: []slack.Message{
			{User: "U1", Text: "anyone seen this timeout", TS: ts(2000)},
			{User: "U2", Text: "bump the pool size", TS: ts(2100)},
			{User: "U1", Text: "that worked", TS: ts(2200)},
		},
		WantIDs:          []string{"slack:C1:w" + ts(2000)},
		WantKind:         episode.KindWindow,
		WantParticipants: []model.ID{"ann@x.com", "bo@x.com"},
		WantMessages:     3,
	}, { // Test 2: One person talking to themselves is not a conversation.
		Name: "solitary",
		Messages: []slack.Message{
			{User: "U1", Text: "note to self", TS: ts(3000)},
			{User: "U1", Text: "and another", TS: ts(3100)},
			{User: "U1", Text: "and one more", TS: ts(3200)},
		},
		WantIDs: nil,
	}, { // Test 3: Two messages are too thin to remember.
		Name: "too short",
		Messages: []slack.Message{
			{User: "U1", Text: "hi", TS: ts(4000)},
			{User: "U2", Text: "hello", TS: ts(4100)},
		},
		WantIDs: nil,
	}, { // Test 4: A long silence splits one run into two conversations, and
		// only the ones that qualify survive.
		Name: "gap splits",
		Messages: []slack.Message{
			{User: "U1", Text: "first problem", TS: ts(5000)},
			{User: "U2", Text: "try this", TS: ts(5100)},
			{User: "U1", Text: "fixed", TS: ts(5200)},
			{User: "U1", Text: "second problem", TS: ts(90000)},
			{User: "U2", Text: "try that", TS: ts(90100)},
			{User: "U1", Text: "fixed again", TS: ts(90200)},
		},
		WantIDs: []string{"slack:C1:w" + ts(90000), "slack:C1:w" + ts(5000)},
	}, { // Test 5: Bot and system messages never become episodes.
		Name: "bots ignored",
		Messages: []slack.Message{
			{User: "U1", BotID: "B1", Text: "deploy finished", TS: ts(6000)},
			{Subtype: "channel_join", User: "U2", Text: "joined", TS: ts(6100)},
			{User: "U3", Text: "ok", TS: ts(6200)},
		},
		WantIDs: nil,
	}, { // Test 6: Replies that arrive in the history page do not start their
		// own episode; the thread parent already covers them.
		Name: "replies folded",
		Messages: []slack.Message{
			{
				User: "U1", Text: "parent", TS: ts(7000), ThreadTS: ts(7000),
				ReplyCount: 1, ReplyUsers: []string{"U2"}, LatestReply: ts(7100),
			},
			{User: "U2", Text: "reply", TS: ts(7100), ThreadTS: ts(7000)},
		},
		WantIDs:  []string{"slack:C1:" + ts(7000)},
		WantKind: episode.KindThread,
	}, { // Test 7: A thread parent with no replies is not an episode.
		Name: "unanswered thread",
		Messages: []slack.Message{
			{User: "U1", Text: "anyone?", TS: ts(8000), ThreadTS: ts(8000)},
		},
		WantIDs: nil,
	}}
	for testNum, test := range tests {
		t.Run(strconv.Itoa(testNum)+" "+test.Name, func(t *testing.T) {
			t.Parallel()
			got := collectEpisodes(testChannel, test.Messages,
				episodeOpts{byID: testUsers, workspaceURL: "https://acme.slack.com/"})
			ids := make([]string, 0, len(got))
			for _, ep := range got {
				ids = append(ids, ep.ID)
			}
			if diff := cmp.Diff(test.WantIDs, emptyToNil(ids)); diff != "" {
				t.Fatalf("episode ids mismatch (-want +got):\n%s", diff)
			}
			if len(got) == 0 {
				return
			}
			if test.WantKind != "" && got[0].Kind != test.WantKind {
				t.Errorf("kind = %q, want %q", got[0].Kind, test.WantKind)
			}
			if test.WantParticipants != nil {
				if diff := cmp.Diff(test.WantParticipants, got[0].Participants); diff != "" {
					t.Errorf("participants mismatch (-want +got):\n%s", diff)
				}
			}
			if test.WantMessages != 0 && got[0].Messages != test.WantMessages {
				t.Errorf("messages = %d, want %d", got[0].Messages, test.WantMessages)
			}
			if got[0].Place != "payments" || got[0].PlaceID != "C1" {
				t.Errorf("place = (%q, %q), want (payments, C1)", got[0].Place, got[0].PlaceID)
			}
			if got[0].Permalink == "" {
				t.Error("permalink is empty")
			}
			if got[0].Body == "" {
				t.Error("body is empty, so the episode would index nothing")
			}
		})
	}
}

// TestCollectEpisodesCap verifies the per-channel cap keeps the newest
// conversations, so one busy channel cannot flood the store.
func TestCollectEpisodesCap(t *testing.T) {
	t.Parallel()
	var msgs []slack.Message
	for i := range 5 {
		base := 10000 + i*100000
		msgs = append(msgs,
			slack.Message{User: "U1", Text: "problem", TS: ts(base)},
			slack.Message{User: "U2", Text: "answer", TS: ts(base + 60)},
			slack.Message{User: "U1", Text: "thanks", TS: ts(base + 120)},
		)
	}
	got := collectEpisodes(testChannel, msgs,
		episodeOpts{byID: testUsers, workspaceURL: "https://acme.slack.com/", max: 2})
	if len(got) != 2 {
		t.Fatalf("collected %d episodes, want 2", len(got))
	}
	if got[0].ID != "slack:C1:w"+ts(410000) {
		t.Errorf("kept %q first, want the newest conversation", got[0].ID)
	}
}

// TestCollectEpisodesNoWorkspaceURL verifies episodes still form without a
// workspace URL, just without links.
func TestCollectEpisodesNoWorkspaceURL(t *testing.T) {
	t.Parallel()
	msgs := []slack.Message{
		{User: "U1", Text: "problem", TS: ts(1000)},
		{User: "U2", Text: "answer", TS: ts(1060)},
		{User: "U1", Text: "thanks", TS: ts(1120)},
	}
	got := collectEpisodes(testChannel, msgs, episodeOpts{byID: testUsers})
	if len(got) != 1 {
		t.Fatalf("collected %d episodes, want 1", len(got))
	}
	if got[0].Permalink != "" {
		t.Errorf("permalink = %q, want empty", got[0].Permalink)
	}
}

// emptyToNil normalizes an empty slice so the table can express "no episodes"
// as nil.
func emptyToNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
