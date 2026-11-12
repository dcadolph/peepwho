package resolve

import (
	"math"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// defaultSearchLimit caps a search that does not ask for a size.
const defaultSearchLimit = 20

// SearchResult is one match from a free-text search over indexed entities.
type SearchResult struct {
	// Kind is "person", "team", or "channel". A team is a CODEOWNERS handle,
	// somewhere to look rather than somebody to ask.
	Kind string `json:"kind"`
	// ID is the entity's identifier: a person's canonical id or a channel name.
	ID string `json:"id"`
	// Name is the display name.
	Name string `json:"name"`
	// Email is the person's work email, when the match is a person.
	Email string `json:"email,omitempty"`
	// Title is the person's job title.
	Title string `json:"title,omitempty"`
	// Team is the person's team, or a channel's stated topic.
	Team string `json:"team,omitempty"`
	// Topics are the entity's topics, strongest first.
	Topics []string `json:"topics,omitempty"`
	// Score ranks the match; higher is a closer match.
	Score float64 `json:"score"`
	// Matched names the fields the query hit, such as "team" or "topic".
	Matched []string `json:"matched,omitempty"`
	// Deactivated reports a person whose account is deactivated, somebody who
	// may have known the subject but is no longer there to ask.
	Deactivated bool `json:"deactivated,omitempty"`
}

// Search finds people and channels whose name, email, title, team, or topics
// contain the query, ranked by how directly they match: an exact name beats a
// prefix beats a substring, and a name beats an email beats a title, team, or
// topic. It is a direct lookup, distinct from Resolve, which ranks by who knows
// a subject. A limit of zero or less returns a default cap.
func Search(ix *index.Index, query string, limit int) []SearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	var out []SearchResult
	for id, p := range ix.Graph.People {
		team := teamName(ix, p)
		score, matched := scorePerson(p, team, q)
		if score <= 0 {
			continue
		}
		kind := "person"
		if id.IsTeam() {
			kind = "team"
		}
		out = append(out, SearchResult{
			Kind: kind, ID: string(id), Name: nameOr(p.Name, string(id)),
			Email: p.Email, Title: p.Title, Team: team, Topics: salientTopics(ix, p.Topics, 8),
			Score: score, Matched: matched, Deactivated: p.Deactivated(),
		})
	}
	for _, ch := range ix.Graph.Channels {
		score, matched := scoreChannel(ch, q)
		if score <= 0 {
			continue
		}
		out = append(out, SearchResult{
			Kind: "channel", ID: ch.Name, Name: ch.Name, Team: ch.Topic,
			Score: score, Matched: matched,
		})
	}
	SortSearchResults(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SortSearchResults orders results for showing, closest match first. A
// departed person never sits above an active person with the same kind of
// match: within one order value the active come first, then the higher score,
// then the name and the id. With nobody deactivated this is plain score order.
func SortSearchResults(results []SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if oa, ob := searchOrder(a), searchOrder(b); oa != ob {
			return oa > ob
		}
		if a.Deactivated != b.Deactivated {
			return !a.Deactivated
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}

// searchOrder is the value a result is ordered by: its score, except that a
// departed person matched on a subject orders at the floor of the subject
// band. The work somebody did on a subject is no reason to send the asker to
// a person who has left ahead of one who is still here, so within the band a
// departed person orders below every active one.
func searchOrder(r SearchResult) float64 {
	if r.Deactivated && r.Score >= personTopicTier && r.Score < personTopicTier+topicTierSpread {
		return personTopicTier
	}
	return r.Score
}

// Subject match tier. personTopicTier is the score a subject match starts
// from. topicTierSpread is how far a subject match may rise above it for the
// work behind it, kept under the gap to the tier above. topicTierScale is the
// concentration at which half that spread is earned, on the discounted scale
// where a strong owner of one subject sits around a fifth.
const (
	personTopicTier = 8.0
	topicTierSpread = 3.0
	topicTierScale  = 0.25
)

// scorePerson scores how directly a person matches the lowercased query and
// lists the fields that hit. The score is the strongest single field; a name
// carries the most weight, then email, title, team, and topic.
func scorePerson(p *model.Person, team, q string) (float64, []string) {
	var best float64
	var matched []string
	hit := func(tier float64, field string) {
		if tier > best {
			best = tier
		}
		matched = append(matched, field)
	}
	nm := strings.ToLower(p.Name)
	switch {
	case nm == q:
		hit(100, "name")
	case strings.HasPrefix(nm, q):
		hit(60, "name")
	case strings.Contains(nm, q):
		hit(40, "name")
	}
	if p.Email != "" && strings.Contains(strings.ToLower(p.Email), q) {
		hit(30, "email")
	}
	if p.Title != "" && strings.Contains(strings.ToLower(p.Title), q) {
		hit(15, "title")
	}
	if team != "" && strings.Contains(strings.ToLower(team), q) {
		hit(12, "team")
	}
	// Everybody who holds a matching subject matches it equally as far as the
	// text is concerned, so without this the people who merely touched it come
	// back in alphabetical order and the person who owns it is wherever their
	// name happens to fall. The bonus is bounded well below the next tier, so
	// no amount of work in a subject outranks a title or team match.
	var strongest float64
	for tid, w := range p.Topics {
		if w > strongest && strings.Contains(strings.ToLower(string(tid)), q) {
			strongest = w
		}
	}
	if reach := p.Reach(); strongest > 0 && reach > 0 {
		// Discounted by how much this person does everywhere else, for the same
		// reason every other ranking here is: the few who touch all of a code
		// base out-weigh the owner of any one part of it.
		concentration := strongest / math.Sqrt(reach)
		hit(personTopicTier+topicTierSpread*concentration/(concentration+topicTierScale), "topic")
	}
	return best, matched
}

// scoreChannel scores how directly a channel matches the query, on its name
// then its stated topic.
func scoreChannel(ch *model.Channel, q string) (float64, []string) {
	var best float64
	var matched []string
	hit := func(tier float64, field string) {
		if tier > best {
			best = tier
		}
		matched = append(matched, field)
	}
	nm := strings.ToLower(ch.Name)
	switch {
	case nm == q:
		hit(90, "channel")
	case strings.HasPrefix(nm, q):
		hit(55, "channel")
	case strings.Contains(nm, q):
		hit(38, "channel")
	}
	topicHit := ch.Topic != "" && strings.Contains(strings.ToLower(ch.Topic), q)
	if !topicHit {
		for tid := range ch.Topics {
			if strings.Contains(strings.ToLower(string(tid)), q) {
				topicHit = true
				break
			}
		}
	}
	if topicHit {
		hit(10, "topic")
	}
	return best, matched
}

// teamName returns the display name of a person's team, or the empty string.
func teamName(ix *index.Index, p *model.Person) string {
	if p.TeamID == "" {
		return ""
	}
	if t := ix.Graph.Teams[p.TeamID]; t != nil {
		return t.Name
	}
	return ""
}

// nameOr returns name, or fallback when name is empty.
func nameOr(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}
