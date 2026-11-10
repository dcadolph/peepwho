package simorg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// Brain floors for the big company. The gauntlet asks about every subject in
// eight styles, so these are floors over hundreds of questions, not a lucky
// handful. Raise them when ranking genuinely improves; never lower one to make
// a build pass.
const (
	// minBrainTop1 is the share of questions the right person must win
	// outright, across every style except blind paraphrase.
	minBrainTop1 = 0.92
	// minBrainTop3 is the share where the right person must at least be
	// visible, same scope.
	minBrainTop3 = 0.95
	// minEverydayTop1 is the floor for the everyday-language battery: the
	// questions people actually type, synonyms and all. These must not regress,
	// because they are the first thing anyone tries.
	minEverydayTop1 = 0.90
)

// brainCase is one asked question with its known right answer.
type brainCase struct {
	// style groups the question for the per-style breakdown.
	style string
	// query is the question as someone would type it.
	query string
	// want is the person who should come back.
	want model.ID
	// alsoRight are the other planted experts for the same subject. The corpus
	// deliberately gives some subjects co-equal voices for the risk spread, so
	// any of them first is a correct answer; want must still be visible.
	alsoRight map[model.ID]bool
}

// mangle introduces one deterministic typo: the middle character of the longest
// word is doubled, which stays within one edit of the original.
func mangle(topic string) string {
	words := strings.Fields(topic)
	longest := 0
	for i, w := range words {
		if len(w) > len(words[longest]) {
			longest = i
		}
	}
	w := words[longest]
	mid := len(w) / 2
	words[longest] = w[:mid] + string(w[mid]) + w[mid:]
	return strings.Join(words, " ")
}

// brainGauntlet builds the asked questions for every subject the company has.
func brainGauntlet(c *company) []brainCase {
	var cases []brainCase
	for s, subj := range subjects {
		want := c.owners[s].who.canonical()
		co := make(map[model.ID]bool)
		for _, p := range c.people {
			for _, tp := range p.topics {
				if tp == s {
					co[model.ID(p.email)] = true
				}
			}
		}
		add := func(style, query string) {
			cases = append(cases, brainCase{style: style, query: query, want: want, alsoRight: co})
		}
		add("direct", "who knows about "+subj.Topic)
		add("talk-to", "who do I talk to about "+subj.Topic)
		add("owns", "who owns "+subj.Topic)
		add("help", "I need help with "+subj.Topic)
		add("trouble", subj.Topic+" is acting up again")
		add("jargon", subj.Words[2]+" "+subj.Words[3])
		add("typo", "who knows about "+mangle(subj.Topic))
		add("anchored", subj.Anchored)
	}
	return cases
}

// scoreBrain runs the cases and returns per-style hits at one and three, with
// the misses described for the log.
func scoreBrain(ix *index.Index, cases []brainCase) (map[string][3]int, []string) {
	byStyle := make(map[string][3]int)
	var misses []string
	for _, bc := range cases {
		got := ix.Search(bc.query, 3)
		tally := byStyle[bc.style]
		tally[2]++
		rank := -1
		for i, m := range got {
			if m.Person.ID == bc.want {
				rank = i
				break
			}
		}
		topIsExpert := len(got) > 0 &&
			(got[0].Person.ID == bc.want || bc.alsoRight[got[0].Person.ID])
		switch {
		case rank == 0, topIsExpert && rank > 0:
			tally[0]++
			tally[1]++
		case rank > 0:
			tally[1]++
		default:
			top := "nothing"
			if len(got) > 0 {
				top = string(got[0].Person.ID)
			}
			misses = append(misses, fmt.Sprintf("%s: %q wanted %s got %s", bc.style, bc.query, bc.want, top))
		}
		byStyle[bc.style] = tally
	}
	return byStyle, misses
}

// TestBigCompanyBrain runs the ranking gauntlet on the company the public demo
// serves: every subject asked eight ways, scored against the planted owner. It
// is the answer to "is the brain actually good" in a form a regression cannot
// slip past.
func TestBigCompanyBrain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ix, err := BuildBigIndex(dir)
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	c := buildCompany(BigSpec())
	cases := brainGauntlet(c)

	byStyle, misses := scoreBrain(ix, cases)
	var top1, top3, asked int
	for style, tally := range byStyle {
		t.Logf("%-9s asked=%2d p@1=%.2f p@3=%.2f",
			style, tally[2], float64(tally[0])/float64(tally[2]), float64(tally[1])/float64(tally[2]))
		if style == "anchored" {
			// Anchored questions are paraphrases with one remembered word; they
			// are reported for visibility and floored by the small-org harness.
			continue
		}
		top1 += tally[0]
		top3 += tally[1]
		asked += tally[2]
	}
	p1 := float64(top1) / float64(asked)
	p3 := float64(top3) / float64(asked)
	t.Logf("overall (sans anchored): asked=%d p@1=%.2f p@3=%.2f", asked, p1, p3)
	for i, m := range misses {
		if i >= 12 {
			t.Logf("  ... and %d more misses", len(misses)-12)
			break
		}
		t.Logf("  miss %s", m)
	}
	if p1 < minBrainTop1 {
		t.Errorf("brain p@1 = %.2f, want at least %.2f", p1, minBrainTop1)
	}
	if p3 < minBrainTop3 {
		t.Errorf("brain p@3 = %.2f, want at least %.2f", p3, minBrainTop3)
	}
}

