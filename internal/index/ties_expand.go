package index

import (
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/model"
)

// Tie expansion bounds. Expansion trades precision for reach, so every bound
// here exists to keep the trade small: a couple of the strongest ties, from
// subjects the question actually named, discounted below every word the person
// typed.
const (
	// tieExpansionPenalty scales a tie-derived term's contribution. It sits
	// well under the synonym discount because a synonym is another name for
	// the same thing, while a tie is a different thing that travels with it.
	tieExpansionPenalty = 0.45
	// maxTieExpansions bounds how many tie-derived terms one query gains.
	maxTieExpansions = 3
	// maxTiesPerTopic bounds how many ties each named subject contributes.
	maxTiesPerTopic = 2
	// minTieWeight is the weakest tie worth expanding through.
	minTieWeight = 0.02
)

// tieAskedPrefix marks a tie-derived expansion in the asked map, so the
// penalty and the reason line can both tell it from a synonym.
const tieAskedPrefix = "~"

// tieExpand grows a resolved query with the subjects that travel with the ones
// it named, learned from the co-occurrence graph rather than from any list.
//
// This is the search path finally reading whotoask's most distinctive structure.
// A question about billing retries reaches the person who owns kafka-lag when
// the two move together in this organization's work, and the reason line says
// exactly that, because "they travel together here" is evidence a synonym
// table could never hold. Every expansion is discounted below every typed
// word, so reach never outranks what was actually asked.
func (ix *Index) tieExpand(originals []string, covers map[string][]string, asked map[string]string) []string {
	if len(originals) == 0 || len(ix.Graph.Topics) == 0 {
		return nil
	}
	type candidate struct {
		term   string
		origin string
		weight float64
	}
	var out []candidate
	seen := make(map[string]bool)
	for _, source := range queryTopics(originals) {
		// Only a compound subject defines travel. A bare word like "mobile"
		// or "releases" is a topic too, but its ties reach half the company:
		// expanding through singles handed four question shapes to neighbors
		// of the wrong subject on the big synthetic company (p@1 .9 to .72).
		// The salience test already draws this line for reporting; expansion
		// holds to the same one.
		if !strings.Contains(string(source.id), "-") {
			continue
		}
		topic := ix.Graph.Topics[source.id]
		if topic == nil || !topic.Salient() || len(topic.Near) == 0 {
			continue
		}
		ties := make([]model.ID, 0, len(topic.Near))
		for other := range topic.Near {
			ties = append(ties, other)
		}
		sort.Slice(ties, func(i, j int) bool {
			wi, wj := topic.Near[ties[i]].Weight, topic.Near[ties[j]].Weight
			if wi != wj {
				return wi > wj
			}
			return ties[i] < ties[j]
		})
		took := 0
		for _, other := range ties {
			if took >= maxTiesPerTopic {
				break
			}
			tie := topic.Near[other]
			o := ix.Graph.Topics[other]
			if tie.Weight < minTieWeight || o == nil || !o.Salient() {
				continue
			}
			took++
			for _, tok := range tokenize(strings.ReplaceAll(string(other), "-", " ")) {
				if seen[tok] {
					continue
				}
				seen[tok] = true
				out = append(out, candidate{
					term: tok, origin: string(source.id), weight: tie.Weight,
				})
			}
		}
	}
	// Strongest ties first, then the cap: three extra terms is reach, thirty
	// is a different question than the one asked.
	sort.SliceStable(out, func(i, j int) bool { return out[i].weight > out[j].weight })
	var added []string
	for _, c := range out {
		if len(added) >= maxTieExpansions {
			break
		}
		if _, dup := covers[c.term]; dup {
			continue
		}
		// A tie hit is evidence, never coverage. A synonym answers the words
		// asked; a tied subject merely travels with them, and crediting it as
		// coverage let a neighbor's owner outrank the owner of the thing
		// actually named — measured as a p@1 drop from .9 to .72 on the big
		// synthetic company before this line held it empty.
		covers[c.term] = nil
		asked[c.term] = tieAskedPrefix + c.origin
		added = append(added, c.term)
	}
	return added
}

// queryTopic is one subject a query named, with the typed words that named it.
type queryTopic struct {
	// id is the canonical topic.
	id model.ID
	// covers are the original terms the topic stands for.
	covers []string
}

// queryTopics finds the subjects a query names: each word alone, and each
// adjacent pair joined the way compound subjects are written. "billing
// retries" names billing, retries, and billing-retries; only the ones that
// exist in the graph matter, and the caller checks that.
func queryTopics(originals []string) []queryTopic {
	var out []queryTopic
	for i, w := range originals {
		out = append(out, queryTopic{id: topicID(w), covers: []string{w}})
		if i+1 < len(originals) {
			pair := originals[i] + "-" + originals[i+1]
			out = append(out, queryTopic{
				id: topicID(pair), covers: []string{originals[i], originals[i+1]},
			})
		}
	}
	return out
}
