package episode

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/text"
	"github.com/dcadolph/peepwho/internal/vector"
)

// DefaultHalfLife is the age at which an episode's score halves. Recall
// reaches back months, so it decays more slowly than a question about who
// works on something now.
const DefaultHalfLife = 365 * 24 * time.Hour

// saturation bounds how much repetition one term can contribute to a single
// episode, so a word said thirty times in a long thread does not outrank the
// thread that is actually about it.
const saturation = 3.0

// Store holds episodes and the inverted index over their text. It lives in its
// own file beside the main index, so a question about who knows something
// never pays to load conversation history.
type Store struct {
	// episodes maps an episode ID to the episode.
	episodes map[string]*Episode
	// postings maps a term to per-episode accumulated weight.
	postings map[string]map[string]float64
	// byParticipant maps a person to the episodes they took part in. It is
	// derived on load rather than stored.
	byParticipant map[model.ID][]string
	// termsOf maps an episode to the terms it put in the postings, so removing
	// one visits only its own terms. Without it every removal walks the whole
	// vocabulary, which makes re-indexing cost the episode count times the
	// vocabulary size. It is derived rather than stored, and a missing entry
	// simply falls back to the full walk.
	termsOf map[string][]string
	// vecs holds per-episode embedding vectors when present.
	vecs map[string][]float32
	// halfLife is the age at which a score halves; zero uses the default.
	halfLife time.Duration
	// now returns the current time; tests pin it for deterministic decay.
	now func() time.Time
}

// New returns an empty store.
func New() *Store {
	return &Store{
		episodes:      make(map[string]*Episode),
		postings:      make(map[string]map[string]float64),
		byParticipant: make(map[model.ID][]string),
		vecs:          make(map[string][]float32),
		now:           time.Now,
	}
}

// Len reports how many episodes the store holds.
func (s *Store) Len() int { return len(s.episodes) }

// Episode returns the episode with the given ID.
func (s *Store) Episode(id string) (*Episode, bool) {
	ep, ok := s.episodes[id]
	return ep, ok
}

// All returns every episode, ordered newest first. Callers must not mutate the
// returned episodes.
func (s *Store) All() []*Episode {
	out := make([]*Episode, 0, len(s.episodes))
	for _, ep := range s.episodes {
		out = append(out, ep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Occurred.Equal(out[j].Occurred) {
			return out[i].ID < out[j].ID
		}
		return out[i].Occurred.After(out[j].Occurred)
	})
	return out
}

// SetHalfLife sets the age at which a score halves. Zero restores the default;
// a negative duration turns decay off, so an old conversation ranks purely on
// how well it matches.
func (s *Store) SetHalfLife(d time.Duration) { s.halfLife = d }

