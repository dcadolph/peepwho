// Package model defines the normalized expertise graph: the people, teams,
// orgs, and topics that whotoask indexes and ranks.
package model

import (
	"maps"
	"slices"
	"strings"

	"github.com/dcadolph/whotoask/internal/util"
)

// ID is a stable identifier for a graph entity.
type ID string

// IsTeam reports whether a person id names a team rather than a person. A
// CODEOWNERS entry may name @org/team, which keeps its slash through to the id,
// and no source puts a slash in a person's own identifier. The graph keeps such
// owners as people so ownership can see them, but nothing that counts or lists
// people may include them.
func (id ID) IsTeam() bool {
	s := string(id)
	if _, rest, ok := strings.Cut(s, ":"); ok {
		s = rest
	}
	return strings.Contains(s, "/")
}

// PeopleCount returns how many entries of people name a person, not a team.
func PeopleCount(people map[ID]*Person) int {
	n := 0
	for id := range people {
		if !id.IsTeam() {
			n++
		}
	}
	return n
}

// Person is an individual in the organization.
type Person struct {
	// ID uniquely identifies the person.
	ID ID
	// Name is the person's display name.
	Name string
	// Email is the person's work email address.
	Email string
	// Title is the person's job title.
	Title string
	// TeamID links the person to their team.
	TeamID ID
	// OrgID links the person to their organization.
	OrgID ID
	// ManagerID links the person to their manager, if known.
	ManagerID ID
	// Identities lists alternate identifiers merged into this person, such as
	// a GitHub login joined to an email.
	Identities []ID
	// Links are deep links that open a conversation with the person in a
	// source's own app, such as slack://user?team=T0&id=U1, sorted.
	Links []string `json:"Links,omitempty"`
	// Topics maps a topic ID to this person's affinity weight for it.
	Topics map[ID]float64
	// Owns lists topic IDs this person is a declared owner of, from a source of
	// record such as CODEOWNERS, as opposed to topics they merely work in.
	Owns []ID
	// Recent holds the part of each topic weight earned lately, so a subject
	// somebody knows best but has stopped working on can be told from one they
	// are still in. Absent from this map while present in Topics means their
	// work on it has stopped.
	Recent map[ID]float64
	// Direct is the part of each topic weight earned by changing something
	// inside the area itself, rather than by a file elsewhere that happens to
	// carry its name. A platform file inside another integration, such as
	// voip/assist_satellite.py, is real work on the platform and no evidence at
	// all about who owns the integration of that name, so ownership reads this
	// and search does not.
	Direct map[ID]float64
	// Stated is the part of each topic weight that a source of record assigned
	// rather than the person earning it: being listed in CODEOWNERS or given a
	// topics column in an org chart. Subtracting it from Topics leaves the work
	// they actually did, which is the difference between owning an area and
	// working in it. Without it, indexing a CODEOWNERS file makes every owner
	// look active in everything they own.
	Stated map[ID]float64
	// Account is what the sources that know account status say about the
	// person's accounts, joined so one active account keeps them active.
	Account AccountStatus `json:"Account,omitempty"`
}

// Deactivated reports whether every account a status-aware source knows for
// the person is deactivated. They stay in the graph, since what they knew is
// still on record, and rank below everyone who can still be reached.
func (p *Person) Deactivated() bool { return p.Account == AccountDeactivated }

// AccountStatus is what a source says about a person's account.
type AccountStatus int

const (
	// AccountUnknown means the source does not report account status. It is the
	// zero value, so records and indexes from before status existed read as it.
	AccountUnknown AccountStatus = iota
	// AccountActive is an account that can still be reached.
	AccountActive
	// AccountDeactivated is an account the workspace has deactivated.
	AccountDeactivated
)

// Join combines the status of two accounts of one person. Any active account
// makes the person active, since one real person can hold several accounts and
// only some of them may be closed. Unknown never overrides a known status, so a
// source that cannot see accounts does not hide a deactivation another reports.
func (s AccountStatus) Join(o AccountStatus) AccountStatus {
	switch {
	case s == AccountActive || o == AccountActive:
		return AccountActive
	case s == AccountDeactivated || o == AccountDeactivated:
		return AccountDeactivated
	default:
		return AccountUnknown
	}
}

// Reach is the sum of the person's topic weights: how much of everything their
// expertise covers. It adds in sorted topic order, because float addition is
// not associative and a sum in map order differs between runs in its last
// digits, which is enough to reorder two people who are otherwise tied.
func (p *Person) Reach() float64 {
	var total float64
	for _, tid := range slices.Sorted(maps.Keys(p.Topics)) {
		total += p.Topics[tid]
	}
	return total
}

// Team is a named group of people within an organization.
type Team struct {
	// ID uniquely identifies the team.
	ID ID
	// Name is the team's display name.
	Name string
	// OrgID links the team to its organization.
	OrgID ID
	// Desc is an optional description of the team's remit.
	Desc string
}

// Org is a top-level organization or department.
type Org struct {
	// ID uniquely identifies the organization.
	ID ID
	// Name is the organization's display name.
	Name string
}

