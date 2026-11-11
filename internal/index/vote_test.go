package index

import (
	"fmt"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/feedback"
)

// voteIndex builds two equally weighted kafka owners and one kafka channel.
func voteIndex() *Index {
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "alice@corp.com", Name: "Alice",
		Topics: []string{"kafka"}, Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "bob@corp.com", Name: "Bob",
		Topics: []string{"kafka"}, Source: "org-csv",
	}, {
		Kind: connector.KindChannel, Name: "kafka", Title: "kafka questions",
		Source: "slack",
	}})
	return ix
}

func TestFeedbackReordersPeople(t *testing.T) {
	t.Parallel()
	ix := voteIndex()

	base := ix.Search("kafka", 2)
	if len(base) != 2 || base[0].Person.ID != "alice@corp.com" {
		t.Fatalf("baseline order = %+v, want alice first on the ID tiebreak", base)
	}

	ix.SetFeedback([]feedback.Entry{
		{Query: "kafka", Person: "bob@corp.com", Vote: feedback.Helpful},
		{Query: "kafka", Person: "alice@corp.com", Vote: feedback.NotHelpful},
	})
	got := ix.Search("kafka", 2)
	if got[0].Person.ID != "bob@corp.com" {
		t.Errorf("top after votes = %s, want bob", got[0].Person.ID)
	}
	if r := got[0].Reasons[len(got[0].Reasons)-1]; r != "boosted by feedback" {
		t.Errorf("bob last reason = %q, want boosted by feedback", r)
	}
	if r := got[1].Reasons[len(got[1].Reasons)-1]; r != "lowered by feedback" {
		t.Errorf("alice last reason = %q, want lowered by feedback", r)
	}
}

func TestFeedbackScopedToQuery(t *testing.T) {
	t.Parallel()
	ix := voteIndex()
	ix.SetFeedback([]feedback.Entry{
		{Query: "kafka streaming", Person: "bob@corp.com", Vote: feedback.Helpful},
	})
	got := ix.Search("kafka", 2)
	if got[0].Person.ID != "alice@corp.com" {
		t.Errorf("top = %s; a vote for a narrower query must not apply to a broader one",
			got[0].Person.ID)
	}
}

func TestFeedbackStemsAndClamps(t *testing.T) {
	t.Parallel()
	ix := voteIndex()
	votes := make([]feedback.Entry, 0, 10)
	for range 10 {
		votes = append(votes, feedback.Entry{
			Query: "kafka", Person: "bob@corp.com", Vote: feedback.Helpful,
		})
	}
	ix.SetFeedback(votes)

	base := voteIndex().Search("kafka", 2)
	got := ix.Search("kafka", 2)
	var baseBob, gotBob float64
	for _, m := range base {
		if m.Person.ID == "bob@corp.com" {
			baseBob = m.Score
		}
	}
	for _, m := range got {
		if m.Person.ID == "bob@corp.com" {
			gotBob = m.Score
		}
	}
	want := baseBob * ix.feedbackFactor(defaultFeedbackNet)
	if gotBob < want*0.999 || gotBob > want*1.001 {
		t.Errorf("bob score = %.4f, want clamped to %.4f", gotBob, want)
	}
}

func TestFeedbackBoostsChannels(t *testing.T) {
	t.Parallel()
	ix := voteIndex()
	base := ix.SearchChannels("kafka", 1)[0].Score

	ix.SetFeedback([]feedback.Entry{
		{Query: "kafka", Channel: "kafka", Vote: feedback.Helpful},
	})
	got := ix.SearchChannels("kafka", 1)[0]
	want := base * ix.feedbackFactor(1)
	if got.Score < want*0.999 || got.Score > want*1.001 {
		t.Errorf("channel score = %.4f, want %.4f", got.Score, want)
	}
	if r := got.Reasons[len(got.Reasons)-1]; r != "boosted by feedback" {
		t.Errorf("channel last reason = %q", r)
	}
}

// votedResult returns the score and the last reason line the query gives the
// person or channel the vote names, and whether the query returned it at all.
func votedResult(ix *Index, query string, vote feedback.Entry) (score float64, last string, found bool) {
	if vote.Channel != "" {
		for _, m := range ix.SearchChannels(query, 0) {
			if string(m.Channel.ID) == slug(vote.Channel) {
				return m.Score, lastReason(m.Reasons), true
			}
		}
		return 0, "", false
	}
	for _, m := range ix.Search(query, 0) {
		if m.Person.Email == vote.Person {
			return m.Score, lastReason(m.Reasons), true
		}
	}
	return 0, "", false
}

// lastReason returns the final reason line, or the empty string for none.
func lastReason(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return reasons[len(reasons)-1]
}

// TestFeedbackFiresOnlyOnTypedTerms covers a vote leaking from the question it
// was recorded on into another one, through a term the query grew by itself. A
// tie adds a subject nobody asked about and a synonym adds a word nobody typed,
// so neither carries a vote onto the question. Each pair of cases shows the
// vote held off the grown question and firing on its own.
func TestFeedbackFiresOnlyOnTypedTerms(t *testing.T) {
	t.Parallel()
	channel := connector.Record{
		Kind: connector.KindChannel, Name: "kafka-lag", Title: "kafka lag questions",
		Source: "slack",
	}
	personTie := feedback.Entry{
		Query: "kafka lag", Person: "kafka@corp.com", Vote: feedback.Helpful,
	}
	personSyn := feedback.Entry{
		Query: "payments", Person: "billing@corp.com", Vote: feedback.Helpful,
	}
	channelTie := feedback.Entry{
		Query: "kafka lag", Channel: "kafka-lag", Vote: feedback.Helpful,
	}
	tests := []struct {
		Vote      feedback.Entry
		Query     string
		WantFired bool
	}{{ // Test 0: A vote on a tied subject stays off the question that reaches it.
		Vote: personTie, Query: "billing retries", WantFired: false,
	}, { // Test 1: The same vote fires on the question it was recorded on.
		Vote: personTie, Query: "kafka lag", WantFired: true,
	}, { // Test 2: A vote on a synonym stays off the word that expands to it.
		Vote: personSyn, Query: "billing", WantFired: false,
	}, { // Test 3: The same vote fires when the synonym is the word typed.
		Vote: personSyn, Query: "payments", WantFired: true,
	}, { // Test 4: A channel vote on a tied subject stays off the question that reaches it.
		Vote: channelTie, Query: "billing retries", WantFired: false,
	}, { // Test 5: The same channel vote fires on the question it was recorded on.
		Vote: channelTie, Query: "kafka lag", WantFired: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			base, _, found := votedResult(tieFixture(t, channel), test.Query, test.Vote)
			if !found {
				t.Fatalf("%q never returned the voted result, so the case proves nothing",
					test.Query)
			}
			ix := tieFixture(t, channel)
			ix.SetFeedback([]feedback.Entry{test.Vote})
			got, last, _ := votedResult(ix, test.Query, test.Vote)

			want := base
			if test.WantFired {
				want = base * ix.feedbackFactor(1)
			}
			if got < want*0.999 || got > want*1.001 {
				t.Errorf("score = %.4f, want %.4f (%.4f without the vote)", got, want, base)
			}
			if fired := last == "boosted by feedback"; fired != test.WantFired {
				t.Errorf("last reason = %q, vote fired = %t, want %t", last, fired, test.WantFired)
			}
		})
	}
}
