// Package resolve answers queries against an index through swappable resolvers.
// The keyword resolver needs no LLM. The LLM resolver retrieves candidates with
// the keyword index, then asks a model to rank them. The written recommendation
// is composed locally from the top-ranked candidates, so it can never name a
// person or channel the model invented.
package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// Answer is a resolved response: ranked people, ranked channels, and an
// optional written summary that the LLM resolver fills in.
type Answer struct {
	// People is the ranked list of people to talk to.
	People []model.Match
	// Channels is the ranked list of places to ask.
	Channels []model.ChannelMatch
	// Summary is a short written recommendation; empty in keyword mode.
	Summary string
	// ix is the index this answer came from, carried so the view can tell a
	// real subject from a word mined out of prose when it lists what somebody
	// knows. It is nil on an answer built without one, and the view falls back
	// to the unfiltered list.
	ix *index.Index
}

// Resolver answers a natural-language query.
type Resolver interface {
	// Resolve ranks up to limit people and channels for query.
	Resolve(ctx context.Context, query string, limit int) (Answer, error)
}

// ResolverFunc adapts a function to the Resolver interface.
type ResolverFunc func(ctx context.Context, query string, limit int) (Answer, error)

// Resolve calls f.
func (f ResolverFunc) Resolve(ctx context.Context, query string, limit int) (Answer, error) {
	return f(ctx, query, limit)
}

// Keyword is a non-LLM resolver that ranks by keyword and affinity scoring over
// the index. It needs no external services.
type Keyword struct {
	// ix is the index to search.
	ix *index.Index
}

// NewKeyword returns a Keyword resolver over ix. It panics if ix is nil.
func NewKeyword(ix *index.Index) *Keyword {
	if ix == nil {
		panic("resolve: NewKeyword requires a non-nil index")
	}
	return &Keyword{ix: ix}
}

// Resolve ranks people and channels for query using the index.
func (k *Keyword) Resolve(_ context.Context, query string, limit int) (Answer, error) {
	return Answer{
		People:   withNamed(nameMatch(k.ix, query, limit), k.ix.Search(query, limit), limit),
		Channels: k.ix.SearchChannels(query, limit),
		ix:       k.ix,
	}, nil
}

// Chatter sends a system and user prompt to a model and returns its reply. The
// llm package's Ollama client satisfies it.
type Chatter interface {
	// Chat returns the model's reply to the system and user messages.
	Chat(ctx context.Context, system, user string) (string, error)
}

