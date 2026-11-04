package resolve

import (
	"sort"
	"strings"

	"github.com/agnivade/levenshtein"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// namePrefixes are the ways a person's name gets wrapped in a question. They
// are stripped before matching so "who is holly dunn" is read as the name it
// contains.
var namePrefixes = []string{
	"who is ", "who's ", "whos ", "who was ", "who are ",
	"tell me about ", "what does ", "what do ", "about ",
}

// nameSuffixes are the tails those questions end with, such as "what does
// holly dunn know about".
var nameSuffixes = []string{
	" know about", " know", " work on", " do", " does", " own", " owns",
}

// nameTypoLen is the shortest token allowed to match with a typo. Below it a
// single edit is most of the word, and "dan" would answer for "dave".
const nameTypoLen = 4

// nameMatch returns the people a question names outright, best first. Asking a
// colleague's name is one of the most natural things to type into something
// that finds people, and expertise ranking alone cannot answer it: a name is
// identity, not a subject, so it is deliberately not part of the expertise
// index, and without this "Holly Dunn" returns whoever merely mentioned a
// similar word.
//
// The match is deliberately strict, because a loose one would let any person
// whose surname resembles a common word outrank the expert on that word. Only
// the whole question naming the whole person counts: their exact name, their
// email, or every word of a multi-word question matching a part of their name,
// which is what lets a typo through without opening the door to single words.
func nameMatch(ix *index.Index, query string, limit int) []model.Match {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.TrimRight(q, "?!. ")
	for _, p := range namePrefixes {
		if after, ok := strings.CutPrefix(q, p); ok {
			q = strings.TrimSpace(after)
			break
		}
	}
	for _, s := range nameSuffixes {
		if before, ok := strings.CutSuffix(q, s); ok {
			q = strings.TrimSpace(before)
			break
		}
	}
	if q == "" {
		return nil
	}
	asked := strings.Fields(q)

	type scored struct {
		person *model.Person
		score  float64
	}
	var hits []scored
	for _, p := range ix.Graph.People {
		name := strings.ToLower(p.Name)
		email := strings.ToLower(p.Email)
		local, _, _ := strings.Cut(email, "@")
		switch {
		case name != "" && name == q:
			hits = append(hits, scored{p, 1})
		case email != "" && email == q:
			hits = append(hits, scored{p, 0.95})
		case email != "" && local == q && !namesASubject(ix, q):
			// A mailbox whose name matches the question only identifies a
			// person when the question was not about a subject. Plenty of
			// addresses are called github, billing, or support, and reading
			// those as "the person you mean" put a mailbox above everybody who
			// actually works on the thing being asked about.
			hits = append(hits, scored{p, 0.95})
		case len(asked) > 1 && nameCoversAll(name, asked):
			hits = append(hits, scored{p, 0.9})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].person.ID < hits[j].person.ID
	})

	out := make([]model.Match, 0, len(hits))
	for i, h := range hits {
		if limit > 0 && i >= limit {
			break
		}
		var team *model.Team
		if h.person.TeamID != "" {
			team = ix.Graph.Teams[h.person.TeamID]
		}
		reasons := []string{"name"}
		// Answering "who is this" with only the name is a lookup. Naming what
		// they are known for turns it into the answer the question was really
		// after, which is what this person can help with.
		if known := salientTopics(ix, h.person.Topics, 3); len(known) > 0 {
			reasons = append(reasons, "knows "+strings.Join(known, ", "))
		}
		out = append(out, model.Match{
			Person: h.person, Team: team,
			Score: h.score, Strength: h.score, Reasons: reasons,
		})
	}
	return out
}

// nameCoversAll reports whether every word of the question matches a part of
// the name, allowing one edit on the longer words so a misspelled colleague is
// still found. Requiring every word is what keeps a question about a subject
// from matching somebody whose surname happens to look like it.
func nameCoversAll(name string, asked []string) bool {
	if name == "" {
		return false
	}
	parts := strings.Fields(name)
	for _, word := range asked {
		if !nameHasPart(parts, word) {
			return false
		}
	}
	return true
}

// nameHasPart reports whether one word of a question matches one part of a
// name, exactly or within a single edit.
func nameHasPart(parts []string, word string) bool {
	for _, part := range parts {
		if part == word || strings.HasPrefix(part, word) {
			return true
		}
		if len(word) >= nameTypoLen && len(part) >= nameTypoLen &&
			levenshtein.ComputeDistance(part, word) <= 1 {
			return true
		}
	}
	return false
}

// withNamed puts the people a question named outright ahead of the people who
// merely know about it, keeping the ranked answer behind them. A question that
// names somebody is asking about that person; a question that does not is
// unaffected, since nameMatch returns nothing for it.
func withNamed(named, ranked []model.Match, limit int) []model.Match {
	if len(named) == 0 {
		return ranked
	}
	// The people the question named first, then the ranked answer behind them.
	// A match with no person has no identity to key on and drops out.
	out := util.Distinct(append(append([]model.Match{}, named...), ranked...),
		func(m model.Match) model.ID {
			if m.Person == nil {
				return ""
			}
			return m.Person.ID
		})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// namesASubject reports whether the question is the name of a subject the graph
// holds, as opposed to a person.
func namesASubject(ix *index.Index, q string) bool {
	if ix == nil {
		return false
	}
	return ix.Graph.Topics[model.ID(q)].Salient()
}