// Topic is a subject of expertise, keyed by a normalized lowercase name.
type Topic struct {
	// ID uniquely identifies the topic.
	ID ID
	// Name is the normalized topic text.
	Name string
	// Curated marks a topic that some source stated explicitly, such as a GitHub
	// topic, a Jira label, a CODEOWNERS path, or an org-chart topics column. A
	// topic only ever mined out of prose is not curated, which is how an
	// incidental word is told apart from a subject somebody declared.
	Curated bool
	// Sources names the connectors that produced this topic. Breadth across
	// sources is the strongest evidence a topic is real rather than a stray word.
	Sources []string
	// Near are the subjects this one is worked on alongside. It is the only
	// thing whotoask knows about a subject that does not come through the people
	// who hold it: two areas changed in the same commit are related whoever
	// changed them, and everything else relates subjects by their shared
	// experts, which cannot then be evidence about expertise without arguing in
	// a circle.
	Near map[ID]Tie
	// Ubiquitous marks a topic nearly everybody holds. Those are the scaffolding
	// of a codebase rather than subjects within it: the name of the repository,
	// the language it is written in, the directory every file sits under. They
	// are stated as plainly as any real subject and are worthless for telling
	// people apart, which is the only thing a subject is for here.
	Ubiquitous bool
}

// Salient reports whether a topic is established enough to present as a subject
// in its own right. A topic everybody holds never counts, because it
// distinguishes nobody.
//
// A subject some source stated outright counts. A topic only ever mined from
// prose has to clear two bars: several sources saw it, AND it names something
// rather than being a single ordinary word. Appearing in two sources sounds like
// corroboration and is not, because the commonest words appear everywhere:
// measured on a real tracker, that rule alone promoted 7,528 mined words to
// subjects, and they were "appearing", "avoiding", "overkill", "secondly",
// "work". Requiring more than one word cut 97% of that while keeping the ones
// that read like names, such as state-store, interactive-query, and jwt-bearer.
//
// Nothing is lost to search either way. A mined word stays in the index and
// still matches a question; it just does not get counted as a subject the
// organization has, reported as a risk, or connected to other subjects.
func (t *Topic) Salient() bool {
	if t == nil || t.Ubiquitous {
		return false
	}
	if t.Curated {
		return true
	}
	return len(t.Sources) > 1 && strings.Contains(string(t.ID), "-") &&
		util.ReadsAsName(string(t.ID))
}

// Tie is how two subjects are connected: how much of the time they move
// together, and how many people have ever done work spanning both.
type Tie struct {
	// Weight is how much of the time the two subjects move as one thing, from
	// zero to one.
	Weight float64
	// Witnesses is how many different people have ever worked across both. One
	// means the connection between them has only ever been in a single head,
	// which is a bus factor on the relationship rather than on either subject,
	// and nothing that counts experts per subject can see it.
	Witnesses int
	// Sole names that person when Witnesses is one. Several people may hold
	// both subjects while only one has ever worked across them, so it cannot be
	// recovered from the graph afterwards.
	Sole ID
}

// Channel is a place to ask, such as a Slack channel.
type Channel struct {
	// ID uniquely identifies the channel.
	ID ID
	// Name is the channel name without any leading symbol.
	Name string
	// Topic is the channel's stated topic, shown to users.
	Topic string
	// URL opens the channel in its source tool, empty when unknown.
	URL string
	// Members lists the people active in the channel, by person ID.
	Members []ID
	// Topics maps a topic ID to the channel's affinity weight for it.
	Topics map[ID]float64
}

// Graph holds the full set of entities whotoask has indexed.
type Graph struct {
	// People maps person ID to person.
	People map[ID]*Person `json:"people"`
	// Teams maps team ID to team.
	Teams map[ID]*Team `json:"teams"`
	// Orgs maps org ID to organization.
	Orgs map[ID]*Org `json:"orgs"`
	// Topics maps topic ID to topic.
	Topics map[ID]*Topic `json:"topics"`
	// Channels maps channel ID to channel.
	Channels map[ID]*Channel `json:"channels"`
}

// NewGraph returns an empty graph with initialized maps.
func NewGraph() *Graph {
	return &Graph{
		People:   make(map[ID]*Person),
		Teams:    make(map[ID]*Team),
		Orgs:     make(map[ID]*Org),
		Topics:   make(map[ID]*Topic),
		Channels: make(map[ID]*Channel),
	}
}

// Match is a single ranked answer: a person, their team, the relevance score,
// and the human-readable reasons the person matched.
type Match struct {
	// Person is the matched individual.
	Person *Person
	// Team is the matched person's team, if known.
	Team *Team
	// Score is the relevance score; higher is more relevant.
	Score float64
	// Strength estimates how trustworthy the match is, from zero to one:
	// query-term coverage scaled by the strength of the matched fields.
	Strength float64
	// Declared reports that the match rests on a stated topic, title, or name.
	// Team names are made of everyday words such as "people" and mentions are
	// passing talk, so neither counts.
	Declared bool
	// Reasons explains why the person matched, for transparency.
	Reasons []string
}

// ChannelMatch is a ranked channel answer: the channel, its score, the reasons
// it matched, and the people most worth contacting there.
type ChannelMatch struct {
	// Channel is the matched channel.
	Channel *Channel
	// Score is the relevance score; higher is more relevant.
	Score float64
	// Strength estimates how trustworthy the match is, from zero to one:
	// query-term coverage scaled by the strength of the matched fields.
	Strength float64
	// Declared reports that the match rests on the channel's name, topic, or
	// purpose rather than on passing mentions in its messages alone.
	Declared bool
	// Reasons explains why the channel matched.
	Reasons []string
	// TopMembers are the most relevant active members, most relevant first.
	TopMembers []*Person
}