// Embedder turns text into a vector for semantic retrieval. The llm package's
// Ollama client satisfies it.
type Embedder interface {
	// Embed returns the embedding vector for text.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// llmSystem instructs the model to rank only provided candidates and reply as
// JSON, keeping the answer grounded in real data.
const llmSystem = `You help an employee find who to talk to and which channel to ask in.
You are given a question and a list of candidate people and channels retrieved from an
internal index. Pick and rank only the most relevant candidates. Never invent a person,
email, or channel that is not in the candidate list. Reply only as JSON of the form:
{"people":["<email or name from candidates, best first>"],
"channels":["<channel name from candidates, best first>"]}.
If no candidate is relevant, return empty arrays.`

// redactedSystem is the system prompt for the redacted path: candidates are
// numbered and carry no identifiers, people are roles, channels are only the
// query terms they matched, and the model replies with candidate numbers.
const redactedSystem = `You help an employee find who to talk to and which channel to ask in.
You are given a question and a numbered list of candidates retrieved from an internal index.
Candidates are anonymized: rank people only by their role, team, and matched terms, and rank
channels only by which question terms they matched and how.
Reply only as JSON of the form:
{"people":["<candidate number, best first>"],"channels":["<channel number, best first>"]}.
If no candidate is relevant, return empty arrays.`

// LLM is a resolver that retrieves candidates with the keyword index, then asks
// a model to rank them. It stays grounded: model output is matched back to
// retrieved candidates, anything invented is dropped, and the written summary is
// composed locally from the top result so the model never authors a name. In
// redacted mode, built for cloud models under the redacted policy, candidates
// leave the machine as numbered roles with no names or emails and the model
// returns numbers.
type LLM struct {
	// ix retrieves candidates.
	ix *index.Index
	// chat is the model client.
	chat Chatter
	// embedder retrieves candidates semantically when set and the index has
	// embeddings; nil falls back to keyword retrieval.
	embedder Embedder
	// redact strips personal identifiers from everything sent to the model.
	redact bool
}

// NewLLM returns an LLM resolver. The embedder may be nil, in which case
// candidates are retrieved by keyword. It panics if ix or chat is nil.
func NewLLM(ix *index.Index, chat Chatter, embedder Embedder) *LLM {
	if ix == nil || chat == nil {
		panic("resolve: NewLLM requires a non-nil index and chat client")
	}
	return &LLM{ix: ix, chat: chat, embedder: embedder}
}

// NewRedactedLLM returns an LLM resolver that never sends names, emails,
// channel names, channel topics, or message text to the model: people go out
// as numbered roles, channels as numbered matched-term entries, and the
// written summary is composed locally. The question itself still goes to the
// model verbatim. Use it with cloud providers under the redacted policy.
func NewRedactedLLM(ix *index.Index, chat Chatter, embedder Embedder) *LLM {
	l := NewLLM(ix, chat, embedder)
	l.redact = true
	return l
}

// llmResult is the JSON the model is asked to return. Any summary the model
// includes is ignored: the recommendation is written locally from the grounded
// top result.
type llmResult struct {
	// People is the ranked list of person emails, names, or candidate numbers.
	People []rankToken `json:"people"`
	// Channels is the ranked list of channel names or candidate numbers.
	Channels []rankToken `json:"channels"`
}

// rankToken is one entry in a model's ranked list. The redacted prompt asks for
// a candidate number, but models variously return the bare integer, the number
// as a string, or the name, so it decodes from a JSON string or number alike.
type rankToken string

// UnmarshalJSON accepts a JSON string or number and stores its string form.
// Anything else decodes to the empty token, which matches no candidate and is
// dropped downstream, so one odd entry never fails the whole ranking.
func (t *rankToken) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = rankToken(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*t = rankToken(n.String())
		return nil
	}
	*t = ""
	return nil
}

// rankStrings returns the ranked tokens as a plain string slice for reordering.
func rankStrings(tokens []rankToken) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = string(t)
	}
	return out
}

// extractJSON returns the JSON object embedded in a model reply that may wrap it
// in a markdown code fence or surround it with prose: the substring from the
// first opening brace to the last closing brace, or the trimmed reply when it
// holds no braces.
func extractJSON(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start >= 0 && end > start {
		return raw[start : end+1]
	}
	return strings.TrimSpace(raw)
}

// Resolve retrieves candidates, asks the model to rank them, and returns the
// grounded result with a locally written summary. If the model reply cannot be
// parsed, it keeps keyword order. An explicit empty ranking is honored as the
// model abstaining, not overridden with the full candidate list.
func (l *LLM) Resolve(ctx context.Context, query string, limit int) (Answer, error) {
	n := candidateN(limit)
	people, channels := l.retrieve(ctx, query, n)
	if len(people) == 0 && len(channels) == 0 {
		return Answer{}, nil
	}

	system, prompt := llmSystem, buildPrompt(query, people, channels)
	if l.redact {
		system, prompt = redactedSystem, buildRedactedPrompt(query, people, channels)
	}
	raw, err := l.chat.Chat(ctx, system, prompt)
	if err != nil {
		return Answer{}, fmt.Errorf("llm resolve: %w", err)
	}

	var out llmResult
	if jsonErr := json.Unmarshal([]byte(extractJSON(raw)), &out); jsonErr != nil {
		// The reply was not usable JSON: keep keyword order rather than trusting
		// unparsed prose, and write the summary from those grounded results.
		return groundedAnswer(Cap(people, limit), Cap(channels, limit)), nil
	}
	return groundedAnswer(
		rankedPeople(people, out.People, limit),
		rankedChannels(channels, out.Channels, limit),
	), nil
}

// groundedAnswer assembles an Answer and writes its recommendation locally from
// the top-ranked results, so the summary can never name a non-candidate.
func groundedAnswer(people []model.Match, channels []model.ChannelMatch) Answer {
	return Answer{
		People:   people,
		Channels: channels,
		Summary:  localSummary(people, channels),
	}
}

