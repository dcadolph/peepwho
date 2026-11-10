package resolve

import (
	"slices"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestRelated checks that topics held by the same people are reported as
// related, that an unrelated topic is not, and that a smaller expert set reads
// as the narrower topic.
func TestRelated(t *testing.T) {
	t.Parallel()
	ix := index.New()
	g := ix.Graph
	topic := func(id string) {
		g.Topics[model.ID(id)] = &model.Topic{ID: model.ID(id), Name: id, Curated: true}
	}
	topic("billing")
	topic("billing-retries")
	topic("kafka")
	person := func(id string, topics ...string) {
		p := &model.Person{ID: model.ID(id), Topics: make(map[model.ID]float64)}
		for _, tp := range topics {
			p.Topics[model.ID(tp)] = 1
		}
		g.People[model.ID(id)] = p
	}
	// Everyone on retries also does billing; billing has one extra person, so
	// retries is the narrower topic. Nobody who does billing touches kafka.
	person("a@x.com", "billing", "billing-retries")
	person("b@x.com", "billing", "billing-retries")
	person("c@x.com", "billing")
	person("d@x.com", "kafka")

	rel := Related(ix, "billing", 0)
	if len(rel) != 1 {
		t.Fatalf("Related(billing) = %+v, want just billing-retries", rel)
	}
	if rel[0].Topic != "billing-retries" {
		t.Errorf("related topic = %q, want billing-retries", rel[0].Topic)
	}
	if !rel[0].Narrower {
		t.Errorf("billing-retries should read as narrower than billing: %+v", rel[0])
	}
	if got := rel[0].Overlap; got != 1 {
		t.Errorf("overlap = %v, want 1: every retries expert also does billing", got)
	}
	if rel := Related(ix, "kafka", 0); len(rel) != 0 {
		t.Errorf("Related(kafka) = %+v, want none: no shared experts", rel)
	}
	if rel := Related(ix, "nonexistent", 0); rel != nil {
		t.Errorf("Related(nonexistent) = %+v, want nil", rel)
	}
}

// TestTopicGroups checks the two guards that keep folding honest: a fragment
// folds into its compound only when the same people hold both, a lone expert's
// unrelated topics never fuse, and a genuinely broader topic keeps its identity.
func TestTopicGroups(t *testing.T) {
	t.Parallel()
	ix := index.New()
	g := ix.Graph
	topic := func(id string) {
		g.Topics[model.ID(id)] = &model.Topic{ID: model.ID(id), Name: id, Curated: true}
	}
	person := func(id string, topics ...string) {
		p := &model.Person{ID: model.ID(id), Topics: make(map[model.ID]float64)}
		for _, tp := range topics {
			p.Topics[model.ID(tp)] = 1
		}
		g.People[model.ID(id)] = p
	}
	for _, id := range []string{
		"billing", "retries", "billing-retries",
		"kubernetes", "kubernetes-deploys", "kafka",
	} {
		topic(id)
	}
	// The billing people all work on billing-retries, so those words are that
	// subject said shorter. Kubernetes is broader: most of its experts never
	// touch deploys. Zoe is the lone expert on two unrelated subjects.
	person("a@x.com", "billing", "retries", "billing-retries", "kubernetes")
	person("b@x.com", "billing", "retries", "billing-retries", "kubernetes")
	person("c@x.com", "kubernetes")
	person("d@x.com", "kubernetes")
	person("e@x.com", "kubernetes", "kubernetes-deploys")
	person("zoe@x.com", "kafka")

	groups := topicGroups(ix)
	if got := groups["billing"]; got != "billing-retries" {
		t.Errorf("billing folded to %q, want billing-retries", got)
	}
	if got := groups["retries"]; got != "billing-retries" {
		t.Errorf("retries folded to %q, want billing-retries", got)
	}
	if got := groups["billing-retries"]; got != "billing-retries" {
		t.Errorf("billing-retries folded to %q, want itself", got)
	}
	// Only one of four kubernetes experts does deploys, so the broader topic
	// keeps its own identity rather than collapsing into the narrower one.
	if got := groups["kubernetes"]; got != "kubernetes" {
		t.Errorf("kubernetes folded to %q, want itself: it is the broader subject", got)
	}
	// Kafka shares no word with anything, so it stands alone even though its
	// only expert holds nothing else.
	if got := groups["kafka"]; got != "kafka" {
		t.Errorf("kafka folded to %q, want itself", got)
	}

	// Rolled up, the three billing names report as one risk rather than three.
	var billing []TopicRisk
	for _, r := range Risk(ix, 0) {
		if r.Topic == "billing-retries" || r.Topic == "billing" || r.Topic == "retries" {
			billing = append(billing, r)
		}
	}
	if len(billing) != 1 {
		t.Fatalf("billing risks = %+v, want one rolled-up entry", billing)
	}
	if want := []string{"billing", "retries"}; !slices.Equal(billing[0].Includes, want) {
		t.Errorf("includes = %v, want %v", billing[0].Includes, want)
	}
}

// TestRelatedLeadsWithWhatChangedTogether checks the stronger evidence goes
// first and says so. Subjects touched by one piece of work are related whoever
// changed them; subjects with the same experts may only share one person, and
// that kind of relatedness cannot be evidence about people without arguing in
// a circle.
func TestRelatedLeadsWithWhatChangedTogether(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Ada", Email: "ada@x.com",
			Topics: []string{"billing", "ledger", "unrelated"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Bo", Email: "bo@x.com",
			Topics: []string{"billing", "unrelated"}, Source: "git"},
		{Kind: connector.KindTopic, Name: "billing", Source: "git",
			Links: []connector.TopicLink{{To: "ledger", Weight: 0.5}}},
	})
	ix.Canonicalize()

	got := Related(ix, "billing", 5)
	if len(got) == 0 {
		t.Fatal("billing has no related subjects at all")
	}
	if got[0].Topic != "ledger" {
		t.Errorf("first related = %q, want ledger, which the work ties to billing", got[0].Topic)
	}
	if got[0].Because != becauseTogether {
		t.Errorf("evidence = %q, want it to say the two were worked on together", got[0].Because)
	}
	// The weaker kind still appears, so nothing is lost by preferring the other.
	var sawExperts bool
	for _, r := range got {
		if r.Because == becauseExperts {
			sawExperts = true
		}
	}
	if !sawExperts {
		t.Errorf("related = %+v, want shared-expert relations still listed behind", got)
	}
}
