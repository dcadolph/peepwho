// Searching the index: query expansion, term resolution, scoring, ranking,
// and the reasons every answer carries.

package index

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/agnivade/levenshtein"

	"github.com/dcadolph/whotoask/internal/model"
)

// evidenceBoost turns the kind of evidence behind a match into a score
// multiplier. Accumulated weight measures how often the words appear, which a
// prolific poster can supply endlessly; the kind of field they appear in is
// what repetition cannot buy. A passing mention stays where its weight put it,
// and a declared topic, title, or team lifts the score, so the person who owns
// a subject beats the person who merely talks about it by construction rather
// than by accident of the tuning constants.
func evidenceBoost(evidence float64) float64 {
	return 0.4 + 0.9*evidence
}

// EvidenceWindow bounds how many leading candidates are re-ranked by evidence,
// so an unlimited search does not pay the reason lookup for every match. A
// search cut anywhere inside the window returns a prefix of the same order.
const EvidenceWindow = 64

// expandedQuery grows the tokenized query with its synonyms and returns the
// full term list, the per-term coverage back to the words actually asked, and
// the asked expression behind each expansion for the reason line. Every
// original term covers itself; an expansion covers the words that triggered it,
// so a hit through a synonym still counts toward how much of the question was
// answered instead of diluting it.
func expandedQuery(query string) (terms []string, covers map[string][]string, asked map[string]string) {
	ordered := tokenize(query)
	terms = distinct(ordered)
	if len(terms) == 0 {
		return nil, nil, nil
	}
	covers = make(map[string][]string, len(terms))
	asked = make(map[string]string)
	for _, t := range terms {
		covers[t] = []string{t}
	}
	for _, e := range expandTerms(query) {
		if _, dup := covers[e.term]; dup {
			continue
		}
		// An expansion may cover a word the tokenizer dropped, such as the "in"
		// of "sign in". Coverage is measured over the searched terms, so only
		// those count; a phrase whose every word was dropped still covers
		// something by standing in for the whole question.
		kept := make([]string, 0, len(e.covers))
		for _, orig := range e.covers {
			if _, real := covers[orig]; real {
				kept = append(kept, orig)
			}
		}
		terms = append(terms, e.term)
		covers[e.term] = kept
		asked[e.term] = e.asked
	}
	return terms, covers, asked
}

// applyExpansionPenalty discounts the resolved expansions, so a synonym never
// outranks the words the person actually used.
func applyExpansionPenalty(resolved map[string]termHit, asked map[string]string) {
	for term, from := range asked {
		hit, ok := resolved[term]
		if !ok {
			continue
		}
		if strings.HasPrefix(from, tieAskedPrefix) {
			// A tie-derived term is a different subject that travels with the
			// one asked about, not another name for it, so it is discounted
			// below every synonym and far below every typed word.
			// mapsum: hit is this iteration's own copy of a struct value.
			hit.penalty *= tieExpansionPenalty
		} else {
			// mapsum: hit is this iteration's own copy of a struct value.
			hit.penalty *= expansionPenalty
		}
		resolved[term] = hit
	}
}

// splitTieTerms separates tie-derived expansions from the words asked and
// their synonyms, so the two can score in separate passes.
func splitTieTerms(terms []string, asked map[string]string) (base, ties []string) {
	for _, t := range terms {
		if strings.HasPrefix(asked[t], tieAskedPrefix) {
			ties = append(ties, t)
			continue
		}
		base = append(base, t)
	}
	return base, ties
}

// mergeReach adds tie-pass results for entities the base pass never found.
//
// Expansion is reach, not re-ranking: a candidate the question already found
// keeps exactly the score the question gave them, and the tie pass may only
// introduce people who would otherwise be invisible. Letting ties top up
// existing candidates was measured handing four question shapes to a weakly
// matching neighbor of the right answer; making expansion purely additive
// cannot move anyone the question found, by construction.
func mergeReach(
	scores map[model.ID]float64, matched map[model.ID]map[string]bool,
	tieScores map[model.ID]float64, tieMatched map[model.ID]map[string]bool,
) {
	for id, sc := range tieScores {
		if _, found := scores[id]; found {
			continue
		}
		scores[id] = sc
		matched[id] = tieMatched[id]
	}
}

