package index

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/text"
	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vector"
)

// Embedder turns text into a vector. The llm package's Ollama client satisfies
// it.
type Embedder interface {
	// Embed returns the embedding vector for text.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// HasEmbeddings reports whether the index holds vectors to search semantically.
func (ix *Index) HasEmbeddings() bool {
	return len(ix.personVecs) > 0 || len(ix.channelVecs) > 0
}

// Embed fills per-person and per-channel vectors by embedding a text
// representation of each entity. An error from e aborts and is returned.
func (ix *Index) Embed(ctx context.Context, e Embedder) error {
	done := 0
	pv := make(map[model.ID][]float32, len(ix.Graph.People))
	for id, p := range ix.Graph.People {
		// A person rebuilt from a saved index carries no message text, only the
		// stemmed search terms, since readable text is never written to disk.
		// Re-embedding them would produce a thinner vector than the one built
		// when their messages were in hand, so keep the existing vector rather
		// than weaken it. A full Build clears vectors first, so a fresh embed,
		// including a switch of embedding model, is never pinned to a stale one.
		if pt := ix.texts[id]; pt == nil || pt.Text == "" {
			if existing, ok := ix.personVecs[id]; ok && len(existing) > 0 {
				pv[id] = existing
				done++
				ix.embedProgress.Report(done)
				continue
			}
		}
		vec, err := e.Embed(ctx, personEmbedText(p, ix.texts[id]))
		if err != nil {
			return fmt.Errorf("index: embed person %s: %w", id, err)
		}
		pv[id] = vec
		done++
		ix.embedProgress.Report(done)
	}
	cv := make(map[model.ID][]float32, len(ix.Graph.Channels))
	for id, ch := range ix.Graph.Channels {
		vec, err := e.Embed(ctx, channelEmbedText(ch, ix.channelTexts[id]))
		if err != nil {
			return fmt.Errorf("index: embed channel %s: %w", id, err)
		}
		cv[id] = vec
		done++
		ix.embedProgress.Report(done)
	}
	vocab := ix.topicVocab()
	tv := make(map[model.ID][]float32, len(ix.Graph.Topics))
	for id, t := range ix.Graph.Topics {
		// Only subjects the organization actually has are worth a vector. A word
		// mined once out of a title, such as "issue" or "runbook", would other-
		// wise become something a question could match, and it would carry
		// whoever happens to hold it into the answer.
		if !t.Salient() {
			continue
		}
		text := ix.topicEmbedText(id, vocab)
		if text == "" {
			continue
		}
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("index: embed topic %s: %w", id, err)
		}
		tv[id] = vec
		done++
		ix.embedProgress.Report(done)
	}
	ix.personVecs = pv
	ix.channelVecs = cv
	ix.topicVecs = tv
	return nil
}

// topicVocabWords is how many distinctive words describe a subject, and
// topicVocabFloor is how often a word must appear in that subject's work before
// it can be called distinctive of it rather than a coincidence.
const (
	topicVocabWords = 6
	topicVocabFloor = 3
	// topicVocabSpreadPct is the share of subjects a word may be prominent in
	// before it counts as filler rather than description.
	topicVocabSpreadPct = 25
)