// rankedPeople applies the model's ordering to the candidates. A nil ranking,
// meaning the field was absent, keeps keyword order; an explicit empty ranking
// is the model abstaining and yields no people.
func rankedPeople(cands []model.Match, order []rankToken, limit int) []model.Match {
	if order != nil && len(order) == 0 {
		return nil
	}
	return Cap(reorderPeople(cands, rankStrings(order)), limit)
}

// rankedChannels applies the model's ordering to the channel candidates, with
// the same abstention rule as rankedPeople.
func rankedChannels(cands []model.ChannelMatch, order []rankToken, limit int) []model.ChannelMatch {
	if order != nil && len(order) == 0 {
		return nil
	}
	return Cap(reorderChannels(cands, rankStrings(order)), limit)
}

// retrieve gets candidate people and channels, semantically when an embedder is
// configured and the index has vectors, otherwise by keyword. A failed query
// embedding falls back to keyword retrieval.
func (l *LLM) retrieve(ctx context.Context, query string, n int) ([]model.Match, []model.ChannelMatch) {
	if l.embedder != nil && l.ix.HasEmbeddings() {
		if vec, err := l.embedder.Embed(ctx, query); err == nil {
			return l.ix.SemanticPeople(vec, n), l.ix.SemanticChannels(vec, n)
		}
	}
	return l.ix.Search(query, n), l.ix.SearchChannels(query, n)
}

// candidateN returns how many candidates to retrieve for the model, a small
// multiple of the requested limit, bounded so prompts stay short.
func candidateN(limit int) int {
	n := limit * 2
	if limit <= 0 || n > 25 {
		n = 25
	}
	if n < 8 {
		n = 8
	}
	return n
}

// buildPrompt renders the question and candidates as the model's user message.
func buildPrompt(query string, people []model.Match, channels []model.ChannelMatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nCandidate people:\n", query)
	if len(people) == 0 {
		b.WriteString("(none)\n")
	}
	for i, m := range people {
		team := ""
		if m.Team != nil {
			team = m.Team.Name
		}
		fmt.Fprintf(&b, "%d. %s <%s> - %s; team %s; matched %s\n",
			i+1, m.Person.Name, m.Person.Email, m.Person.Title, team, strings.Join(m.Reasons, ", "))
	}
	b.WriteString("\nCandidate channels:\n")
	if len(channels) == 0 {
		b.WriteString("(none)\n")
	}
	for i, c := range channels {
		var members []string
		for _, p := range c.TopMembers {
			members = append(members, p.Name)
		}
		fmt.Fprintf(&b, "%d. #%s - topic: %s; active: %s\n",
			i+1, c.Channel.Name, c.Channel.Topic, strings.Join(members, ", "))
	}
	return b.String()
}

// buildRedactedPrompt renders the question and candidates without identifiers
// from the index: each person is a numbered role with title, team, and matched
// terms; each channel is a number with only the query terms it matched, never
// its name, topic, or members. The matched terms come from the user's own
// question, so nothing indexed leaves beyond titles and team names.
func buildRedactedPrompt(query string, people []model.Match, channels []model.ChannelMatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nCandidate people:\n", query)
	if len(people) == 0 {
		b.WriteString("(none)\n")
	}
	for i, m := range people {
		team := ""
		if m.Team != nil {
			team = m.Team.Name
		}
		fmt.Fprintf(&b, "%d. %s; team %s; matched %s\n",
			i+1, m.Person.Title, team, strings.Join(m.Reasons, ", "))
	}
	b.WriteString("\nCandidate channels:\n")
	if len(channels) == 0 {
		b.WriteString("(none)\n")
	}
	for i, c := range channels {
		matched := strings.Join(c.Reasons, ", ")
		if matched == "" {
			matched = "(no detail)"
		}
		fmt.Fprintf(&b, "%d. matched %s\n", i+1, matched)
	}
	return b.String()
}

