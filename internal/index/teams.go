// Teams: the CODEOWNERS handles that own areas of record. They are kept out of
// the people ranking entirely and answered on their own, because a team is
// somewhere to look, not somebody to ask.

package index

import (
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/model"
)

// splitTeams separates the teams from the postings. A posting no team holds is
// shared with the people view rather than copied, so an index without teams
// pays nothing for the split. Every team also gains the words of its handle: a
// team called legal-squad answers a question about legal whatever its paths
// are named, and a team declared only on "*" has no other words at all.
func (ix *Index) splitTeams() (people, teams map[string]map[model.ID]float64) {
	people = make(map[string]map[model.ID]float64, len(ix.postings))
	teams = make(map[string]map[model.ID]float64)
	put := func(m map[string]map[model.ID]float64, key string, id model.ID, w float64) {
		if m[key] == nil {
			m[key] = make(map[model.ID]float64)
		}
		m[key][id] += w
	}
	for key, posting := range ix.postings {
		held := false
		for id := range posting {
			if id.IsTeam() {
				held = true
				break
			}
		}
		if !held {
			people[key] = posting
			continue
		}
		for id, w := range posting {
			if id.IsTeam() {
				put(teams, key, id, w)
			} else {
				put(people, key, id, w)
			}
		}
	}
	for id := range ix.Graph.People {
		if !id.IsTeam() {
			continue
		}
		for _, word := range handleWords(id) {
			put(teams, stem(word), id, weightTopic)
		}
	}
	return people, teams
}

// handleWords returns the words of a team's own name, the part of its handle
// after the organization: codeowners:simcorp/legal-squad gives legal and squad.
func handleWords(id model.ID) []string {
	s := string(id)
	if _, rest, ok := strings.Cut(s, ":"); ok {
		s = rest
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return tokenize(strings.NewReplacer("-", " ", "_", " ").Replace(s))
}

// SearchTeams ranks the teams for query and returns up to limit of them, each
// with the reasons it matched. A non-positive limit returns all. Teams rank only
// against each other, on their subjects and the words of their handles, so the
// answer can name the owners of record beside the people without a team ever
// taking a person's place.
func (ix *Index) SearchTeams(query string, limit int) []model.Match {
	if ix.teamN == 0 {
		return nil
	}
	terms, covers, asked := expandedQuery(query)
	if len(terms) == 0 {
		return nil
	}
	originals := 0
	for _, t := range terms {
		if _, isExp := asked[t]; !isExp {
			originals++
		}
	}
	resolved := resolveTerms(ix.teamPostings, ix.teamVocab, terms)
	applyExpansionPenalty(resolved, asked)
	scores, matched := scoreByTerms(ix.teamPostings, terms, resolved, ix.teamN, ix.teamLens)

	matches := make([]model.Match, 0, len(scores))
	for id, sc := range scores {
		p := ix.Graph.People[id]
		if p == nil {
			continue
		}
		reasons, evidence := ix.teamReasons(id, matched[id], resolved, asked)
		matches = append(matches, model.Match{
			Person:   p,
			Score:    sc * evidenceBoost(evidence),
			Strength: evidence * coveredShare(matched[id], covers, originals),
			Declared: true,
			Reasons:  reasons,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		oi := matchedOriginal(matched[matches[i].Person.ID], asked)
		oj := matchedOriginal(matched[matches[j].Person.ID], asked)
		if oi != oj {
			return oi
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Person.ID < matches[j].Person.ID
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// teamReasons describes, for each matched term, whether it hit one of the
// team's subjects or its handle, and returns the strongest evidence among them.
func (ix *Index) teamReasons(
	id model.ID, terms map[string]bool, resolved map[string]termHit, asked map[string]string,
) ([]string, float64) {
	pt := ix.texts[id]
	words := handleWords(id)
	out := make([]string, 0, len(terms))
	var evidence float64
	for term := range terms {
		hit := resolved[term]
		field, strength := "mention", evidenceMention
		var found string
		// take records the word a stem lookup found, so a switch case can
		// classify the hit and capture what it matched in the same test.
		take := func(word string, ok bool) bool {
			if ok {
				found = word
			}
			return ok
		}
		switch {
		case pt != nil && take(stemMatch(hit.key, pt.Topics...)):
			field, strength = "topic", evidenceTopic
		case take(stemMatch(hit.key, words...)):
			field, strength = "name", evidenceTopic
		}
		// mapsum: strength is a constant picked in this iteration, and max is exact in any order.
		evidence = max(evidence, strength)
		out = append(out, hitReason(term, field, found, hit, asked))
	}
	sort.Strings(out)
	return out, evidence
}