// typedTerms returns the terms the person typed, leaving out every term the
// query grew by a synonym or a tie. Coverage is counted over these, and a
// feedback vote fires only on these, so a vote on one question never reaches
// another through a word nobody asked.
func typedTerms(terms []string, asked map[string]string) []string {
	typed := make([]string, 0, len(terms))
	for _, t := range terms {
		if _, isExp := asked[t]; !isExp {
			typed = append(typed, t)
		}
	}
	return typed
}

// matchedOriginal reports whether the candidate matched any term the person
// actually typed, as opposed to only terms the query grew by expansion. The
// initial ranking places every expansion-only candidate after every candidate
// the question itself found; within the evidence window, matchedBeyondTies
// then holds the part of that promise that was measured, because penalty
// tuning alone lost four question shapes to neighbors of the right answer.
func matchedOriginal(matched map[string]bool, asked map[string]string) bool {
	for term := range matched {
		if _, isExp := asked[term]; !isExp {
			return true
		}
	}
	return false
}

// matchedBeyondTies reports whether the candidate matched the question or a
// synonym of it, as opposed to only subjects that travel with it. The line
// sits here because the two kinds of reach are different claims: a synonym is
// another name for what was asked and competes on its evidence, while a
// tie-derived subject was never asked about at all, and letting tie reach
// outrank people the question found was measured losing four question shapes
// to weakly matching neighbors of the right answer (p@1 .90 to .72).
func matchedBeyondTies(matched map[string]bool, asked map[string]string) bool {
	for term := range matched {
		if !strings.HasPrefix(asked[term], tieAskedPrefix) {
			return true
		}
	}
	return false
}

// coveredShare is how much of the asked question a match answered: the share of
// original terms covered by anything it matched, synonyms included.
func coveredShare(matched map[string]bool, covers map[string][]string, originals int) float64 {
	if originals == 0 {
		return 0
	}
	hit := make(map[string]bool, originals)
	for term := range matched {
		for _, orig := range covers[term] {
			hit[orig] = true
		}
	}
	return float64(len(hit)) / float64(originals)
}

// Search ranks people for query and returns up to limit matches. A non-positive
// limit returns all matches. Teams are not people and never appear here: the
// ranking reads only the people view, so scores, order, and strength are what
// an index without the teams would give. SearchTeams answers for them.
// Deactivated people never appear here either. SearchSplit returns them.
func (ix *Index) Search(query string, limit int) []model.Match {
	people, _ := ix.SearchSplit(query, limit)
	return people
}