// TestEverydayLanguageBrain asks the questions people actually type on their
// first day: everyday words, synonyms, and abbreviations, none of which are
// the index's own vocabulary. Every one must reach the person the company
// routes that question to.
func TestEverydayLanguageBrain(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	c := buildCompany(BigSpec())
	ownerOf := func(topic string) model.ID {
		for s := range subjects {
			if subjects[s].Topic == topic {
				return c.owners[s].who.canonical()
			}
		}
		t.Fatalf("no subject %q", topic)
		return ""
	}
	// The planted co-experts of a subject are correct answers too; the corpus
	// makes them near-equal on purpose so the risk view has a spread.
	expertsOf := func(topic string) map[model.ID]bool {
		co := make(map[model.ID]bool)
		for s := range subjects {
			if subjects[s].Topic != topic {
				continue
			}
			for _, p := range c.people {
				for _, tp := range p.topics {
					if tp == s {
						co[model.ID(p.email)] = true
					}
				}
			}
		}
		return co
	}

	cases := []brainCase{
		{style: "everyday", query: "who do I talk to about vacation", want: ownerOf("vacation")},
		{style: "everyday", query: "who do I talk to about time off", want: ownerOf("vacation")},
		{style: "everyday", query: "how do days off work here", want: ownerOf("vacation")},
		{style: "everyday", query: "who handles pto", want: ownerOf("vacation")},
		{style: "everyday", query: "something looks wrong on my paycheck", want: ownerOf("payroll taxes")},
		{style: "everyday", query: "who handles salary questions", want: ownerOf("payroll taxes")},
		{style: "everyday", query: "how do I get expenses reimbursed", want: ownerOf("expense reports")},
		{style: "everyday", query: "submitting receipts from a work trip", want: ownerOf("expense reports")},
		{style: "everyday", query: "who handles health insurance enrollment", want: ownerOf("health benefits")},
		{style: "everyday", query: "adding my kid to dental coverage", want: ownerOf("health benefits")},
		{style: "everyday", query: "who reviews an nda", want: ownerOf("contract review")},
		{style: "everyday", query: "who sets up interviews for candidates", want: ownerOf("hiring interviews")},
		{style: "everyday", query: "questions about new hire orientation", want: ownerOf("onboarding paperwork")},
		{style: "everyday", query: "who knows k8s", want: ownerOf("kubernetes deploys")},
		{style: "everyday", query: "kube rollouts keep failing", want: ownerOf("kubernetes deploys")},
		{style: "everyday", query: "who owns auth", want: ownerOf("sso login")},
		{style: "everyday", query: "sign in problems this morning", want: ownerOf("sso login")},
		{style: "everyday", query: "who to page about an outage", want: ownerOf("oncall paging")},
	}
	topicOf := map[model.ID]string{}
	for s := range subjects {
		topicOf[c.owners[s].who.canonical()] = subjects[s].Topic
	}
	for i := range cases {
		cases[i].alsoRight = expertsOf(topicOf[cases[i].want])
	}
	byStyle, misses := scoreBrain(ix, cases)
	tally := byStyle["everyday"]
	p1 := float64(tally[0]) / float64(tally[2])
	t.Logf("everyday: asked=%d p@1=%.2f p@3=%.2f",
		tally[2], p1, float64(tally[1])/float64(tally[2]))
	for _, m := range misses {
		t.Logf("  miss %s", m)
	}
	if p1 < minEverydayTop1 {
		t.Errorf("everyday p@1 = %.2f, want at least %.2f", p1, minEverydayTop1)
	}
}

// TestSquadOwnedAreasJudgedInTheGauntlet holds the squad-to-team bridge inside
// the generated company: the areas CODEOWNERS declares by squad handle match
// org-chart teams, so none of them is set aside as group-owned, and each one
// whose planted owner still leads it stands held rather than lost to the
// bridge.
func TestSquadOwnedAreasJudgedInTheGauntlet(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	report := resolve.Ownership(ix)
	if report.GroupOwned != 0 {
		t.Errorf("groupOwned = %d, want every squad matched to its team", report.GroupOwned)
	}
	squadTopics := make(map[string]bool)
	for s := range subjects {
		if s%5 != 0 && s%7 == 3 {
			squadTopics[strings.ReplaceAll(subjects[s].Topic, " ", "-")] = true
		}
	}
	if len(squadTopics) == 0 {
		t.Fatal("the generator planted no squad-owned areas")
	}
	held := make(map[string]bool)
	for _, a := range resolve.OwnedAreas(ix) {
		if a.Standing == resolve.StandingHeld {
			held[a.Topic] = true
		}
	}
	for topic := range squadTopics {
		if !held[topic] {
			t.Errorf("squad-owned area %q is not held through its team's members", topic)
		}
	}
}
