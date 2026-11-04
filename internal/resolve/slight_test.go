package resolve

import (
	"fmt"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// TestDemoteSlightSubjects verifies a concentrated subject resting on less
// work than the median is levelled down rather than leading the report, while
// a concentrated subject carrying real work keeps its level.
func TestDemoteSlightSubjects(t *testing.T) {
	t.Parallel()
	risks := make([]TopicRisk, 0, 14)
	// Twelve ordinary subjects establish a median weight of 100.
	for range 12 {
		risks = append(risks, TopicRisk{Topic: "ordinary", Level: "ok", Weight: 100})
	}
	risks = append(risks,
		TopicRisk{Topic: "core", Level: "critical", Weight: 900},
		TopicRisk{Topic: "trivia", Level: "critical", Weight: 3},
		TopicRisk{Topic: "small-pair", Level: "elevated", Weight: 2},
	)
	demoteSlightSubjects(risks)

	byTopic := make(map[string]string)
	for _, r := range risks {
		if r.Topic != "ordinary" {
			byTopic[r.Topic] = r.Level
		}
	}
	if byTopic["core"] != "critical" {
		t.Errorf("core = %q, want critical: it carries far more than the median", byTopic["core"])
	}
	if byTopic["trivia"] != "elevated" {
		t.Errorf("trivia = %q, want elevated: one person holding almost no work "+
			"must not outrank the systems that matter", byTopic["trivia"])
	}
	if byTopic["small-pair"] != "ok" {
		t.Errorf("small-pair = %q, want ok", byTopic["small-pair"])
	}
}

// TestDemoteSlightSubjectsNeedsACorpus verifies a handful of subjects is left
// alone: a young organization where everything rests on one person deserves to
// hear that plainly rather than have it averaged away.
func TestDemoteSlightSubjectsNeedsACorpus(t *testing.T) {
	t.Parallel()
	risks := []TopicRisk{
		{Topic: "a", Level: "critical", Weight: 1},
		{Topic: "b", Level: "critical", Weight: 50},
	}
	demoteSlightSubjects(risks)
	for _, r := range risks {
		if r.Level != "critical" {
			t.Errorf("%s = %q, want critical kept with too few subjects to have a middle",
				r.Topic, r.Level)
		}
	}
}

// TestRiskDemotesSlightSubjects verifies Risk itself applies the weight floor,
// not merely that the helper works: a sole-expert subject touched once must
// not be reported as critical beside the systems the organization runs on.
func TestRiskDemotesSlightSubjects(t *testing.T) {
	t.Parallel()
	ix := index.New()
	var recs []connector.Record
	// A core subject many people work in heavily, so the corpus has a middle.
	for i := range 14 {
		topics := make([]string, 0, 40)
		for range 30 {
			topics = append(topics, "core")
		}
		topics = append(topics, fmt.Sprintf("area%d", i), fmt.Sprintf("area%d", i))
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Name: fmt.Sprintf("P%d", i),
			Email: fmt.Sprintf("p%d@x.com", i), Topics: topics, Source: "t",
		})
	}
	// One person who touched one trivial thing once.
	recs = append(recs, connector.Record{
		Kind: connector.KindPerson, Name: "Passer", Email: "passer@x.com",
		Topics: []string{"accordionpanel"}, Source: "t",
	})
	ix.Build(recs)
	ix.Canonicalize()

	for _, r := range Risk(ix, 0) {
		if r.Topic == "accordionpanel" && r.Level == "critical" {
			t.Errorf("a subject touched once is reported %q; Risk is not applying "+
				"the weight floor", r.Level)
		}
	}
}
