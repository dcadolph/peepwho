package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/feedback"
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