// Add stores an episode and indexes its body. Adding an episode that is
// already present replaces it, so re-indexing a source is idempotent. The body
// is tokenized and dropped; only the resulting terms are kept.
func (s *Store) Add(ep Episode) {
	if ep.ID == "" {
		return
	}
	// Credentials pasted into a conversation must never reach the store: not
	// the search terms, and above all not the retained archive notes, which
	// are quoted back verbatim.
	ep.Body, _ = text.Scrub(ep.Body)
	if len(ep.Archive) > 0 {
		notes := make([]Note, len(ep.Archive))
		for i, n := range ep.Archive {
			n.Text, _ = text.Scrub(n.Text)
			notes[i] = n
		}
		ep.Archive = notes
	}
	var vec []float32
	if old, ok := s.episodes[ep.ID]; ok {
		// Re-indexing must never quietly throw away what only the older run
		// holds. A run without the archive reports the same conversation with
		// no words attached, and a run without embeddings reports it with no
		// vector; taking either at face value would delete work the source may
		// no longer serve. Only `archive prune` deletes.
		if len(ep.Archive) == 0 && len(old.Archive) > 0 {
			ep.Archive = old.Archive
			// Keeping the words but not the people who wrote them would leave
			// the retained conversation unreachable by the person who solved
			// it. Replies are only read while archiving, so a later plain run
			// reports just the participants visible without them.
			ep.Participants = unionParticipants(ep.Participants, old.Participants)
		}
		if ep.Asked == "" && old.Asked != "" {
			// A source that stopped reporting who opened the conversation has not
			// established that nobody did, and dropping the asker would silently
			// turn a measured ask into an unstructured one.
			ep.Asked = old.Asked
		}
		vec = s.vecs[ep.ID]
		s.forget(old)
	}
	body := ep.Body
	stored := ep
	stored.Body = ""
	s.episodes[ep.ID] = &stored
	for _, p := range stored.Participants {
		s.byParticipant[p] = append(s.byParticipant[p], stored.ID)
	}
	if len(vec) > 0 {
		s.vecs[stored.ID] = vec
	}
	indexed := strings.TrimSpace(body + " " + stored.Text() + " " + archiveText(stored.Archive))
	var mine []string
	for _, term := range text.Terms(indexed) {
		posting := s.postings[term]
		if posting == nil {
			posting = make(map[string]float64)
			s.postings[term] = posting
			mine = append(mine, term)
		} else if _, already := posting[stored.ID]; !already {
			mine = append(mine, term)
		}
		posting[stored.ID]++
	}
	if len(mine) > 0 {
		if s.termsOf == nil {
			s.termsOf = make(map[string][]string)
		}
		s.termsOf[stored.ID] = mine
	}
}

// archiveText joins retained messages so a kept conversation is findable by
// what was actually said in it. Without this a conversation is searchable only
// by its opening message, which states the problem rather than the answer, and
// the words a later run no longer fetches would drop out of the index while
// still sitting on disk.
func archiveText(notes []Note) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for _, n := range notes {
		b.WriteString(n.Text)
		b.WriteByte(' ')
	}
	return b.String()
}

