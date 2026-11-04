package resolve

import (
	"maps"
	"slices"
	"sort"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// Span is a connection between two subjects that only one person has ever made.
//
// It is a different risk from a subject resting on one expert, and nothing that
// counts experts per subject can see it. Both areas may be well covered on
// their own while the work that crosses between them has only ever been done by
// one person, so what rests on them is not a subject but the knowledge that
// the two belong together. The person picking either one up afterwards has no
// reason to look at the other.
type Span struct {
	// Topics are the joined subjects, alphabetically. Two of them is the plain
	// case: one crossing, made by one person. More than two means the crossings
	// join up, and every one of them rests on the same person.
	Topics []string `json:"topics"`
	// Person is the display name of the only person who has worked across them.
	Person string `json:"person"`
	// PersonID is that person's canonical identifier.
	PersonID string `json:"personId"`
	// Together is how much of the time the subjects move as one thing. For a
	// group it is the weakest crossing holding it together, since that is what
	// the whole finding rests on.
	Together float64 `json:"together"`
	// Experts is how many people hold the subjects between them, which is what
	// makes the finding surprising: they are not short of experts.
	Experts int `json:"experts"`
}

// Size is how many subjects the finding joins.
func (s Span) Size() int { return len(s.Topics) }

// soleEdge is one crossing between two subjects made by one person, before the
// crossings that join up are gathered together.
type soleEdge struct {
	// A and B are the two subjects, alphabetically.
	A, B string
	// Who is the only person who has done focused work across them. A sweeping
	// refactor touching hundreds of areas at once is not evidence that anyone
	// understands how they connect, so it is not counted.
	Who model.ID
	// Weight is how much of the time the two move as one thing.
	Weight float64
}

// maxSpanRank is how far down a subject's ties a connection may sit and still
// be worth reporting. It must be among the strongest for BOTH subjects.
//
// This is a rank test rather than a floor on the weight, and deliberately so: a
// floor set by eye cuts the real ties along with the noise, because how much of
// the time two subjects move as one is a tiny number whenever both are also
// worked on alone. Rank is free of that. Measured on a real issue tracker it
// cut sixteen reported connections to seven and every one it dropped was noise:
// a weak tie between two broad subjects, where one person happening to be the
// only one who crossed says nothing about either.
const maxSpanRank = 3

// SoleSpans finds the connections between subjects that rest on one person,
// strongest first. A limit of zero or less returns all of them.
//
// Every surface that names a person has to be checked against the people who
// touch everything, who otherwise take every answer at once. This one is
// naturally resistant and was measured to be: on a real project the widest
// contributor, with fifteen thousand subjects to his name, is the sole witness
// to nothing at all. Being the only person who crossed between two areas is not
// a ranking, and somebody who touches everything has company everywhere, so the
// sweepers rule themselves out. Keep that property when changing this.
func SoleSpans(ix *index.Index, limit int) []Span {
	holders := topicHolders(ix)
	seen := make(map[[2]string]bool)
	var out []soleEdge
	for tid, topic := range ix.Graph.Topics {
		if !topic.Salient() {
			continue
		}
		for other, tie := range topic.Near {
			if tie.Witnesses != 1 || tie.Sole == "" {
				continue
			}
			o := ix.Graph.Topics[other]
			if o == nil || !o.Salient() {
				continue
			}
			if tieRank(topic, other) > maxSpanRank || tieRank(o, tid) > maxSpanRank {
				continue
			}
			a, b := string(tid), string(other)
			if a > b {
				a, b = b, a
			}
			key := [2]string{a, b}
			if seen[key] {
				continue
			}
			seen[key] = true

			// Who holds the subjects is counted once per group below, since
			// the point of the finding is that they are not short of experts.
			out = append(out, soleEdge{
				A: a, B: b, Who: ix.CanonicalID(tie.Sole), Weight: tie.Weight,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		// A more specific pairing comes first, so collapsing below keeps the
		// one that names the subjects most exactly.
		ni, nj := len(out[i].A)+len(out[i].B), len(out[j].A)+len(out[j].B)
		if ni != nj {
			return ni > nj
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	out = collapseSpans(out)

	spans := joinCrossings(ix, out, holders)
	if limit > 0 && len(spans) > limit {
		spans = spans[:limit]
	}
	return spans
}

// joinCrossings gathers the crossings that share a person and a subject into
// one finding.
//
// Reported one crossing at a time, a body of work that one person alone moves
// between reads as several unrelated findings: on a real repository the same
// person was the only one crossing between four subjects, and that arrived as
// five rows saying nearly the same thing. It is one thing to know and one
// person to lose.
func joinCrossings(ix *index.Index, edges []soleEdge, holders map[string]map[model.ID]bool) []Span {
	byPerson := make(map[model.ID][]soleEdge)
	for _, e := range edges {
		byPerson[e.Who] = append(byPerson[e.Who], e)
	}
	people := slices.Sorted(maps.Keys(byPerson))

	var out []Span
	for _, who := range people {
		mine := byPerson[who]
		// Which subjects this person's crossings reach from each other.
		near := make(map[string][]string, len(mine)*2)
		for _, e := range mine {
			near[e.A] = append(near[e.A], e.B)
			near[e.B] = append(near[e.B], e.A)
		}
		starts := slices.Sorted(maps.Keys(near))

		seen := make(map[string]bool, len(near))
		for _, start := range starts {
			if seen[start] {
				continue
			}
			group := []string{start}
			seen[start] = true
			for i := 0; i < len(group); i++ {
				for _, next := range near[group[i]] {
					if !seen[next] {
						seen[next] = true
						group = append(group, next)
					}
				}
			}
			sort.Strings(group)

			// The weakest crossing inside the group is what the finding rests
			// on, so it is the figure to report.
			inGroup := make(map[string]bool, len(group))
			for _, s := range group {
				inGroup[s] = true
			}
			weakest := 0.0
			for _, e := range mine {
				if !inGroup[e.A] {
					continue
				}
				if weakest == 0 || e.Weight < weakest {
					weakest = e.Weight
				}
			}
			everyone := make(map[model.ID]bool)
			for _, s := range group {
				for id := range holders[s] {
					everyone[id] = true
				}
			}
			out = append(out, Span{
				Topics: group, Person: personName(ix, who), PersonID: string(who),
				Together: weakest, Experts: len(everyone),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// A body of joined work is a heavier finding than a single crossing,
		// so size leads, the same way it does for joined work.
		if len(out[i].Topics) != len(out[j].Topics) {
			return len(out[i].Topics) > len(out[j].Topics)
		}
		if out[i].Together != out[j].Together {
			return out[i].Together > out[j].Together
		}
		return out[i].Topics[0] < out[j].Topics[0]
	})
	return out
}

// collapseSpans drops a finding that restates one already reported.
//
// A subject name that reads as several words registers as those words as well
// as the whole, so one connection between two areas arrives once per fragment:
// api with image-uploads, api with image, and api with uploads are a single
// thing said three times. Keeping only the most specific of a family says it
// once, which is the difference between a report somebody reads and a list they
// skim past.
func collapseSpans(spans []soleEdge) []soleEdge {
	out := make([]soleEdge, 0, len(spans))
	for _, s := range spans {
		restated := false
		for _, kept := range out {
			if sameSpan(kept, s) {
				restated = true
				break
			}
		}
		if !restated {
			out = append(out, s)
		}
	}
	return out
}

// sameSpan reports whether two findings name the same pair of areas. The pair
// is unordered, and either side may arrive as a fragment of its own name, so
// both orientations have to be tried.
func sameSpan(a, b soleEdge) bool {
	if util.SameFamily(a.A, b.A) && util.SameFamily(a.B, b.B) {
		return true
	}
	return util.SameFamily(a.A, b.B) && util.SameFamily(a.B, b.A)
}

// tieRank is where other sits among a topic's ties, strongest first, counting
// from one. A subject not tied to it at all ranks last of all.
func tieRank(t *model.Topic, other model.ID) int {
	rank := 1
	w := t.Near[other].Weight
	if w <= 0 {
		return len(t.Near) + 1
	}
	for id, tie := range t.Near {
		// Ties of equal weight are ordered by name, so the rank of a given
		// subject does not depend on map order.
		if tie.Weight > w || (tie.Weight == w && id < other) {
			rank++
		}
	}
	return rank
}