// localSummary writes a one-line recommendation from the top-ranked results,
// so redacted mode never needs the model to see or produce a name.
func localSummary(people []model.Match, channels []model.ChannelMatch) string {
	var parts []string
	if len(people) > 0 {
		p := people[0].Person
		who := p.Name
		if who == "" {
			who = p.Email
		}
		if p.Title != "" {
			who += " (" + p.Title + ")"
		}
		parts = append(parts, "Talk to "+who)
	}
	if len(channels) > 0 {
		parts = append(parts, "ask in #"+channels[0].Channel.Name)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

// reorderPeople ranks candidates by the model's order, matching on email,
// name, or one-based candidate number, then appends any candidates the model
// omitted. Unknown entries from the model are ignored, keeping the result
// grounded.
func reorderPeople(cands []model.Match, order []string) []model.Match {
	// A display name shared by two candidates identifies neither. Indexed by
	// name alone the second would quietly overwrite the first, so a reordering
	// that said "Kim" would move whichever of the two happened to be stored
	// last. Ambiguous names are left out entirely: the position and the email
	// still name a person exactly, and a reference that resolves to nobody is
	// better than one that resolves to the wrong body.
	nameCount := make(map[string]int, len(cands))
	for _, m := range cands {
		if m.Person.Name != "" {
			nameCount[strings.ToLower(m.Person.Name)]++
		}
	}
	byKey := make(map[string]model.Match, len(cands)*3)
	for i, m := range cands {
		byKey[strconv.Itoa(i+1)] = m
		if m.Person.Email != "" {
			byKey[strings.ToLower(m.Person.Email)] = m
		}
		if name := strings.ToLower(m.Person.Name); name != "" && nameCount[name] == 1 {
			byKey[name] = m
		}
	}
	// What the model asked for first, then everything it did not mention, with
	// the first mention of each person winning.
	ordered := make([]model.Match, 0, len(order)+len(cands))
	for _, key := range order {
		if m, ok := byKey[strings.ToLower(strings.TrimSpace(key))]; ok {
			ordered = append(ordered, m)
		}
	}
	ordered = append(ordered, cands...)
	return util.Distinct(ordered, func(m model.Match) model.ID { return m.Person.ID })
}

// reorderChannels ranks channel candidates by the model's order, matching on
// name or one-based candidate number, then appends any the model omitted.
// Unknown names are ignored.
func reorderChannels(cands []model.ChannelMatch, order []string) []model.ChannelMatch {
	byName := make(map[string]model.ChannelMatch, len(cands)*2)
	for i, c := range cands {
		byName[strconv.Itoa(i+1)] = c
		byName[strings.ToLower(c.Channel.Name)] = c
	}
	ordered := make([]model.ChannelMatch, 0, len(order)+len(cands))
	for _, key := range order {
		key = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "#")))
		if c, ok := byName[key]; ok {
			ordered = append(ordered, c)
		}
	}
	ordered = append(ordered, cands...)
	return util.Distinct(ordered, func(c model.ChannelMatch) model.ID { return c.Channel.ID })
}

// Cap returns at most limit items; a non-positive limit returns all. It is
// exported because the command layer caps the same result lists for display.
func Cap[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// Semantic is a resolver that ranks purely by embedding similarity. It needs an
// embedder and an index built with embeddings; it uses no chat model.
type Semantic struct {
	// ix holds the entity vectors.
	ix *index.Index
	// embedder embeds the query.
	embedder Embedder
}

// NewSemantic returns a Semantic resolver. It panics if ix or embedder is nil.
func NewSemantic(ix *index.Index, embedder Embedder) *Semantic {
	if ix == nil || embedder == nil {
		panic("resolve: NewSemantic requires a non-nil index and embedder")
	}
	return &Semantic{ix: ix, embedder: embedder}
}

// Resolve ranks people and channels by blending meaning with words. The two
// signals fail differently: word matching wins when the asker remembers any
// term that was actually used and scores zero when they remember none, while
// vector similarity survives a full paraphrase but is blurrier on exact hits.
// Fusing the two rankings answers both askers without anyone picking a mode.
func (s *Semantic) Resolve(ctx context.Context, query string, limit int) (Answer, error) {
	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return Answer{}, fmt.Errorf("semantic resolve: %w", err)
	}
	// Fusing over a deeper pool than the caller asked for lets an answer that
	// both signals agree on climb into the visible window. A non-positive
	// limit means every match, so the pool is unbounded too, matching keyword.
	pool := 0
	if limit > 0 {
		pool = max(limit*3, 15)
	}
	people := withNamed(nameMatch(s.ix, query, limit),
		fusePeople(s.ix.Search(query, pool),
			s.ix.SemanticPeople(vec, pool), s.topicPeople(vec, pool), limit), limit)
	channels := fuseChannels(s.ix.SearchChannels(query, pool), s.ix.SemanticChannels(vec, pool), limit)
	return Answer{People: people, Channels: channels, ix: s.ix}, nil
}