// unionParticipants returns everyone in either list, keeping the order of the
// first and appending anyone the second adds.
func unionParticipants(keep, add []model.ID) []model.ID {
	seen := make(map[model.ID]bool, len(keep)+len(add))
	out := make([]model.ID, 0, len(keep)+len(add))
	for _, list := range [][]model.ID{keep, add} {
		for _, p := range list {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// SetVector attaches an embedding vector to an episode.
func (s *Store) SetVector(id string, vec []float32) {
	if _, ok := s.episodes[id]; ok && len(vec) > 0 {
		s.vecs[id] = vec
	}
}

// Vector returns the embedding vector for an episode.
func (s *Store) Vector(id string) ([]float32, bool) {
	v, ok := s.vecs[id]
	return v, ok
}

// forget removes an episode's postings and participant links, so replacing it
// cannot leave stale terms pointing at it.
func (s *Store) forget(ep *Episode) {
	s.unpost(ep.ID)
	for _, p := range ep.Participants {
		ids := s.byParticipant[p]
		kept := ids[:0]
		for _, id := range ids {
			if id != ep.ID {
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 {
			delete(s.byParticipant, p)
			continue
		}
		s.byParticipant[p] = kept
	}
	delete(s.vecs, ep.ID)
	delete(s.episodes, ep.ID)
}

// dropSearchIndex removes an episode's keyword postings and embedding vector, so
// its text can no longer be found by search. Unlike forget it keeps the episode
// record and its participant links, so the pointer survives while the purged
// content becomes unsearchable.
func (s *Store) dropSearchIndex(id string) {
	s.unpost(id)
	delete(s.vecs, id)
}

// rebuildTerms derives, from the postings, which terms each episode
// contributed. A store restored from disk has the postings but not that map,
// and one pass here is what keeps removals cheap for the rest of the run.
func (s *Store) rebuildTerms() {
	s.termsOf = make(map[string][]string, len(s.episodes))
	for term, posting := range s.postings {
		for id := range posting {
			s.termsOf[id] = append(s.termsOf[id], term)
		}
	}
}

// unpost removes one episode from the postings. It visits only the terms that
// episode contributed when its terms are known, and falls back to walking the
// whole vocabulary when they are not, which is what a store restored from disk
// starts out as until its terms are rebuilt.
func (s *Store) unpost(id string) {
	terms, known := s.termsOf[id]
	if !known {
		for term, posting := range s.postings {
			delete(posting, id)
			if len(posting) == 0 {
				delete(s.postings, term)
			}
		}
		return
	}
	for _, term := range terms {
		posting := s.postings[term]
		if posting == nil {
			continue
		}
		delete(posting, id)
		if len(posting) == 0 {
			delete(s.postings, term)
		}
	}
	delete(s.termsOf, id)
}

// Query asks for episodes matching text, optionally narrowed to one person.
type Query struct {
	// Text is the question, matched against episode terms.
	Text string
	// Person narrows results to episodes that person took part in. Recall for
	// an individual always sets it; an archive search across the organization
	// leaves it empty.
	Person model.ID
	// Limit caps results; zero means five.
	Limit int
}

// Result is one ranked episode with the terms that matched it.
type Result struct {
	// Episode is the matched episode.
	Episode *Episode
	// Score ranks the result.
	Score float64
	// Strength is the share of query terms this episode matched, from zero
	// to one.
	Strength float64
	// Matched lists the query terms found in the episode.
	Matched []string
}

// recent returns a person's episodes, newest first, up to limit. It backs
// topic-less recall: "show me the conversations I took part in."
func (s *Store) recent(person model.ID, limit int) []Result {
	ids := s.byParticipant[person]
	if len(ids) == 0 {
		return nil
	}
	out := make([]Result, 0, len(ids))
	for _, id := range ids {
		ep := s.episodes[id]
		if ep == nil {
			continue
		}
		out = append(out, Result{Episode: ep, Score: s.decay(ep.Occurred)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Episode.Occurred.Equal(out[j].Episode.Occurred) {
			return out[i].Episode.ID < out[j].Episode.ID
		}
		return out[i].Episode.Occurred.After(out[j].Episode.Occurred)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Search ranks episodes for a query. Scoring is term overlap, saturated so
// repetition cannot dominate, scaled by how much of the question an episode
// covers and by how recently it happened.
func (s *Store) Search(q Query) []Result {
	terms := text.Terms(q.Text)
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	if len(terms) == 0 {
		// A truly empty topic falls back to the person's most recent
		// conversations. A query that was all stopwords matched nothing
		// meaningful, so it returns nothing rather than everything. Without a
		// person there is no scope either.
		if q.Person != "" && strings.TrimSpace(q.Text) == "" {
			return s.recent(q.Person, limit)
		}
		return nil
	}
	var scope map[string]bool
	if q.Person != "" {
		ids := s.byParticipant[q.Person]
		if len(ids) == 0 {
			return nil
		}
		scope = make(map[string]bool, len(ids))
		for _, id := range ids {
			scope[id] = true
		}
	}

	type acc struct {
		score   float64
		matched []string
	}
	hits := make(map[string]*acc)
	seen := make(map[string]bool, len(terms))
	unique := 0
	for _, term := range terms {
		if seen[term] {
			continue
		}
		seen[term] = true
		unique++
		for id, weight := range s.postings[term] {
			if scope != nil && !scope[id] {
				continue
			}
			a := hits[id]
			if a == nil {
				a = &acc{}
				hits[id] = a
			}
			a.score += weight / (weight + saturation)
			a.matched = append(a.matched, term)
		}
	}

	out := make([]Result, 0, len(hits))
	for id, a := range hits {
		ep := s.episodes[id]
		if ep == nil {
			continue
		}
		coverage := float64(len(a.matched)) / float64(unique)
		sort.Strings(a.matched)
		out = append(out, Result{
			Episode:  ep,
			Score:    a.score * coverage * s.decay(ep.Occurred),
			Strength: coverage,
			Matched:  a.matched,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Episode.ID < out[j].Episode.ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SearchSemantic ranks episodes by how close their meaning is to a query
// vector, which finds a conversation whose words you no longer remember. It
// returns nothing when no episode was embedded.
func (s *Store) SearchSemantic(query []float32, q Query) []Result {
	if len(query) == 0 || len(s.vecs) == 0 {
		return nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	var scope map[string]bool
	if q.Person != "" {
		ids := s.byParticipant[q.Person]
		if len(ids) == 0 {
			return nil
		}
		scope = make(map[string]bool, len(ids))
		for _, id := range ids {
			scope[id] = true
		}
	}
	out := make([]Result, 0, len(s.vecs))
	for id, vec := range s.vecs {
		if scope != nil && !scope[id] {
			continue
		}
		ep := s.episodes[id]
		if ep == nil {
			continue
		}
		similarity := vector.Cosine(query, vec)
		if similarity <= 0 {
			continue
		}
		out = append(out, Result{
			Episode:  ep,
			Score:    similarity * s.decay(ep.Occurred),
			Strength: similarity,
			Matched:  []string{"similar meaning"},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Episode.ID < out[j].Episode.ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// HasVectors reports whether any episode was embedded, which is what makes
// semantic recall possible.
func (s *Store) HasVectors() bool { return len(s.vecs) > 0 }

// decay returns the recency multiplier for an episode, halving every
// half-life. Undated episodes never decay.
func (s *Store) decay(at time.Time) float64 {
	half := s.halfLife
	if half == 0 {
		half = DefaultHalfLife
	}
	if half <= 0 || at.IsZero() {
		return 1
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	age := now().Sub(at)
	if age <= 0 {
		return 1
	}
	return math.Pow(0.5, age.Seconds()/half.Seconds())
}

// ForgetPerson removes one person from every conversation, under every
// identifier they are known by: their participant links, their retained notes,
// and any episode they were alone in. The whole identity set is taken in one
// pass because participants are stored canonically while note authors can
// still carry a source-native identifier; judged one identifier at a time, an
// episode half-purged by an earlier pass can read as a one-person conversation
// and take a bystander's notes down with it. An episode is dropped only when
// nobody outside the set appears in it, as a participant or as a note author.
// An edited episode loses the purged person's notes and its searchable words
// where they cannot be separated out, and keeps the pointer so the
// conversation can still be found through its place and people. It reports how
// many episodes were dropped outright and how many were edited.
func (s *Store) ForgetPerson(ids []model.ID) (dropped, edited int) {
	purged := make(map[model.ID]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			purged[id] = true
		}
	}
	if len(purged) == 0 {
		return 0, 0
	}
	for _, ep := range s.All() {
		involved, othersInvolved := false, false
		for _, p := range ep.Participants {
			if purged[p] {
				involved = true
			} else {
				othersInvolved = true
			}
		}
		if purged[ep.Asked] {
			// A source can name an asker the participant list never carries, and
			// judged on participants alone that episode would be passed over with
			// the purged person still named on it.
			involved = true
		}
		hasNotes, othersNotes := false, false
		for _, n := range ep.Archive {
			if purged[n.Author] {
				hasNotes = true
			} else {
				othersNotes = true
			}
		}
		if !involved && !hasNotes {
			continue
		}
		if !othersInvolved && !othersNotes {
			s.forget(ep)
			dropped++
			continue
		}
		updated := *ep
		kept := make([]model.ID, 0, len(ep.Participants))
		for _, p := range ep.Participants {
			if !purged[p] {
				kept = append(kept, p)
			}
		}
		updated.Participants = kept
		if purged[updated.Asked] {
			// A purged person left as the asker would still be named by every
			// answer that quotes who opened the conversation.
			updated.Asked = ""
		}
		notes := make([]Note, 0, len(ep.Archive))
		for _, n := range ep.Archive {
			if !purged[n.Author] {
				notes = append(notes, n)
			}
		}
		updated.Archive = notes
		s.forget(ep)
		// Re-adding rebuilds the search terms from what remains; the original
		// body is gone, which is the point.
		s.Add(updated)
		edited++
	}
	return dropped, edited
}