// topicVocab returns, for every salient subject, the words that distinguish it
// from the rest of the organization's talk. A subject is described by the words
// its work is done in, so a question asked in somebody else's vocabulary can
// still land on it. Selecting by raw frequency would return "the" and "deploy"
// for every subject alike, so a word counts only when it appears far more often
// in this subject's work than across everyone's, which is what makes the short
// list actually describe the thing.
func (ix *Index) topicVocab() map[model.ID][]string {
	global := make(map[string]float64)
	var globalTotal float64
	perTopic := make(map[model.ID]map[string]float64)
	totals := make(map[model.ID]float64)

	// People run in ID order: each subject's word counts and total sum one
	// affinity per person, and in map order those sums differ in their last
	// digits between runs.
	for _, id := range slices.Sorted(maps.Keys(ix.Graph.People)) {
		p := ix.Graph.People[id]
		pt := ix.texts[id]
		if pt == nil || pt.Text == "" {
			continue
		}
		words := text.Tokenize(pt.Text)
		for _, w := range words {
			global[w]++
			globalTotal++
		}
		for tid, affinity := range p.Topics {
			if affinity <= 0 {
				continue
			}
			if t := ix.Graph.Topics[tid]; t == nil || !t.Salient() {
				continue
			}
			counts := perTopic[tid]
			if counts == nil {
				counts = make(map[string]float64)
				perTopic[tid] = counts
			}
			for _, w := range words {
				counts[w] += affinity
				totals[tid] += affinity
			}
		}
	}

	// A word that turns up in most subjects describes none of them. Filler
	// phrasing is shared by everyone who writes about their own work, so it
	// clears a lift test measured against the whole company while pulling every
	// subject's vector toward the same middle. Count how many subjects each word
	// is prominent in and drop the ones that are everywhere.
	spread := make(map[string]int)
	for tid, counts := range perTopic {
		if totals[tid] <= 0 {
			continue
		}
		for w, c := range counts {
			if (c/totals[tid])/(global[w]/globalTotal) > 1 {
				spread[w]++
			}
		}
	}
	maxSubjects := max(1, len(perTopic)*topicVocabSpreadPct/100)

	out := make(map[model.ID][]string, len(perTopic))
	for tid, counts := range perTopic {
		if totals[tid] <= 0 {
			continue
		}
		type scored struct {
			word string
			lift float64
		}
		var ranked []scored
		for w, c := range counts {
			if global[w] < topicVocabFloor || spread[w] > maxSubjects {
				continue
			}
			lift := (c / totals[tid]) / (global[w] / globalTotal)
			if lift <= 1 {
				continue
			}
			ranked = append(ranked, scored{word: w, lift: lift})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].lift != ranked[j].lift {
				return ranked[i].lift > ranked[j].lift
			}
			return ranked[i].word < ranked[j].word
		})
		words := make([]string, 0, topicVocabWords)
		for i, r := range ranked {
			if i >= topicVocabWords {
				break
			}
			words = append(words, r.word)
		}
		out[tid] = words
	}
	return out
}

// topicEmbedText describes a subject as its name followed by the words that
// distinguish its work, which is what a question phrased in somebody else's
// words has to match against.
func (ix *Index) topicEmbedText(tid model.ID, vocab map[model.ID][]string) string {
	t := ix.Graph.Topics[tid]
	if t == nil {
		return ""
	}
	name := t.Name
	if name == "" {
		name = string(tid)
	}
	parts := append([]string{strings.ReplaceAll(name, "-", " ")}, vocab[tid]...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// SemanticTopics ranks subjects by similarity to the query vector. Matching a
// question to a subject and then naming that subject's people, rather than
// matching the question straight to a person, is what lets a paraphrase land:
// one person's vector averages everything they ever said, while a subject's
// describes one thing.
func (ix *Index) SemanticTopics(query []float32, limit int) []model.ID {
	// The whole field is ranked and judged before the limit cuts it. Judged on
	// the truncated list, the same query counted as discriminating at one limit
	// and flat at another, and a small limit could dress a flat field up as an
	// answer because two results are too few to judge.
	ranked := rankByCosine(ix.topicVecs, query)
	if !discriminating(ranked) {
		return nil
	}
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.ID, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.id)
	}
	return out
}

// TopicSimilarity reports how far a subject stands out from the other subjects
// for this query, on the same scale people are scored by, so a question that
// suits every subject equally reports strength in none of them.
func (ix *Index) TopicSimilarity(query []float32, tid model.ID) float64 {
	vec, ok := ix.topicVecs[tid]
	if !ok {
		return 0
	}
	ranked := rankByCosine(ix.topicVecs, query)
	median, ok := fieldMedian(ranked)
	return standout(cosine(query, vec), median, ok)
}

