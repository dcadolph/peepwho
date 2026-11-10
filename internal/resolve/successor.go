package resolve

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// Successor tuning. Each constant bounds one evidence source so no single
// signal can hand the recommendation to a stranger.
const (
	// successorCandidates is how many people are scored per finding before the
	// best is named. It bounds work, not correctness: everyone with any of the
	// three kinds of evidence is a candidate.
	successorCandidates = 64
	// successorOverlapWeight scales shared-subject overlap. Overlap is the
	// broadest signal and the weakest per unit, because two people can share
	// every word of a subject without either being able to take it over.
	successorOverlapWeight = 1.0
	// successorPlaceWeight scales already-working-there evidence: work in the
	// same place, discounted the way every place score is. Somebody with real
	// commits beside the holder needs no introduction to the area.
	successorPlaceWeight = 3.0
	// successorReviewWeight scales having reviewed the holder's area. A
	// reviewer has read the code without writing it, which is more preparation
	// than overlap and less than authorship.
	successorReviewWeight = 2.0
	// successorMinShared is the fewest shared subjects that make somebody a
	// candidate on overlap alone; place or review evidence qualifies by
	// itself. It is an absolute count, not a fraction of the holder's
	// portfolio: dividing by everything a prolific person holds asked a
	// successor to match a career before being named, and on a real repository
	// that answered "nobody stands out" for exactly the people the report
	// leads with. Three real subjects is the same bar the identity rules use
	// for evidence that two records are one person.
	successorMinShared = 3
)

// SuccessorReason is one piece of evidence behind a succession candidate,
// phrased so it can be printed beside the name.
type SuccessorReason struct {
	// Kind is overlap, place, or review.
	Kind string `json:"kind"`
	// Detail is the printable sentence fragment, such as "holds 4 of the 6
	// subjects they hold" or "reviewed changes in internal/billing".
	Detail string `json:"detail"`
}

// Successor is the person best placed to become the second on a subject or
// place, with the evidence for saying so.
//
// The wording matters more than usual here. This is not a claim that the
// person can do the work, which nothing in a work record can establish; it is
// a claim about proximity: of everyone in the record, this person starts
// closest. The report says "nearest candidate", never "replacement".
type Successor struct {
	// ID is the candidate's canonical identifier.
	ID string `json:"id"`
	// Name is their display name.
	Name string `json:"name"`
	// Score orders candidates; it is comparable within one finding only.
	Score float64 `json:"score"`
	// Reasons is the evidence, strongest first, at most three entries.
	Reasons []SuccessorReason `json:"reasons"`
}

// successorInputs carries the cross-source evidence Successors draws on, so
// the assess pipeline computes each map once rather than per finding.
type successorInputs struct {
	// holders maps each salient topic to the people with real affinity for it.
	holders map[string]map[model.ID]bool
	// personTopics maps each person to their salient topic set, for overlap.
	personTopics map[model.ID]map[string]bool
	// placeWork maps directory to person to discounted work, from PlaceLeads
	// over the same tally the systems file is built from. Nil without git.
	placeWork map[string]map[model.ID]float64
	// reviewed maps directory to the people who took part in pull requests
	// that changed it without ranking among its top committers. Nil without
	// review data.
	reviewed map[string]map[model.ID]bool
}

// NewSuccessorInputs gathers the evidence for succession naming. places may be
// nil when no repository was read; reviewers may be nil when no review data
// was fetched. Both degrade the recommendation rather than disabling it, and
// what remains is subject overlap, which every source provides.
func NewSuccessorInputs(
	ix *index.Index, places []Place, reviewers map[string]map[model.ID]bool,
) *successorInputs {
	in := &successorInputs{
		holders:      topicHolders(ix),
		personTopics: make(map[model.ID]map[string]bool),
		reviewed:     reviewers,
	}
	for topic, people := range in.holders {
		for id := range people {
			if in.personTopics[id] == nil {
				in.personTopics[id] = make(map[string]bool)
			}
			in.personTopics[id][topic] = true
		}
	}
	if len(places) > 0 {
		in.placeWork = make(map[string]map[model.ID]float64, len(places))
		for _, pl := range places {
			m := make(map[model.ID]float64, len(pl.Holders))
			for _, h := range pl.Holders {
				m[model.ID(h.ID)] = h.Work
			}
			in.placeWork[pl.Dir] = m
		}
	}
	return in
}

