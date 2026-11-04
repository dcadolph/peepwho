package resolve

import (
	"math"
	"sort"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// Region is a connected body of work resting on one person: subjects tied to
// each other because they are worked on together, where the same person leads
// every one of them.
//
// It is a different finding from a subject with a bus factor of one, and a
// worse one. Ten unrelated subjects each held by one person are ten small
// risks. Ten subjects that change together and are all led by the same person
// are one large risk, because whoever picks the work up has to learn the whole
// region at once, and nothing in a per-subject report shows that.
type Region struct {
	// Lead is the display name of the person who leads every subject in it.
	Lead string `json:"lead"`
	// LeadID is that person's canonical identifier.
	LeadID string `json:"leadId"`
	// Topics are the connected subjects, alphabetically.
	Topics []string `json:"topics"`
}

// Size is how many subjects the region spans.
func (r Region) Size() int { return len(r.Topics) }

// minRegion is the fewest joined subjects worth reporting as a region. Two is
// a pair, which per-subject risk already shows well enough.
const minRegion = 3

// Regions finds the connected bodies of work that rest on a single person,
// largest first. A limit of zero or less returns all of them.
func Regions(ix *index.Index, limit int) []Region {
	lead := leads(ix)

	// Adjacency between subjects, kept to those a person actually leads so the
	// walk below only crosses ties inside one person's work.
	near := make(map[model.ID][]model.ID, len(lead))
	for tid := range lead {
		for other := range ix.Graph.Topics[tid].Near {
			if _, ok := lead[other]; ok {
				near[tid] = append(near[tid], other)
			}
		}
	}

	ids := make([]model.ID, 0, len(lead))
	for tid := range lead {
		ids = append(ids, tid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var out []Region
	seen := make(map[model.ID]bool, len(ids))
	for _, start := range ids {
		if seen[start] {
			continue
		}
		who := lead[start]
		// Walk outwards while the same person still leads, so a region stops at
		// the edge of what one person holds rather than running through the
		// whole graph.
		group := []model.ID{start}
		seen[start] = true
		for i := 0; i < len(group); i++ {
			for _, next := range near[group[i]] {
				if seen[next] || lead[next] != who {
					continue
				}
				seen[next] = true
				group = append(group, next)
			}
		}
		if len(group) < minRegion {
			continue
		}
		names := make([]string, 0, len(group))
		for _, tid := range group {
			names = append(names, string(tid))
		}
		sort.Strings(names)
		out = append(out, Region{Lead: personName(ix, who), LeadID: string(who), Topics: names})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Topics) != len(out[j].Topics) {
			return len(out[i].Topics) > len(out[j].Topics)
		}
		return out[i].Topics[0] < out[j].Topics[0]
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// leads names the person every real subject rests on, in one pass.
//
// It is deliberately not the largest raw weight. Every code base has a few
// people who touch all of it, and by raw weight they out-hold the owner of
// almost every subject at once, which handed a maintainer's own work to a
// passer-by in five separate places before this was written down once: the
// holdout metric, ownership drift, the risk tie-break, joined work, and the
// departure view. Weight is discounted by the square root of everything that
// person does, and only work counts, never a subject a source of record merely
// assigned them.
func leads(ix *index.Index) map[model.ID]model.ID {
	out := make(map[model.ID]model.ID)
	for tid, ranked := range Leaders(ix, 1) {
		out[tid] = ranked[0].id
	}
	return out
}

// leader is one person's standing on one subject under the lead score.
type leader struct {
	// id is the person's canonical identifier.
	id model.ID
	// Name is their display name.
	Name string
	// score is work on the subject discounted by the square root of all work.
	score float64
}

// ID returns the leader's canonical identifier as a string.
func (l leader) ID() string { return string(l.id) }

// Leaders returns, for every salient subject, the k people it most rests on
// under the same breadth-discounted score leads uses. It exists as its own
// entry point so a benchmark judging the ownership claim scores exactly the
// formula the product ships, rather than a copy that drifts.
func Leaders(ix *index.Index, k int) map[model.ID][]leader {
	out := make(map[model.ID][]leader)
	for id, p := range ix.Graph.People {
		// Summed in identifier order. Float addition is not associative and Go
		// randomizes map iteration, so accumulating a career total as the map
		// yields it gives a different last bit each run, and this total is a
		// divisor for every score below it.
		var total float64
		for _, tid := range sortedTopics(p.Topics) {
			if work := p.Topics[tid] - p.Stated[tid]; work > 0 {
				total += work
			}
		}
		if total <= 0 {
			continue
		}
		root := math.Sqrt(total)
		canon := ix.CanonicalID(id)
		for tid, w := range p.Topics {
			here := w - p.Stated[tid]
			if here <= 0 || !ix.Graph.Topics[tid].Salient() {
				continue
			}
			out[tid] = append(out[tid], leader{id: canon, score: here / root})
		}
	}
	for tid, ranked := range out {
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].id < ranked[j].id
		})
		// One person can appear under several uncanonicalized identities that
		// resolve to one canonical id; keep the first of each.
		kept := ranked[:0]
		seen := make(map[model.ID]bool)
		for _, l := range ranked {
			if seen[l.id] {
				continue
			}
			seen[l.id] = true
			l.Name = personName(ix, l.id)
			kept = append(kept, l)
			if len(kept) == k {
				break
			}
		}
		out[tid] = kept
	}
	return out
}