// TopicExperts returns the people with the strongest affinity for a topic,
// most expert first. It is the deterministic half of a semantic answer: the
// vector picks the subject, the graph picks who owns it.
func (ix *Index) TopicExperts(tid model.ID, limit int) []*model.Person {
	type scored struct {
		p *model.Person
		w float64
	}
	var out []scored
	for _, p := range ix.Graph.People {
		w := p.Topics[tid]
		if w <= 0 {
			continue
		}
		// Discounted by everything else this person does. Ranked on raw weight,
		// the few who touch all of a code base come back as the experts on
		// every part of it.
		reach := p.Reach()
		if reach <= 0 {
			continue
		}
		out = append(out, scored{p: p, w: w / math.Sqrt(reach)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].w != out[j].w {
			return out[i].w > out[j].w
		}
		return out[i].p.ID < out[j].p.ID
	})
	people := make([]*model.Person, 0, len(out))
	for i, s := range out {
		if limit > 0 && i >= limit {
			break
		}
		people = append(people, s.p)
	}
	return people
}

// SetEmbedProgress sets a callback invoked after each entity is embedded with
// the running count. Embedding makes one blocking model call per person and
// channel, so on a large graph this is the difference between visible movement
// and a frozen line.
func (ix *Index) SetEmbedProgress(p util.Progress) { ix.embedProgress = p }

// standoutScale is the cosine margin over the middle of the field that counts as
// a match standing fully on its own. Raw cosine is not a strength: an
// embedding model puts two unrelated pieces of engineering talk around 0.6 as a
// matter of course, so reporting 0.6 as "fairly sure" states a certainty nothing
// earned. What carries information is how far a match stands above the rest of
// the field for the same question, which is what this scales.
const standoutScale = 0.15

// minStandout is the strength below which a match is indistinguishable from
// the field and is dropped rather than shown. A name offered with no real
// evidence behind it is worse than no name, because it will be believed.
const minStandout = 0.05

// discriminating reports whether a ranking actually picked somebody out. The
// test is the best score against the middle of the field rather than against the
// tail, because a tail always slopes away on its own, and rather than against
// second place, because two people genuinely sharing a subject should not read
// as no answer.
//
// A flat ranking is not a weak answer to show with a low score, it is the
// absence of one. Presenting it names a person at a strength the evidence does
// not support, which is worse than saying nothing at all. A list too short to
// have a middle is left alone, since there is nothing there to mislead anyone.
func discriminating(ranked []scoredID) bool {
	if len(ranked) < minRankedToJudge {
		return true
	}
	median, ok := fieldMedian(ranked)
	return standout(ranked[0].score, median, ok) >= minStandout
}

// fieldMedian is the middle score of a ranking, the level a question places
// everything at when it distinguishes nothing. It returns false when the
// ranking is too short to have a middle worth measuring against.
func fieldMedian(ranked []scoredID) (float64, bool) {
	if len(ranked) < minRankedToJudge {
		return 0, false
	}
	return ranked[len(ranked)/2].score, true
}

// standout converts a raw similarity into a strength by measuring it against
// the middle of its own field, so a match reports how far it rose above the
// crowd rather than how large a number the model happened to return. With too
// few results to make a field, there is nothing to stand out from and the
// similarity is reported as it is.
func standout(score, median float64, haveField bool) float64 {
	if score <= 0 {
		return 0
	}
	if !haveField {
		return min(1, score)
	}
	return min(1, max(0, (score-median)/standoutScale))
}

// minRankedToJudge is how many results a ranking needs before its spread means
// anything.
const minRankedToJudge = 4

// SemanticPeople ranks people by cosine similarity to the query vector. The
// similarity doubles as the strength, clamped at zero.
func (ix *Index) SemanticPeople(query []float32, limit int) []model.Match {
	// The field statistics come from the whole field, then the limit cuts the
	// list. Measured against the truncated ranking, the same match reported a
	// different strength at limit 5 than at limit 50, and strength must be a
	// fact about the match, not about how much of the list was requested.
	ranked := rankByCosine(ix.personVecs, query)
	if !discriminating(ranked) {
		return nil
	}
	median, haveField := fieldMedian(ranked)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.Match, 0, len(ranked))
	for _, r := range ranked {
		p := ix.Graph.People[r.id]
		if p == nil {
			continue
		}
		strength := standout(r.score, median, haveField)
		if strength < minStandout {
			continue
		}
		var team *model.Team
		if p.TeamID != "" {
			team = ix.Graph.Teams[p.TeamID]
		}
		out = append(out, model.Match{
			Person:   p,
			Team:     team,
			Score:    r.score,
			Strength: strength,
			Reasons:  []string{"semantic match"},
		})
	}
	return out
}