// topicSubjects is how many subjects a question is matched against, and
// topicSubjectExperts how many people each of them contributes.
const (
	topicSubjects       = 3
	topicSubjectExperts = 4
)

// topicPeople answers by way of the subject rather than the person: it matches
// the question to the subjects the organization works on, then names the people
// the graph already says own them. A person's vector is the average of
// everything they ever said, so a question in somebody else's words lands
// nowhere near it; a subject's vector describes one thing, and who owns that
// subject is a fact the index computed without any model at all.
func (s *Semantic) topicPeople(vec []float32, limit int) []model.Match {
	var out []model.Match
	seen := make(map[model.ID]bool)
	for _, tid := range s.ix.SemanticTopics(vec, topicSubjects) {
		sim := s.ix.TopicSimilarity(vec, tid)
		name := strings.ReplaceAll(string(tid), "-", " ")
		for _, p := range s.ix.TopicExperts(tid, topicSubjectExperts) {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, model.Match{
				Person:   p,
				Score:    sim,
				Strength: sim,
				Reasons:  []string{"works on " + name},
			})
			if limit > 0 && len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// rrfK dampens rank differences in reciprocal rank fusion. The standard value
// keeps a first place from utterly dominating and lets agreement across both
// lists beat a single strong showing in one.
const rrfK = 60

// fusePeople merges the word ranking and the meaning ranking of people by
// reciprocal rank fusion, keeping each person's best reasons and strength
// from whichever list explained them better.
func fusePeople(words, meaning, subjects []model.Match, limit int) []model.Match {
	type fused struct {
		match model.Match
		score float64
	}
	byID := make(map[model.ID]*fused)
	fold := func(list []model.Match) {
		for rank, m := range list {
			if m.Person == nil {
				continue
			}
			f := byID[m.Person.ID]
			if f == nil {
				f = &fused{match: m}
				byID[m.Person.ID] = f
			}
			f.score += 1.0 / float64(rrfK+rank+1)
			// The word ranking explains itself in matched terms while the
			// meaning ranking only says "semantic match", so richer reasons
			// and the higher strength win the display.
			if len(m.Reasons) > len(f.match.Reasons) {
				f.match.Reasons = m.Reasons
			}
			if m.Strength > f.match.Strength {
				f.match.Strength = m.Strength
			}
		}
	}
	fold(words)
	fold(meaning)
	fold(subjects)

	out := make([]model.Match, 0, len(byID))
	scores := make(map[model.ID]float64, len(byID))
	for id, f := range byID {
		scores[id] = f.score
		f.match.Score = f.score
		out = append(out, f.match)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := scores[out[i].Person.ID], scores[out[j].Person.ID]
		if si != sj {
			return si > sj
		}
		return out[i].Person.ID < out[j].Person.ID
	})
	// A non-positive limit means every match, the same as the keyword path, so
	// the two modes agree on what --limit 0 does.
	return Cap(out, limit)
}

// fuseChannels merges the two channel rankings the same way.
func fuseChannels(words, meaning []model.ChannelMatch, limit int) []model.ChannelMatch {
	type fused struct {
		match model.ChannelMatch
		score float64
	}
	byID := make(map[model.ID]*fused)
	fold := func(list []model.ChannelMatch) {
		for rank, m := range list {
			if m.Channel == nil {
				continue
			}
			f := byID[m.Channel.ID]
			if f == nil {
				f = &fused{match: m}
				byID[m.Channel.ID] = f
			}
			f.score += 1.0 / float64(rrfK+rank+1)
			if len(m.Reasons) > len(f.match.Reasons) {
				f.match.Reasons = m.Reasons
			}
			if m.Strength > f.match.Strength {
				f.match.Strength = m.Strength
			}
		}
	}
	fold(words)
	fold(meaning)

	out := make([]model.ChannelMatch, 0, len(byID))
	scores := make(map[model.ID]float64, len(byID))
	for id, f := range byID {
		scores[id] = f.score
		f.match.Score = f.score
		out = append(out, f.match)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := scores[out[i].Channel.ID], scores[out[j].Channel.ID]
		if si != sj {
			return si > sj
		}
		return out[i].Channel.ID < out[j].Channel.ID
	})
	return Cap(out, limit)
}
