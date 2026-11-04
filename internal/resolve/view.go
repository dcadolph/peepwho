package resolve

import (
	"math"
	"sort"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// Strength labels partition the zero-to-one match range for display.
const (
	// strongStrength is the floor of a strong match.
	strongStrength = 0.75
	// moderateStrength is the floor of a moderate match.
	moderateStrength = 0.45
)

// StrengthLabel names a match strength for display: strong, moderate, or
// weak. It returns the empty string for zero, when the strength is unknown.
func StrengthLabel(c float64) string {
	switch {
	case c <= 0:
		return ""
	case c >= strongStrength:
		return "strong"
	case c >= moderateStrength:
		return "moderate"
	default:
		return "weak"
	}
}

// roundStrength trims a strength to two decimals for stable JSON output.
func roundStrength(c float64) float64 {
	return math.Round(c*100) / 100
}

// topTopics returns up to n of a person's topic names, strongest first, ties
// broken alphabetically. Topic identifiers are readable slugs, so they double
// as display names.
// salientTopics returns a person's strongest topics, keeping only the subjects
// the organization actually has. Topic text is mined from prose as well as
// declared, so an unfiltered list shows a person "knowing" words like
// "regression" or "runbook" that were never a subject: fine as ranking signal,
// embarrassing as an answer to what somebody is known for.
func salientTopics(ix *index.Index, topics map[model.ID]float64, n int) []string {
	if ix == nil {
		return topTopics(topics, n)
	}
	kept := make(map[model.ID]float64, len(topics))
	for id, w := range topics {
		if ix.Graph.Topics[id].Salient() {
			kept[id] = w
		}
	}
	// A person whose every topic was mined from prose still deserves an answer,
	// so fall back to the unfiltered list rather than showing nothing.
	if len(kept) == 0 {
		return topTopics(topics, n)
	}
	return topTopics(kept, n)
}

func topTopics(topics map[model.ID]float64, n int) []string {
	if len(topics) == 0 {
		return nil
	}
	names := make([]string, 0, len(topics))
	for id := range topics {
		names = append(names, string(id))
	}
	sort.Slice(names, func(i, j int) bool {
		wi, wj := topics[model.ID(names[i])], topics[model.ID(names[j])]
		if wi != wj {
			return wi > wj
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	return names
}

// JSONAnswer is a flat, JSON-friendly view of an Answer, shared by the CLI and
// the web server so both emit the same shape.
type JSONAnswer struct {
	// Query echoes the question asked.
	Query string `json:"query,omitempty"`
	// Summary is the written recommendation, present in LLM mode.
	Summary string `json:"summary,omitempty"`
	// People is the ranked list of people to talk to.
	People []JSONPerson `json:"people"`
	// Channels is the ranked list of places to ask.
	Channels []JSONChannel `json:"channels,omitempty"`
}

// JSONPerson is one ranked person.
type JSONPerson struct {
	// ID is the person's canonical identifier, the stable target for feedback.
	ID string `json:"id"`
	// Name is the person's display name.
	Name string `json:"name"`
	// Email is the person's work email.
	Email string `json:"email,omitempty"`
	// Title is the person's job title.
	Title string `json:"title,omitempty"`
	// Team is the person's team name.
	Team string `json:"team,omitempty"`
	// Identities lists alternate identifiers merged into this person, such as
	// a GitHub login joined to an email.
	Identities []string `json:"identities,omitempty"`
	// Topics are the person's strongest expertise areas, strongest first.
	Topics []string `json:"topics,omitempty"`
	// Score is the relevance score.
	Score float64 `json:"score"`
	// Strength is how strongly this result matches, from zero to one. It is
	// deterministic scoring, not a probability, which is why it is not called
	// a confidence.
	// Zero means unknown and is omitted.
	Strength float64 `json:"strength,omitempty"`
	// Reasons explains why the person matched.
	Reasons []string `json:"reasons,omitempty"`
}

// JSONChannel is one ranked channel.
type JSONChannel struct {
	// Name is the channel name.
	Name string `json:"name"`
	// Topic is the channel's stated topic.
	Topic string `json:"topic,omitempty"`
	// URL opens the channel in its source tool.
	URL string `json:"url,omitempty"`
	// Score is the relevance score.
	Score float64 `json:"score"`
	// Strength is how strongly this result matches, from zero to one. It is
	// deterministic scoring, not a probability, which is why it is not called
	// a confidence.
	// Zero means unknown and is omitted.
	Strength float64 `json:"strength,omitempty"`
	// Reasons explains why the channel matched.
	Reasons []string `json:"reasons,omitempty"`
	// Members are the most relevant people active in the channel.
	Members []JSONMember `json:"members,omitempty"`
}

// JSONMember is one active channel member.
type JSONMember struct {
	// Name is the member's display name.
	Name string `json:"name"`
	// Email is the member's work email.
	Email string `json:"email,omitempty"`
}

// JSONJoin is one inferred identity merge on a profile: an alias folded in, how
// confident the merge is, and the evidence for it.
type JSONJoin struct {
	// Alias is the identifier that was folded in, such as "github:kim-doe".
	Alias string `json:"alias"`
	// Confidence is how sure the merge is, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Reason names the evidence, such as "unique name match".
	Reason string `json:"reason"`
}

// JSONProfile is the full picture of one person, for the profile view.
type JSONProfile struct {
	// ID is the person's canonical identifier.
	ID string `json:"id"`
	// Name is the person's display name.
	Name string `json:"name"`
	// Email is the person's work email.
	Email string `json:"email,omitempty"`
	// Title is the person's job title.
	Title string `json:"title,omitempty"`
	// Team is the person's team name.
	Team string `json:"team,omitempty"`
	// Org is the person's organization name.
	Org string `json:"org,omitempty"`
	// Manager is the person's manager, if known.
	Manager *JSONMember `json:"manager,omitempty"`
	// Identities lists alternate identifiers merged into this person.
	Identities []string `json:"identities,omitempty"`
	// Joins explains the inferred merges among those identities: which alias,
	// how confident, and the evidence. Certain email or provider-id joins are
	// not listed.
	Joins []JSONJoin `json:"joins,omitempty"`
	// Channels lists the channels the person is active in.
	Channels []string `json:"channels,omitempty"`
	// Topics are the person's expertise areas, strongest first.
	Topics []string `json:"topics,omitempty"`
}

// ProfileView renders a profile for the web and CLI.
func ProfileView(ix *index.Index, p index.Profile) JSONProfile {
	out := JSONProfile{
		ID:     string(p.Person.ID),
		Name:   p.Person.Name,
		Email:  p.Person.Email,
		Title:  p.Person.Title,
		Topics: salientTopics(ix, p.Person.Topics, 32),
	}
	for _, id := range p.Person.Identities {
		out.Identities = append(out.Identities, string(id))
	}
	for _, j := range p.Joins {
		out.Joins = append(out.Joins, JSONJoin{Alias: string(j.Alias), Confidence: j.Confidence, Reason: j.Reason})
	}
	if p.Team != nil {
		out.Team = p.Team.Name
	}
	if p.Org != nil {
		out.Org = p.Org.Name
	}
	if p.Manager != nil {
		out.Manager = &JSONMember{Name: p.Manager.Name, Email: p.Manager.Email}
	}
	for _, ch := range p.Channels {
		out.Channels = append(out.Channels, ch.Name)
	}
	return out
}

// View renders the answer as a flat JSONAnswer for the given query.
func (a Answer) View(query string) JSONAnswer {
	out := JSONAnswer{
		Query:   query,
		Summary: a.Summary,
		People:  make([]JSONPerson, 0, len(a.People)),
	}
	for _, m := range a.People {
		jp := JSONPerson{
			ID:       string(m.Person.ID),
			Name:     m.Person.Name,
			Email:    m.Person.Email,
			Title:    m.Person.Title,
			Topics:   salientTopics(a.ix, m.Person.Topics, 8),
			Score:    m.Score,
			Strength: roundStrength(m.Strength),
			Reasons:  m.Reasons,
		}
		for _, id := range m.Person.Identities {
			jp.Identities = append(jp.Identities, string(id))
		}
		if m.Team != nil {
			jp.Team = m.Team.Name
		}
		out.People = append(out.People, jp)
	}
	for _, c := range a.Channels {
		jc := JSONChannel{
			Name:     c.Channel.Name,
			Topic:    c.Channel.Topic,
			URL:      c.Channel.URL,
			Score:    c.Score,
			Strength: roundStrength(c.Strength),
			Reasons:  c.Reasons,
		}
		for _, p := range c.TopMembers {
			jc.Members = append(jc.Members, JSONMember{Name: p.Name, Email: p.Email})
		}
		out.Channels = append(out.Channels, jc)
	}
	return out
}
