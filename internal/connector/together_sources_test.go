package connector

import (
	"fmt"
	"slices"
	"testing"
)

// TestTogetherPairsWhatOnePieceOfWorkNames checks a source whose unit of work
// names several subjects at once ties them to each other. Most sources are that
// shape: an issue carries labels, a page carries labels, and everything one of
// them names was worked on alongside everything else it named.
func TestTogetherPairsWhatOnePieceOfWorkNames(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	// Enough separate pieces of work for a pairing to be more than coincidence,
	// and enough of each subject for its ties to mean anything.
	for range minSubjectItems {
		ties.note([]string{"billing", "ledger"}, "ada@x.com", "PROJ")
		ties.note([]string{"search", "indexing"}, "bo@x.com", "PROJ")
	}
	// Padding so no subject is a majority of the work and gets set aside as
	// scaffolding.
	for i := range 40 {
		ties.note([]string{"filler", "other"}, "cy@x.com", "PROJ")
		_ = i
	}
	recs := ties.records("jira")
	if len(recs) == 0 {
		t.Fatal("nothing was tied to anything")
	}
	find := func(from, to string) (TopicLink, bool) {
		for _, r := range recs {
			if r.Kind != KindTopic || r.Name != from {
				continue
			}
			for _, l := range r.Links {
				if l.To == to {
					return l, true
				}
			}
		}
		return TopicLink{}, false
	}
	link, ok := find("billing", "ledger")
	if !ok {
		t.Fatalf("billing was not tied to ledger: %+v", recs)
	}
	if link.Witnesses != 1 || link.Sole != "ada@x.com" {
		t.Errorf("billing to ledger: %d people, sole %q; want the one who did that work",
			link.Witnesses, link.Sole)
	}
	if _, ok := find("billing", "search"); ok {
		t.Error("billing was tied to search, which no piece of work ever named alongside it")
	}
	if recs[0].Source != "jira" {
		t.Errorf("source = %q, want the source that observed it", recs[0].Source)
	}
}

// TestTogetherIgnoresWorkThatNamesEverything checks a piece of work touching a
// great many subjects ties none of them. A sweeping rename, or an issue tagged
// with the whole taxonomy, says nothing about what belongs with what.
func TestTogetherIgnoresWorkThatNamesEverything(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	sweeping := make([]string, 0, maxTogether+5)
	for i := range maxTogether + 5 {
		sweeping = append(sweeping, string(rune('a'+i%26))+"subject")
	}
	for range 10 {
		ties.note(sweeping, "ada@x.com", "PROJ")
	}
	if recs := ties.records("jira"); len(recs) != 0 {
		t.Errorf("records = %+v, want nothing tied by work that named everything", recs)
	}
}

// TestTogetherTreatsOneSubjectAsOneSubject checks that the same subject spelled
// differently by two sources is counted once. A pairing split across spellings
// falls below the floor that makes it trustworthy and vanishes silently.
func TestTogetherTreatsOneSubjectAsOneSubject(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	for i := range minSubjectItems {
		if i%2 == 0 {
			ties.note([]string{"Billing", " Ledger "}, "ada@x.com", "PROJ")
			continue
		}
		ties.note([]string{"billing", "ledger"}, "ada@x.com", "PROJ")
	}
	for range 40 {
		ties.note([]string{"filler", "other"}, "cy@x.com", "PROJ")
	}
	found := false
	for _, r := range ties.records("github") {
		if r.Name != "billing" {
			continue
		}
		for _, l := range r.Links {
			if l.To == "ledger" {
				found = true
			}
		}
	}
	if !found {
		t.Error("billing and ledger were counted as four subjects, so their pairing disappeared")
	}
}

// TestTogetherDropsSubjectsThatReachEverywhere checks a subject tied to most of
// the vocabulary is removed along with every tie to it.
//
// The graph's own ubiquity rule cannot see these. Measured on a real issue
// tracker, the label a bot puts on every ticket with a patch attached is carried
// by a sixth of the contributors, far under the share of people that marks
// scaffolding, while being tied to seventy per cent of every subject there.
func TestTogetherDropsSubjectsThatReachEverywhere(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	// A vocabulary big enough for a share of it to mean something, in which one
	// label rides along with every real pairing.
	areas := []string{}
	for i := range 30 {
		areas = append(areas, fmt.Sprintf("area%02d", i))
	}
	for i := 0; i+1 < len(areas); i += 2 {
		for range minTogether + 2 {
			ties.note([]string{areas[i], areas[i+1], "patch-available"}, "ada@x.com", "PROJ")
		}
	}
	recs := ties.records("jira")
	for _, r := range recs {
		if r.Name == "patch-available" {
			t.Error("the label that rides along with everything was kept as a subject")
		}
		for _, l := range r.Links {
			if l.To == "patch-available" {
				t.Errorf("%s is still tied to the label that rides along with everything", r.Name)
			}
		}
	}
	// The real pairings underneath it must survive.
	found := false
	for _, r := range recs {
		if r.Name != "area00" {
			continue
		}
		for _, l := range r.Links {
			if l.To == "area01" {
				found = true
			}
		}
	}
	if !found {
		t.Error("area00 lost its real tie to area01 along with the scaffolding")
	}
}

