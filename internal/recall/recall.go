// Package recall answers the question peepwho's index cannot: not who knows
// about something, but when you worked through it before and who was with you.
// An answer is a pointer, never a transcript: the people, the place, the date,
// and a link back to the conversation in the tool it happened in.
package recall

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// Resolver answers recall questions from an episode store, naming people from
// the graph the main index holds.
type Resolver struct {
	// store holds the episodes.
	store *episode.Store
	// ix names people and resolves the identifiers sources use.
	ix *index.Index
	// horizon is how far back links are trusted. Past it an answer says the
	// link may no longer resolve, because the workspace may have aged the
	// message out. Zero means no claim is made.
	horizon time.Duration
	// embedder turns a question into a vector for semantic recall; nil keeps
	// matching to the words themselves.
	embedder Embedder
	// summarizer writes how a kept conversation resolved; nil shows the
	// conversation as written.
	summarizer Summarizer
}

// Embedder turns a question into a vector. Semantic recall needs one, and it
// only helps against episodes embedded when they were indexed.
type Embedder interface {
	// Embed returns the embedding vector for text.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// New returns a Resolver over store, naming people from ix. It panics on a nil
// store or index, which is a wiring mistake rather than a runtime condition.
func New(store *episode.Store, ix *index.Index) *Resolver {
	if store == nil || ix == nil {
		panic("recall: New requires a store and an index")
	}
	// Identities join as more sources are indexed, so a conversation recorded
	// under a handle last month belongs to a person known by email today.
	// Re-resolving here keeps old records findable without rewriting them.
	ix.CanonicalizeStore(store)
	return &Resolver{store: store, ix: ix}
}

// SetHorizon sets how far back links are trusted before an answer warns that
// the source may have deleted the message.
func (r *Resolver) SetHorizon(d time.Duration) { r.horizon = d }

// SetEmbedder enables semantic recall, which matches a question by meaning
// rather than by the words used at the time.
func (r *Resolver) SetEmbedder(e Embedder) { r.embedder = e }

// Semantic reports whether semantic recall can run: an embedder is configured
// and the episodes were embedded when they were indexed.
func (r *Resolver) Semantic() bool { return r.embedder != nil && r.store.HasVectors() }

// Who resolves an identifier a person is known by, such as an email or a Slack
// user ID, to their canonical identity. It returns an empty ID when the hint
// is empty.
func (r *Resolver) Who(hint string) model.ID {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	return r.ix.Canonical(model.ID(hint))
}

// Known reports whether any indexed conversation includes a person, which
// separates "you were not in any of these" from "nothing has been indexed
// yet". An empty answer means different things in those two cases.
func (r *Resolver) Known(person model.ID) bool { return r.store.HasPerson(person) }

// Len reports how many conversations are held.
func (r *Resolver) Len() int { return r.store.Len() }

// Query asks what a person worked through before.
type Query struct {
	// Text is the question.
	Text string
	// Person scopes the answer to conversations this person took part in.
	// Personal recall always sets it.
	Person model.ID
	// Limit caps episodes returned; zero means five.
	Limit int
	// Meaning matches by meaning instead of by words, which finds a
	// conversation whose exact wording is long forgotten. It needs an embedder
	// and episodes that were embedded when indexed.
	Meaning bool
	// Explain includes how each problem was worked out, for conversations
	// whose content peepwho keeps.
	Explain bool
}

// Answer is a recall result, shaped for JSON so the CLI, the web app, and the
// bot all render the same thing.
type Answer struct {
	// Query echoes the question asked.
	Query string `json:"query"`
	// Person is who the answer is about.
	Person string `json:"person,omitempty"`
	// Episodes are the matching conversations, best first.
	Episodes []Episode `json:"episodes"`
	// Scope states what was searched, so an empty answer is not mistaken for
	// an absence of history.
	Scope Scope `json:"scope"`
}

// Episode is one remembered conversation.
type Episode struct {
	// People are the others who took part, which is the answer to "who helped
	// me".
	People []Person `json:"people"`
	// Place is where it happened, such as a channel name.
	Place string `json:"place,omitempty"`
	// ID is the conversation's stable identifier, so a caller can dedup or refer
	// back to the exact conversation rather than only its link.
	ID string `json:"id,omitempty"`
	// Source names the tool it happened in.
	Source string `json:"source"`
	// Kind is the conversation shape: a thread, a stretch of channel talk, or
	// a resolved ticket.
	Kind string `json:"kind"`
	// When is when it last saw activity.
	When time.Time `json:"when"`
	// Messages counts the messages behind it.
	Messages int `json:"messages,omitempty"`
	// Permalink opens the conversation in its own tool, where the reader's own
	// access still applies.
	Permalink string `json:"permalink,omitempty"`
	// LinkMayHaveExpired warns that the conversation is older than the
	// workspace is known to keep, so the link may no longer resolve.
	LinkMayHaveExpired bool `json:"link_may_have_expired,omitempty"`
	// Matched lists the query terms found in the conversation.
	Matched []string `json:"matched,omitempty"`
	// Strength is how much of the question this conversation covered, from
	// zero to one.
	Strength float64 `json:"strength"`
	// Solution is how the problem was worked out, present only for
	// conversations whose content peepwho keeps.
	Solution *Solution `json:"solution,omitempty"`
}

// Person names someone who took part.
type Person struct {
	// Name is their display name.
	Name string `json:"name,omitempty"`
	// Email is their work email.
	Email string `json:"email,omitempty"`
	// Title is their job title.
	Title string `json:"title,omitempty"`
	// ID is the canonical identifier, present when no name is known.
	ID string `json:"id,omitempty"`
}

// Scope states what a recall answer covered, so a miss can be read correctly.
type Scope struct {
	// Sources are the tools episodes came from.
	Sources []string `json:"sources"`
	// Episodes is how many conversations were searched.
	Episodes int `json:"episodes"`
	// Oldest is the earliest conversation held, as a date. It is empty when
	// nothing is held, since a zero time serializes as year one and reads as a
	// real answer.
	Oldest string `json:"oldest,omitempty"`
	// Note explains in words what is and is not covered.
	Note string `json:"note"`
}

// Resolve answers a recall query. Matching is on the words of the question
// unless Meaning is set and semantic recall is available, in which case a
// failed embedding falls back to words rather than failing the answer.
func (r *Resolver) Resolve(ctx context.Context, q Query) Answer {
	if q.Person == "" {
		// Recall is personal. Without a person there is no scope, and a search
		// across everyone's conversations is never the right answer to fall
		// back to, so it returns nothing rather than everything.
		return Answer{Query: q.Text, Episodes: []Episode{}, Scope: r.scope()}
	}
	sq := episode.Query{Text: q.Text, Person: q.Person, Limit: q.Limit}
	var hits []episode.Result
	if q.Text != "" && q.Meaning && r.Semantic() {
		if vec, err := r.embedder.Embed(ctx, q.Text); err == nil {
			hits = r.store.SearchSemantic(vec, sq)
		}
	}
	// Fall back to keyword when semantic found nothing, not only when it was
	// never run. A query embedded by a different model than the index scores
	// every episode at or below zero and returns an empty, non-nil slice, which
	// is exactly the case where a confident empty answer is most misleading.
	if len(hits) == 0 {
		hits = r.store.Search(sq)
	}
	ans := Answer{
		Query:    q.Text,
		Person:   string(q.Person),
		Episodes: make([]Episode, 0, len(hits)),
		Scope:    r.scope(),
	}
	for _, hit := range hits {
		view := r.view(hit, q.Person)
		if q.Explain {
			view.Solution = r.solution(ctx, hit.Episode)
		}
		ans.Episodes = append(ans.Episodes, view)
	}
	return ans
}

// view renders one search hit for display.
func (r *Resolver) view(hit episode.Result, asker model.ID) Episode {
	ep := hit.Episode
	others := ep.Others(asker)
	people := make([]Person, 0, len(others))
	for _, id := range others {
		people = append(people, r.person(id))
	}
	out := Episode{
		People:    people,
		ID:        ep.ID,
		Place:     ep.Place,
		Source:    ep.Source,
		Kind:      string(ep.Kind),
		When:      ep.Occurred,
		Messages:  ep.Messages,
		Permalink: ep.Permalink,
		Matched:   hit.Matched,
		Strength:  roundTwo(hit.Strength),
	}
	if r.horizon > 0 && !ep.Occurred.IsZero() && time.Since(ep.Occurred) > r.horizon {
		out.LinkMayHaveExpired = true
	}
	return out
}

// person names an identifier from the graph, falling back to the identifier
// itself when the graph has never seen it.
func (r *Resolver) person(id model.ID) Person {
	if p := r.ix.Graph.People[r.ix.Canonical(id)]; p != nil {
		return Person{Name: p.Name, Email: p.Email, Title: p.Title}
	}
	return Person{ID: string(id)}
}

// scope describes the history that was searched.
func (r *Resolver) scope() Scope {
	all := r.store.All()
	sources := make(map[string]bool)
	var oldest time.Time
	for _, ep := range all {
		sources[ep.Source] = true
		if ep.Occurred.IsZero() {
			continue
		}
		if oldest.IsZero() || ep.Occurred.Before(oldest) {
			oldest = ep.Occurred
		}
	}
	names := make([]string, 0, len(sources))
	for s := range sources {
		names = append(names, s)
	}
	sort.Strings(names)
	oldestText := ""
	if !oldest.IsZero() {
		oldestText = oldest.Format(time.DateOnly)
	}
	return Scope{
		Sources:  names,
		Episodes: len(all),
		Oldest:   oldestText,
		Note:     scopeNote(names),
	}
}

// scopeNote says in words what a recall answer covered, so a miss reads as a
// gap in what was indexed rather than proof that nothing ever happened.
func scopeNote(sources []string) string {
	if len(sources) == 0 {
		return "Nothing has been indexed yet: run peepwho index with --episodes."
	}
	note := "Covers what peepwho has indexed from " + joinWords(sources) +
		", within each source's history window."
	for _, s := range sources {
		if s == "slack" {
			return note + " Slack covers channels the bot can read; direct messages are not indexed."
		}
	}
	return note
}

// joinWords lists names in prose.
func joinWords(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// roundTwo trims a strength to two decimals for stable output.
func roundTwo(f float64) float64 { return math.Round(f*100) / 100 }
