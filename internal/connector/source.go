// Package connector pulls raw records from work sources and normalizes them
// into records the indexer merges into the expertise graph.
package connector

import (
	"context"
	"time"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
)

// Record is one normalized observation from a source. A KindPerson record
// describes a person: identity, org placement, topics, and mined text. A
// KindChannel record describes a place to ask, with the channel name, topic,
// text, and active members.
type Record struct {
	// Kind classifies the record; the zero value is KindPerson.
	Kind Kind
	// PersonID is a stable per-source identifier; empty derives one from email.
	PersonID string
	// Name is the person's display name.
	Name string
	// Email is the person's work email.
	Email string
	// AltIDs are other identifiers this source knows for the person, such as an
	// AD sign-in name or a second email. Each is unioned with the person so any
	// of them joins them across sources.
	AltIDs []string
	// Title is the person's job title.
	Title string
	// Team is the person's team name.
	Team string
	// Org is the person's organization name.
	Org string
	// Manager is the manager's email or identifier, if known.
	Manager string
	// Topics are explicit expertise tags for the person or channel: a label, a
	// repository topic, an owned path, or an org-chart column. Something stated
	// deliberately, which makes it the strongest topic evidence a source can give.
	Topics []string
	// RecentTopics are the subjects this person worked on lately, a subset of
	// Topics. WhoToAsk answers with whoever knows a subject best, and knowing it
	// best is not the same as still working on it: on a real repository the
	// leading expert of two subjects in five had already stopped touching them,
	// and was less than half as likely to still hold the subject a while later.
	// Naming them without saying so sends somebody to ask a person who has
	// moved on.
	RecentTopics []string
	// DirectTopics are the subjects this person's own changed paths named, a
	// subset of Topics. It is the difference between working inside an area and
	// touching a file elsewhere that carries its name, which decides who owns
	// the area rather than who has merely met it.
	DirectTopics []string
	// WeakTopics are topics inferred from prose rather than declared, such as the
	// words of an issue title. They contribute the same affinity but do not, on
	// their own, establish a subject: a topic seen only this way has to recur
	// across sources before it counts as one.
	WeakTopics []string
	// Links are ties from this record's subject to other subjects, on a
	// KindTopic record. They say which subjects are worked on together, which
	// is the one thing about a subject that does not come through the people
	// who hold it: two areas changed in the same commit are related whoever
	// made the commit.
	Links []TopicLink
	// Members lists person references active in a KindChannel record.
	Members []string
	// Link opens the record's subject in its source tool: the web URL of a
	// KindChannel record, or a deep link that starts a direct message with the
	// person on a KindPerson record.
	Link string
	// Account is whether the person's account in this source is active or
	// deactivated, left unknown by sources that do not report it.
	Account model.AccountStatus
	// Text is free-form text attributed to the person or channel, mined for
	// topics. It is readable message content and is never written to disk: the
	// index persists only the stemmed Terms derived from it, so a stored index
	// holds a search index rather than the messages themselves.
	Text string
	// Terms are the stemmed search terms derived from Text. The index fills and
	// stores them in place of Text so a saved index can be rebuilt on a later
	// merge without keeping any readable message text on disk.
	Terms []string
	// Source names the origin connector, e.g. "org-csv".
	Source string
	// Weight scales this record's affinity contribution; zero means one.
	Weight float64
	// Time is when the described activity happened. The zero value marks the
	// record as a current fact that never decays.
	Time time.Time
}

// Kind classifies a record. For KindChannel records the person identity fields
// are unused: Name is the channel name, Title is the channel topic, Text is the
// purpose and sampled message text mined for affinity, and Members lists the
// person references active in the channel.
type Kind int

const (
	// KindPerson describes a person. It is the zero value.
	KindPerson Kind = iota
	// KindChannel describes a channel, a place to ask.
	KindChannel
	// KindTopic describes a subject rather than a person: what else it is
	// worked on alongside. Name is the subject and Links are its ties.
	KindTopic
)

// TopicLink is one subject's tie to another, seen by a source that watched them
// change together.
type TopicLink struct {
	// To is the other subject's name.
	To string
	// Weight is how much of the time the two move as one thing.
	Weight float64
	// Witnesses is how many different people have ever done work spanning both
	// subjects. One means the knowledge that these two belong together has only
	// ever been in one head: the subjects each have their own experts, and the
	// connection between them has nobody but this person.
	Witnesses int
	// Sole names that person when Witnesses is one, since nothing in the graph
	// can identify them afterwards. Several people may hold both subjects while
	// only one has ever worked across them.
	Sole string
}

// PullRef identifies one pull request: the repository it belongs to and its
// number. The number alone collides across repositories, so a number-keyed
// join credited reviewers into whichever same-numbered repository won a
// goroutine race. Repo is empty when a clone's remotes name no forge
// repository, a data-room copy or a mirror; such an entry can still be joined
// on its number alone when that number is unambiguous, which the resolver
// decides rather than the connector guessing.
type PullRef struct {
	// Repo is the lowercase "owner/name", or empty when the clone says nothing.
	Repo string
	// Number is the pull request number.
	Number int
}

// Source fetches and normalizes records from one origin.
type Source interface {
	// Fetch returns the records this source currently provides.
	Fetch(ctx context.Context) ([]Record, error)
}

// EpisodeSource is implemented by sources that also observe bounded
// conversations, such as a chat thread or a resolved ticket. Episodes are
// collected during Fetch and read afterwards, so a source that does not
// implement it costs nothing and the Source interface stays one method.
type EpisodeSource interface {
	// Episodes returns the conversations seen by the most recent Fetch.
	Episodes() []episode.Episode
}

// SourceFunc adapts a function to the Source interface.
type SourceFunc func(ctx context.Context) ([]Record, error)

// Fetch calls f.
func (f SourceFunc) Fetch(ctx context.Context) ([]Record, error) { return f(ctx) }