// SemanticChannels ranks channels by cosine similarity to the query vector and
// attaches the members most similar to the query.
func (ix *Index) SemanticChannels(query []float32, limit int) []model.ChannelMatch {
	// Field statistics before the limit, as in SemanticPeople, and strength as
	// standout rather than raw cosine: an embedding model puts two unrelated
	// pieces of engineering talk around 0.6 as a matter of course, so the raw
	// number states a certainty nothing earned.
	ranked := rankByCosine(ix.channelVecs, query)
	if !discriminating(ranked) {
		return nil
	}
	median, haveField := fieldMedian(ranked)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	memberScores := cosineScores(ix.personVecs, query)
	out := make([]model.ChannelMatch, 0, len(ranked))
	for _, r := range ranked {
		ch := ix.Graph.Channels[r.id]
		if ch == nil {
			continue
		}
		strength := standout(r.score, median, haveField)
		if strength < minStandout {
			continue
		}
		out = append(out, model.ChannelMatch{
			Channel:    ch,
			Score:      r.score,
			Strength:   strength,
			Reasons:    []string{"semantic match"},
			TopMembers: ix.topMembers(ch, memberScores, 3),
		})
	}
	return out
}

// scoredID pairs an entity id with its similarity score.
type scoredID struct {
	// id is the entity id.
	id model.ID
	// score is the cosine similarity.
	score float64
}

// rankByCosine scores every vector against query and returns the
// positively scoring entities, best first.
func rankByCosine(vecs map[model.ID][]float32, query []float32) []scoredID {
	ranked := make([]scoredID, 0, len(vecs))
	for id, vec := range vecs {
		// Anything at or below zero is unrelated, not merely a weak match.
		// Keeping those would fill the answer with whoever sorts first, and a
		// query embedded by a different model than the index scores every
		// entity zero, so without this the top result is a stranger.
		if score := cosine(query, vec); score > 0 {
			ranked = append(ranked, scoredID{id: id, score: score})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	return ranked
}

// cosineScores returns the cosine similarity of each vector to query.
func cosineScores(vecs map[model.ID][]float32, query []float32) map[model.ID]float64 {
	out := make(map[model.ID]float64, len(vecs))
	for id, vec := range vecs {
		out[id] = cosine(query, vec)
	}
	return out
}

// cosine returns the cosine similarity of a and b.
func cosine(a, b []float32) float64 { return vector.Cosine(a, b) }

// quantizeVecs compresses each embedding vector to int8 for compact storage.
func quantizeVecs(vecs map[model.ID][]float32) map[model.ID][]int8 {
	if len(vecs) == 0 {
		return nil
	}
	out := make(map[model.ID][]int8, len(vecs))
	for id, v := range vecs {
		out[id] = vector.Quantize(v)
	}
	return out
}

// dequantizeVecs restores int8 vectors to float32 for scoring. Cosine is scale
// invariant, so the restored direction ranks the same as the original.
func dequantizeVecs(vecs map[model.ID][]int8) map[model.ID][]float32 {
	out := make(map[model.ID][]float32, len(vecs))
	for id, q := range vecs {
		out[id] = vector.Dequantize(q)
	}
	return out
}

// personEmbedText is the text representation of a person used for embedding.
func personEmbedText(p *model.Person, pt *personText) string {
	parts := []string{p.Name, p.Title}
	if pt != nil {
		parts = append(parts, pt.Teams...)
		parts = append(parts, pt.Topics...)
		parts = append(parts, pt.Text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// channelEmbedText is the text representation of a channel used for embedding.
func channelEmbedText(ch *model.Channel, ct *channelText) string {
	parts := []string{ch.Name, ch.Topic}
	if ct != nil {
		parts = append(parts, ct.Topics...)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