// SearchSplit ranks people for query and returns the active and the
// deactivated apart, each cut to limit. The split happens before the cut, so
// active people fill the limit whatever a departed person scored, and a
// departed person who knew the subject is still returned, ranked among the
// departed, rather than silently dropped.
func (ix *Index) SearchSplit(query string, limit int) (people, departed []model.Match) {
	terms, covers, asked := expandedQuery(query)
	if len(terms) == 0 {
		return nil, nil
	}
	terms = append(terms, ix.tieExpand(tokenize(query), covers, asked)...)
	typed := typedTerms(terms, asked)
	originals := len(typed)
	resolved := resolveTerms(ix.peoplePostings, ix.personVocab, terms)
	applyExpansionPenalty(resolved, asked)
	baseTerms, tieTerms := splitTieTerms(terms, asked)
	scores, matched := scoreByTerms(ix.peoplePostings, baseTerms, resolved, ix.peopleN, ix.personLens)
	if len(tieTerms) > 0 {
		tieScores, tieMatched := scoreByTerms(ix.peoplePostings, tieTerms, resolved, ix.peopleN, ix.personLens)
		mergeReach(scores, matched, tieScores, tieMatched)
	}
	nets := ix.feedbackNets(typed, false)

	matches := make([]model.Match, 0, len(scores))
	for pid, sc := range scores {
		p := ix.Graph.People[pid]
		if p == nil {
			continue
		}
		var team *model.Team
		if p.TeamID != "" {
			team = ix.Graph.Teams[p.TeamID]
		}
		if net := nets[pid]; net != 0 {
			sc *= ix.feedbackFactor(net)
		}
		matches = append(matches, model.Match{Person: p, Team: team, Score: sc})
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
	// Re-rank the leading candidates by the kind of evidence behind them, so
	// declared ownership outranks accumulated chatter no matter how the raw
	// weights land. Only the window that could plausibly hold the answer is
	// examined, since judging evidence means reading each candidate's fields.
	window := min(len(matches), EvidenceWindow)
	// explained is one window candidate's reasons and the evidence behind them.
	type explained struct {
		// reasons are the reason lines for the candidate's matched terms.
		reasons []string
		// evidence is the strongest field any matched term hit.
		evidence float64
		// shown is that evidence with each corrected word marked down.
		shown float64
	}
	// Judging the window reads every reason, so they are kept for the people
	// who survive the cut, and explaining a deep search costs no more than this.
	seen := make(map[model.ID]explained, window)
	for i := range window {
		pid := matches[i].Person.ID
		reasons, evidence, shown := ix.reasons(pid, matched[pid], resolved, asked)
		seen[pid] = explained{reasons: reasons, evidence: evidence, shown: shown}
		matches[i].Score *= evidenceBoost(evidence)
	}
	// The boost never lifts tie reach above anyone the question or its synonyms
	// found. A re-sort on score alone quietly repealed that for every query
	// small enough to fit the window, which is the measured regression the tie
	// discount exists to prevent. Synonym matchers still compete on their
	// evidence: pinning them below every original matcher here was tried and
	// cost the everyday-language benchmark its published p@1, because the right
	// person often holds the subject under its other name.
	sort.SliceStable(matches[:window], func(i, j int) bool {
		oi := matchedBeyondTies(matched[matches[i].Person.ID], asked)
		oj := matchedBeyondTies(matched[matches[j].Person.ID], asked)
		if oi != oj {
			return oi
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Person.ID < matches[j].Person.ID
	})
	people, departed = SplitDeparted(matches)
	if limit > 0 {
		people = people[:min(len(people), limit)]
		departed = departed[:min(len(departed), limit)]
	}
	// Build the reasons and strength only for the people that survived the
	// limit. Each reason stems the person's whole topic set against every query
	// term, so doing it for every candidate before ranking was the query's cost,
	// and only the returned people need it. Strength does not affect the
	// ranking, which is by score, so deferring it changes no result.
	explain := func(list []model.Match) {
		for i := range list {
			pid := list[i].Person.ID
			e, ok := seen[pid]
			if !ok {
				e.reasons, e.evidence, e.shown = ix.reasons(pid, matched[pid], resolved, asked)
			}
			reasons := e.reasons
			if net := nets[pid]; net != 0 {
				reasons = append(reasons, feedbackReason(net))
			}
			list[i].Reasons = reasons
			list[i].Strength = e.shown * coveredShare(matched[pid], covers, originals)
			list[i].Declared = e.evidence > evidenceTeam
			list[i].Reach = !matchedBeyondTies(matched[pid], asked)
		}
	}
	explain(people)
	explain(departed)
	return people, departed
}

// SplitDeparted separates matches into the people who can still be reached and
// the deactivated, keeping the order of each. Matches without a person count as
// active, so nothing is lost from either list.
func SplitDeparted(matches []model.Match) (people, departed []model.Match) {
	people = make([]model.Match, 0, len(matches))
	for _, m := range matches {
		if m.Person != nil && m.Person.Deactivated() {
			departed = append(departed, m)
		} else {
			people = append(people, m)
		}
	}
	return people, departed
}

// SearchChannels ranks channels for query and returns up to limit matches, each
// carrying the most relevant active members. A non-positive limit returns all.
func (ix *Index) SearchChannels(query string, limit int) []model.ChannelMatch {
	terms, covers, asked := expandedQuery(query)
	if len(terms) == 0 {
		return nil
	}
	terms = append(terms, ix.tieExpand(tokenize(query), covers, asked)...)
	typed := typedTerms(terms, asked)
	originals := len(typed)
	resolved := resolveTerms(ix.channelPostings, ix.channelVocab, terms)
	applyExpansionPenalty(resolved, asked)
	baseTerms, tieTerms := splitTieTerms(terms, asked)
	scores, matched := scoreByTerms(
		ix.channelPostings, baseTerms, resolved, len(ix.Graph.Channels), ix.channelLens)
	if len(tieTerms) > 0 {
		tieScores, tieMatched := scoreByTerms(
			ix.channelPostings, tieTerms, resolved, len(ix.Graph.Channels), ix.channelLens)
		mergeReach(scores, matched, tieScores, tieMatched)
	}
	nets := ix.feedbackNets(typed, true)

	matches := make([]model.ChannelMatch, 0, len(scores))
	for cid, sc := range scores {
		ch := ix.Graph.Channels[cid]
		if ch == nil {
			continue
		}
		reasons, evidence, shown := ix.channelReasons(cid, matched[cid], resolved, asked)
		if net := nets[cid]; net != 0 {
			sc *= ix.feedbackFactor(net)
			reasons = append(reasons, feedbackReason(net))
		}
		coverage := coveredShare(matched[cid], covers, originals)
		matches = append(matches, model.ChannelMatch{
			Channel:  ch,
			Score:    sc,
			Strength: shown * coverage,
			Declared: evidence > evidenceTeam,
			Reach:    !matchedBeyondTies(matched[cid], asked),
			Reasons:  reasons,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		oi := matchedOriginal(matched[matches[i].Channel.ID], asked)
		oj := matchedOriginal(matched[matches[j].Channel.ID], asked)
		if oi != oj {
			return oi
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Channel.ID < matches[j].Channel.ID
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	// Rank the members only for the channels that survived the limit. Scoring
	// every person and sorting every channel's whole membership up front, only to
	// keep a handful of channels, was the bulk of a query at scale.
	if len(matches) > 0 {
		personResolved := resolveTerms(ix.peoplePostings, ix.personVocab, terms)
		personScores, _ := scoreByTerms(
			ix.peoplePostings, terms, personResolved, ix.peopleN, ix.personLens)
		for i := range matches {
			matches[i].TopMembers = ix.topMembers(matches[i].Channel, personScores, 3)
		}
	}
	return matches
}

// topMembers returns up to n of a channel's members, most relevant to the query
// first, using precomputed person scores. It selects the top n in a single pass
// with one score lookup per member, rather than sorting the whole membership
// with a lookup inside the comparator, which was the dominant cost of a query at
// scale. Only people worth sending the asker to are listed, so a channel names
// fewer than n members rather than a departed or an unrelated one.
func (ix *Index) topMembers(ch *model.Channel, scores map[model.ID]float64, n int) []*model.Person {
	if n <= 0 {
		return nil
	}
	type scored struct {
		person *model.Person
		score  float64
	}
	best := make([]scored, 0, n+1)
	for _, id := range ch.Members {
		p := ix.Graph.People[id]
		score := scores[id]
		// A team handle is nobody to ask, a deactivated account cannot answer,
		// and a member with no score has nothing to do with the question.
		if p == nil || id.IsTeam() || p.Deactivated() || score <= 0 {
			continue
		}
		cand := scored{person: p, score: score}
		pos := len(best)
		for pos > 0 && best[pos-1].score < cand.score {
			pos--
		}
		if pos >= n {
			continue
		}
		best = append(best, scored{})
		copy(best[pos+1:], best[pos:])
		best[pos] = cand
		if len(best) > n {
			best = best[:n]
		}
	}
	out := make([]*model.Person, len(best))
	for i, b := range best {
		out[i] = b.person
	}
	return out
}

// BM25-style ranking parameters. k1 governs how quickly repeated weight for
// one term saturates, b governs how strongly an entity whose accumulated
// profile is far larger than average is discounted, and termWeightCap bounds
// one term's weight before saturation so unbounded repetition in free text
// cannot outrank an explicit topic tag.
const (
	bm25K1        = 1.2
	bm25B         = 0.75
	termWeightCap = 4.0
	// normCap bounds the verbosity discount. A term's weight is capped, so
	// letting the length normalizer grow unchecked eventually divides an
	// explicit topic tag below a single passing mention, which would mean the
	// more work someone does, the worse they rank on what they own.
	//
	// The bound is low on purpose, and the number is measured rather than
	// chosen: against a real repository's own CODEOWNERS, accuracy rises all
	// the way down from 4.5 to here and falls off again below it, because the
	// people who own the most are also the people who appear the most, and
	// discounting them for it hands their subjects to whoever passed through
	// once. Removing the discount entirely is worse again: then whoever appears
	// most often wins everything. See the ranking section of docs/REFERENCE.md before changing it.
	normCap = 1.75
)

// Fuzzy matching bounds. Terms shorter than fuzzyMinLen never fuzz, one edit
// is allowed from fuzzyMinLen, two from fuzzyTwoEditLen, and each edit
// multiplies the term's contribution by fuzzyPenalty so an exact match always
// outranks a corrected one. Two edits are held to longer words: two edits in a
// short word is a large distortion that turns a real miss such as "postgis"
// into a confident wrong match on "postgres", so the second edit is allowed
// only once a word is long enough that two edits stay a small share of it.
const (
	fuzzyMinLen     = 4
	fuzzyTwoEditLen = 9
	fuzzyPenalty    = 0.7
)

// termHit is the resolution of one query term against a posting vocabulary:
// the key to score with and the penalty its edits cost.
type termHit struct {
	// key is the posting key the term scores against.
	key string
	// penalty scales the term's contribution; one for an exact match.
	penalty float64
	// correction is what a corrected word keeps of its strength: fuzzyPenalty
	// once, whatever the number of edits, and one for an exact match. Two edits
	// are allowed only on a long word, where they are as small a share of it as
	// one edit is of a short word, and a swapped pair of letters counts as two.
	// A synonym or tie discount never enters it.
	correction float64
}

// fuzzy reports whether the resolution corrected the term.
func (h termHit) fuzzy() bool { return h.penalty < 1 }

// vocabIndex groups posting keys by byte length so a fuzzy lookup scans only
// the keys whose length is within the edit-distance band of the query term,
// rather than the entire vocabulary.
type vocabIndex struct {
	// byLen maps a key's byte length to the keys of that length.
	byLen map[int][]string
}

// newVocabIndex buckets a posting vocabulary by key length.
func newVocabIndex(postings map[string]map[model.ID]float64) vocabIndex {
	byLen := make(map[int][]string)
	for k := range postings {
		byLen[len(k)] = append(byLen[len(k)], k)
	}
	return vocabIndex{byLen: byLen}
}

// resolveTerms maps each query term to its posting key, fuzzily when the
// exact stem has no posting. Terms that resolve to nothing are absent.
func resolveTerms(
	postings map[string]map[model.ID]float64, vocab vocabIndex, terms []string,
) map[string]termHit {
	out := make(map[string]termHit, len(terms))
	for _, term := range terms {
		if hit, ok := resolveTerm(postings, vocab, term); ok {
			out[term] = hit
		}
	}
	return out
}

// resolveTerm returns the posting key for one term: its own stem when a
// posting exists, otherwise the closest vocabulary key within the allowed
// edit distance. Only keys whose length is within that distance of the stem
// can match, so the search is confined to those length buckets.
//
// Equally close corrections break to the one the organization actually uses,
// measured by how many people hold it. A real code base contains its own
// misspellings, and they sit exactly as close to a typo as the correct word
// does: asked about "blutooth", whotoask answered with a directory misspelled
// "blueooth" rather than bluetooth, purely because it sorted first.
func resolveTerm(
	postings map[string]map[model.ID]float64, vocab vocabIndex, term string,
) (termHit, bool) {
	key := stem(term)
	if len(postings[key]) > 0 {
		return termHit{key: key, penalty: 1, correction: 1}, true
	}
	runes := len([]rune(term))
	if runes < fuzzyMinLen {
		return termHit{}, false
	}
	maxDist := 1
	if runes >= fuzzyTwoEditLen {
		maxDist = 2
	}
	best, bestDist, bestHeld := "", maxDist+1, -1
	for l := len(key) - maxDist; l <= len(key)+maxDist; l++ {
		for _, cand := range vocab.byLen[l] {
			d := levenshtein.ComputeDistance(key, cand)
			if d > bestDist {
				continue
			}
			held := len(postings[cand])
			if d < bestDist || held > bestHeld || (held == bestHeld && cand < best) {
				best, bestDist, bestHeld = cand, d, held
			}
		}
	}
	if best == "" || bestDist > maxDist {
		return termHit{}, false
	}
	return termHit{
		key: best, penalty: math.Pow(fuzzyPenalty, float64(bestDist)), correction: fuzzyPenalty,
	}, true
}

// entityLens holds the accumulated posting mass per entity and its average,
// the document-length inputs to BM25 length normalization.
type entityLens struct {
	// byID is the summed posting weight per entity across all tokens.
	byID map[model.ID]float64
	// avg is the mean of byID, at least one so normalization never divides
	// by zero.
	avg float64
}

// lengthsOf sums posting weight per entity across all tokens, in sorted order
// at both levels. Float addition is not associative, so a map-order walk put
// different last bits in the length table on different loads of one index,
// and those bits reach every score through the verbosity normalizer; the same
// input must answer with the same bytes across invocations, not merely within
// one process.
func lengthsOf(postings map[string]map[model.ID]float64) entityLens {
	byID := make(map[model.ID]float64, len(postings))
	total := 0.0
	for _, term := range slices.Sorted(maps.Keys(postings)) {
		posting := postings[term]
		for _, id := range slices.Sorted(maps.Keys(posting)) {
			w := posting[id]
			byID[id] += w
			total += w
		}
	}
	avg := 1.0
	if len(byID) > 0 {
		avg = total / float64(len(byID))
	}
	if avg <= 0 {
		avg = 1
	}
	return entityLens{byID: byID, avg: avg}
}

// scoreByTerms accumulates per-entity scores over the resolved terms. Each
// term is weighted by inverse document frequency so rarer terms count for
// more, its accumulated weight is capped and saturated so a person who
// repeats a word endlessly cannot outrank the explicit owner, entities with
// far more accumulated text than average are discounted, and a fuzzily
// corrected term contributes at its resolution penalty. It returns the
// scores and, per entity, the set of terms that matched.
func scoreByTerms(
	postings map[string]map[model.ID]float64,
	terms []string,
	resolved map[string]termHit,
	universe int,
	lens entityLens,
) (map[model.ID]float64, map[model.ID]map[string]bool) {
	scores := make(map[model.ID]float64)
	matched := make(map[model.ID]map[string]bool)
	scored := make(map[string]bool)
	for _, term := range terms {
		hit, ok := resolved[term]
		if !ok {
			continue
		}
		posting := postings[hit.key]
		if len(posting) == 0 {
			continue
		}
		// Record the term's coverage for every entity it hit, even if another
		// term already scored this key, so coverage counts distinct terms.
		for id := range posting {
			if matched[id] == nil {
				matched[id] = make(map[string]bool)
			}
			matched[id][term] = true
		}
		// Score each resolved key once: two query terms that stem to the same
		// key describe one piece of evidence, not two.
		if scored[hit.key] {
			continue
		}
		scored[hit.key] = true
		idf := 1.0
		if universe > 0 {
			idf = 1 + math.Log(float64(universe)/float64(len(posting)))
		}
		for id, w := range posting {
			// Weight saturates past the cap logarithmically instead of being cut
			// off. A hard cut made every strong profile identical up there, and
			// the verbosity normalizer below then handed the win to whoever had
			// the thinnest profile overall: the person with one line in a
			// CODEOWNERS file outranked the owner with years of work. Log growth
			// keeps the guarantee both directions: more real evidence never
			// scores less, and no amount of repetition can run away with it.
			if w > termWeightCap {
				w = termWeightCap * (1 + math.Log(w/termWeightCap))
			}
			// The normalizer floors at one: an above-average profile is
			// discounted for verbosity, but a sparse or decayed profile gets
			// no boost, since its raw weight already says how little is there.
			// It STOPS at normCap rather than growing past it. Letting it grow
			// logarithmically was tried, on the argument that a hard stop
			// treats somebody who touched a hundred subjects the same as
			// somebody who touched ten thousand; it measured worse and was
			// reverted, because the people who own the most also appear the
			// most. The stop is what keeps breadth from dividing an owner's
			// evidence away entirely.
			norm := min(max(1, 1-bm25B+bm25B*(lens.byID[id]/lens.avg)), normCap)
			// mapsum: one add per id for each term, and terms walk in slice order.
			scores[id] += hit.penalty * idf * (w * (bm25K1 + 1)) / (w + bm25K1*norm)
		}
	}
	return scores, matched
}

// appendUnique appends s to list when it is not already present, keeping the
// accumulated field values free of duplicates.
func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// distinct returns terms with duplicates removed, preserving first-seen order,
// so a repeated query token neither double-scores nor deflates coverage.
func distinct(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := terms[:0]
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// reasons describes, for each matched term, which field of the person it hit,
// and returns the strongest evidence among those hits twice over: evidence as
// it ranks and classes the match, and shown as strength reports it, with each
// corrected word marked down, so a guess at a typo never reads as proof. A
// fuzzily corrected term classifies by its resolved stem and says so.
func (ix *Index) reasons(
	pid model.ID, terms map[string]bool, resolved map[string]termHit, asked map[string]string,
) (out []string, evidence, shown float64) {
	pt := ix.texts[pid]
	p := ix.Graph.People[pid]
	out = make([]string, 0, len(terms))
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
		case pt != nil && take(stemMatch(hit.key, pt.Titles...)):
			field, strength = "title", evidenceTitle
		case pt != nil && take(stemMatch(hit.key, pt.Teams...)):
			field, strength = "team", evidenceTeam
		}
		// mapsum: strength is a constant picked in this iteration, and max is exact in any order.
		evidence = max(evidence, strength)
		// mapsum: the product is this iteration's own, and max is exact in any order.
		shown = max(shown, strength*hit.correction)
		// A synonym arrives with a deliberate discount, which must not read as a
		// typo correction: the "for" clause already says why the word differs
		// from the question.
		if from := asked[term]; from != "" && from != term {
			if origin, tied := strings.CutPrefix(from, tieAskedPrefix); tied {
				// The one reason a synonym table could never produce: these
				// two subjects move together in this organization's work.
				out = append(out, fmt.Sprintf("%s (%s), travels with %s here", term, field, origin))
				continue
			}
			out = append(out, fmt.Sprintf("%s (%s) for %q", term, field, from))
			continue
		}
		// Knowing a subject best is not the same as still being in it. On a
		// real repository the leading expert of two subjects in five had
		// stopped touching them, and was less than half as likely to still
		// hold the subject six months on, so sending somebody to ask them
		// without saying so is how an answer goes stale.
		if field == "topic" && quietOn(p, found) {
			field += ", not lately"
		}
		// A correction has to name what it corrected to. Told only that
		// "zigby" was fuzzy, a reader cannot tell whether whotoask read it as
		// zigbee or as zigzag, which is the difference between a lucky save
		// and a wrong guess presented with strength.
		if as := readAs(term, found, hit); as != "" {
			out = append(out, fmt.Sprintf("%s (%s, read for %q)", as, field, term))
			continue
		}
		if hit.fuzzy() {
			field += ", fuzzy"
		}
		out = append(out, fmt.Sprintf("%s (%s)", term, field))
	}
	sort.Strings(out)
	return out, evidence, shown
}

// readAs returns the word a corrected term was read as, or the empty string
// when the term was not corrected or was read as itself. A topic, title, team,
// or name supplies the word it holds. A mention has no field to quote, so it
// is named by the stem the term resolved to: "spark" answered by people who
// wrote about parking has to say park, or the reason claims they wrote about
// Spark.
func readAs(term, found string, hit termHit) string {
	if !hit.fuzzy() {
		return ""
	}
	if found == "" {
		found = hit.key
	}
	if found == term {
		return ""
	}
	return found
}

// quietOn reports whether somebody knows a subject but has stopped working on
// it. An empty recent record means the source never said what was recent, so
// nothing is claimed either way.
func quietOn(p *model.Person, topic string) bool {
	if p == nil || len(p.Recent) == 0 || topic == "" {
		return false
	}
	tid := topicID(topic)
	return p.Topics[tid] > 0 && p.Recent[tid] == 0
}

// channelReasons describes, for each matched term, which field of the channel
// it hit, and returns the strongest evidence among those hits the way reasons
// does: evidence as it classes the match, and shown as strength reports it,
// with each corrected word marked down. A fuzzily corrected term classifies by
// its resolved stem and says so.
func (ix *Index) channelReasons(
	cid model.ID, terms map[string]bool, resolved map[string]termHit, asked map[string]string,
) (out []string, evidence, shown float64) {
	ct := ix.channelTexts[cid]
	out = make([]string, 0, len(terms))
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
		case ct != nil && take(stemMatch(hit.key, ct.Topics...)):
			field, strength = "topic", evidenceTopic
		case ct != nil && take(stemMatch(hit.key, ct.Topic)):
			field, strength = "topic", evidenceTopic
		case ct != nil && take(stemMatch(hit.key, ct.Name)):
			field, strength = "name", evidenceTopic
		}
		// mapsum: strength is a constant picked in this iteration, and max is exact in any order.
		evidence = max(evidence, strength)
		// mapsum: the product is this iteration's own, and max is exact in any order.
		shown = max(shown, strength*hit.correction)
		out = append(out, hitReason(term, field, found, hit, asked))
	}
	sort.Strings(out)
	return out, evidence, shown
}

// hitReason writes the reason line for one matched term of a channel or team:
// the term and the field it hit, the subject it travels with when it came from
// a tie, what it stood in for when it was a synonym, and what it was read as
// when it was corrected.
func hitReason(term, field, found string, hit termHit, asked map[string]string) string {
	from := asked[term]
	// A tied subject is a different thing that moves with the one asked, so it
	// reads the way it does for a person and never as a synonym of the question.
	if origin, tied := strings.CutPrefix(from, tieAskedPrefix); tied {
		return fmt.Sprintf("%s (%s), travels with %s here", term, field, origin)
	}
	if from != "" && from != term {
		return fmt.Sprintf("%s (%s) for %q", term, field, from)
	}
	// A correction has to name what it corrected to. Told only that "zigby"
	// was fuzzy, a reader cannot tell whether whotoask read it as zigbee or as
	// zigzag, which is the difference between a lucky save and a wrong guess
	// presented with strength.
	if as := readAs(term, found, hit); as != "" {
		return fmt.Sprintf("%s (%s, read for %q)", as, field, term)
	}
	if hit.fuzzy() {
		field += ", fuzzy"
	}
	return fmt.Sprintf("%s (%s)", term, field)
}