// TestTogetherKeepsScaffoldingRuleOffForSmallVocabularies checks the share is
// not applied when there is too little vocabulary for it to mean anything. Among
// a handful of subjects everything is tied to most of the rest.
func TestTogetherKeepsScaffoldingRuleOffForSmallVocabularies(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	for range minTogether + 2 {
		ties.note([]string{"billing", "ledger"}, "ada@x.com", "PROJ")
		ties.note([]string{"search", "indexing"}, "bo@x.com", "PROJ")
	}
	if got := len(ties.records("jira")); got == 0 {
		t.Error("a four-subject vocabulary was emptied by a rule about reaching across one")
	}
}

// TestTogetherDropsSubjectsThatMeanTheSameEverywhere checks a subject appearing
// in most of a source's containers is removed, and one that stays inside a
// container is kept, even when the two have the same number of ties.
//
// This is the case no measure of the tie graph can settle. Measured on a real
// tracker, documentation and sql have almost the same number of ties and the
// same neighbourhood shape; degree, neighbour clustering, and how far the
// neighbourhood falls apart without them all rank documentation as the more
// subject-like of the two. What separates them is that sql lives in two
// projects and documentation lives in all five.
func TestTogetherDropsSubjectsThatMeanTheSameEverywhere(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	projects := []string{"KAFKA", "SPARK", "FLINK", "HADOOP"}
	for _, p := range projects {
		for range minTogether + 2 {
			// "docs" rides along in every project; "engine" belongs to one.
			ties.note([]string{"docs", p + "-area"}, "ada@x.com", p)
		}
	}
	for range minTogether + 2 {
		ties.note([]string{"engine", "KAFKA-area"}, "bo@x.com", "KAFKA")
	}
	var kept []string
	for _, r := range ties.records("jira") {
		kept = append(kept, r.Name)
		for _, l := range r.Links {
			if l.To == "docs" {
				t.Errorf("%s is still tied to the subject that means the same in every project", r.Name)
			}
		}
	}
	if slices.Contains(kept, "docs") {
		t.Error("the subject appearing in every project was kept")
	}
	if !slices.Contains(kept, "engine") {
		t.Errorf("the subject belonging to one project was dropped; kept %v", kept)
	}
}

// TestTogetherKeepsSpreadRuleOffForOneContainer checks the rule stays silent
// when a source has too few containers to show anything. A single repository
// cannot demonstrate a subject staying inside one.
func TestTogetherKeepsSpreadRuleOffForOneContainer(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	for range minTogether + 2 {
		ties.note([]string{"billing", "ledger"}, "ada@x.com", "only-repo")
		ties.note([]string{"search", "indexing"}, "bo@x.com", "only-repo")
	}
	if got := len(ties.records("git")); got == 0 {
		t.Error("a single-container source was emptied by a rule about spanning containers")
	}
}

// TestTogetherIgnoresProcessLabels checks the labels that describe what is being
// done to a piece of work, rather than what it is about, form no ties.
//
// They cannot be recognized by any shape in the graph, which is why they are
// listed. What the list must not do is reach past ties: somebody can still be
// the person who knows the documentation, so the subject itself has to survive
// everywhere except in what it is related to.
func TestTogetherIgnoresProcessLabels(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	for range minTogether + 2 {
		ties.note([]string{"billing", "ledger", "good-first-issue"}, "ada@x.com", "PROJ")
		ties.note([]string{"search", "documentation"}, "bo@x.com", "PROJ")
	}
	for _, r := range ties.records("github") {
		if processLabels[r.Name] {
			t.Errorf("%q was tied to something, though it says nothing about what work is about", r.Name)
		}
		for _, l := range r.Links {
			if processLabels[l.To] {
				t.Errorf("%s is tied to the process label %q", r.Name, l.To)
			}
		}
	}
	// The real pairing alongside the process label still stands.
	found := false
	for _, r := range ties.records("github") {
		if r.Name != "billing" {
			continue
		}
		for _, l := range r.Links {
			if l.To == "ledger" {
				found = true
			}
		}
	}
	if !found {
		t.Error("billing lost its tie to ledger because a process label shared the ticket")
	}
}

// TestTogetherIgnoresWordsOfOneName checks two words of a single compound name
// are not read as two subjects meeting.
//
// Every file under data_grand_lyon names data, grand, and lyon, so those three
// appear together constantly while saying only how one integration is spelled.
// A subject that names a directory of its own is real wherever else it turns
// up, so one standing side keeps the pairing: that is what lets energy stay
// tied to ovo and srp, which an earlier attempt at this cut by mistake.
func TestTogetherIgnoresWordsOfOneName(t *testing.T) {
	t.Parallel()
	ties := newTogether()
	// energy names a directory of its own; ovo, grand, and lyon never do.
	ties.standing([]string{"energy", "ovo_energy", "data_grand_lyon", "sensor"})
	for range minTogether + 3 {
		ties.note([]string{"energy", "ovo"}, "ada@x.com", "repo")
		ties.note([]string{"grand", "lyon"}, "bo@x.com", "repo")
	}
	for range 40 {
		ties.note([]string{"filler", "other"}, "cy@x.com", "repo")
	}
	tied := func(a, b string) bool {
		for _, r := range ties.records("git") {
			if r.Name != a {
				continue
			}
			for _, l := range r.Links {
				if l.To == b {
					return true
				}
			}
		}
		return false
	}
	if !tied("energy", "ovo") {
		t.Error("energy lost its tie to ovo, though energy names a directory of its own")
	}
	if tied("grand", "lyon") {
		t.Error("grand was tied to lyon, though neither names anything but a piece of one name")
	}
}