// SuccessorFor names the person best placed to become the second on what
// rests on a holder: their whole body of subjects, and a place when a
// directory finding carries one (dir empty when there is none). It returns
// false when nobody clears the evidence floor, which the caller reports as
// its own finding rather than hiding.
//
// Overlap is judged over everything the holder holds rather than only the
// sole subjects, and not by accident: a sole subject has exactly one person
// in its record, so nobody can share it, and succession is about the body of
// work whoever steps in has to learn.
func (in *successorInputs) SuccessorFor(
	ix *index.Index, holder model.ID, dir string,
) (Successor, bool) {
	type tally struct {
		overlap int
		place   float64
		review  bool
	}
	evidence := make(map[model.ID]*tally)
	at := func(id model.ID) *tally {
		if id == holder {
			return nil
		}
		t := evidence[id]
		if t == nil {
			t = &tally{}
			evidence[id] = t
		}
		return t
	}

	held := in.personTopics[holder]
	for topic := range held {
		for id := range in.holders[topic] {
			if t := at(id); t != nil {
				t.overlap++
			}
		}
	}
	if dir != "" {
		for id, work := range in.placeWork[dir] {
			if t := at(id); t != nil {
				t.place = work
			}
		}
		for id := range in.reviewed[dir] {
			if t := at(id); t != nil {
				t.review = true
			}
		}
	}

	// The floor is eligibility, not a score: several real shared subjects, or
	// work in the place, or having reviewed it. Below that the answer is that
	// nobody stands out, which is a sharper finding than the least-bad
	// stranger, and naming a person on one shared word is the failure this
	// feature must never have.
	var maxPlace float64
	maxShared := 0
	for id, t := range evidence {
		if t.overlap < successorMinShared && t.place == 0 && !t.review {
			delete(evidence, id)
			continue
		}
		if t.place > maxPlace {
			maxPlace = t.place
		}
		if t.overlap > maxShared {
			maxShared = t.overlap
		}
	}
	if len(evidence) == 0 {
		return Successor{}, false
	}

	ids := slices.Sorted(maps.Keys(evidence))
	if len(ids) > successorCandidates {
		ids = ids[:successorCandidates]
	}

	// Each signal is normalized against the strongest candidate rather than
	// the holder, so the score reads as standing among the available people.
	// Ties break toward the lower id, which the sorted walk provides.
	best, bestScore := model.ID(""), 0.0
	for _, id := range ids {
		t := evidence[id]
		score := 0.0
		if maxShared > 0 {
			score += successorOverlapWeight * float64(t.overlap) / float64(maxShared)
		}
		if maxPlace > 0 {
			score += successorPlaceWeight * t.place / maxPlace
		}
		if t.review {
			score += successorReviewWeight
		}
		if score > bestScore {
			best, bestScore = id, score
		}
	}
	if best == "" {
		return Successor{}, false
	}

	t := evidence[best]
	out := Successor{ID: string(best), Name: personName(ix, best), Score: bestScore}
	if t.place > 0 && dir != "" {
		out.Reasons = append(out.Reasons, SuccessorReason{
			Kind:   "place",
			Detail: "already works in " + dir + " alongside them",
		})
	}
	if t.review && dir != "" {
		out.Reasons = append(out.Reasons, SuccessorReason{
			Kind:   "review",
			Detail: "has reviewed changes in " + dir,
		})
	}
	if t.overlap > 0 {
		shared := sharedOf(in.personTopics[best], held, 3)
		detail := fmt.Sprintf("holds %d of the %d subjects they hold", countShared(in.personTopics[best], held), len(held))
		if len(shared) > 0 {
			detail += ", including " + strings.Join(shared, ", ")
		}
		out.Reasons = append(out.Reasons, SuccessorReason{Kind: "overlap", Detail: detail})
	}
	return out, true
}

// countShared counts the subjects two people both hold.
func countShared(a, b map[string]bool) int {
	n := 0
	for topic := range a {
		if b[topic] {
			n++
		}
	}
	return n
}

// sharedOf names up to limit subjects two people both hold, alphabetically so
// a rebuild prints the same reasons.
func sharedOf(a, b map[string]bool, limit int) []string {
	var out []string
	for topic := range a {
		if b[topic] {
			out = append(out, topic)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
