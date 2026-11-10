// Package episode records the bounded conversations a person took part in, so
// whotoask can answer which past discussion solved a problem and who was in it.
// An episode holds who, where, and when, plus a link back to the source. The
// conversation itself stays in the tool it happened in unless the archive is
// enabled.
package episode

import (
	"slices"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/model"
)

// Kind classifies what shape of conversation an episode came from, so an
// answer can say whether it is pointing at a thread, a stretch of channel
// chatter, or a resolved ticket.
type Kind string

const (
	// KindThread is a threaded conversation with replies.
	KindThread Kind = "thread"
	// KindWindow is a run of channel messages close together in time, the
	// shape guidance takes when nobody starts a thread.
	KindWindow Kind = "window"
	// KindIncident is a resolved operational incident.
	KindIncident Kind = "incident"
	// KindIssue is a resolved ticket.
	KindIssue Kind = "issue"
	// KindChange is a merged code change and its review discussion.
	KindChange Kind = "change"
)

// Episode is one bounded conversation whotoask can point back to.
type Episode struct {
	// ID uniquely identifies the episode across runs, so re-indexing updates
	// an episode rather than duplicating it.
	ID string `json:"id"`
	// Source names the origin connector, such as "slack".
	Source string `json:"source"`
	// Kind classifies the conversation shape.
	Kind Kind `json:"kind"`
	// Place is the human-readable location, such as a channel or project name.
	Place string `json:"place"`
	// PlaceID is the source's own identifier for that location.
	PlaceID string `json:"place_id,omitempty"`
	// Title is a short subject line when the source has one, such as an issue
	// or incident title. Chat episodes have none.
	Title string `json:"title,omitempty"`
	// Participants are the canonical people who took part, most involved
	// first.
	Participants []model.ID `json:"participants"`
	// Asked is the person who opened the conversation: the thread parent's
	// author, the issue reporter, the pull request author. Empty when the shape
	// carries no such notion, such as a run of loose messages where several
	// people simply talked, or an incident an alert opened rather than a
	// person. When set it is also a participant, and Answered names the rest.
	Asked model.ID `json:"asked,omitempty"`
	// Occurred is when the conversation last saw activity.
	Occurred time.Time `json:"occurred"`
	// Permalink points back to the conversation in its own tool.
	Permalink string `json:"permalink,omitempty"`
	// Messages counts the messages the episode was built from.
	Messages int `json:"messages,omitempty"`
	// Archive holds retained conversation content. It is empty unless the
	// archive is enabled, and it is what a Memory answer quotes.
	Archive []Note `json:"archive,omitempty"`
	// Body is the conversation text a connector hands over so the episode can
	// be indexed. The store tokenizes it and drops it, and it is never
	// serialized: an episode on disk holds terms, not messages.
	Body string `json:"-"`
}

// Note is one retained message inside an archived episode.
type Note struct {
	// Author is the canonical person who wrote it.
	Author model.ID `json:"author"`
	// At is when it was written.
	At time.Time `json:"at"`
	// Text is the message body as written.
	Text string `json:"text"`
}

// Archived reports whether the episode carries retained content.
func (e *Episode) Archived() bool { return len(e.Archive) > 0 }

// ArchiveText returns the retained conversation text as one string, empty when
// nothing was retained. Semantic indexing embeds it so an archive is
// searchable by meaning and not only by keyword, which is the mode a reader is
// most likely to use months later when the exact words are forgotten.
func (e *Episode) ArchiveText() string { return archiveText(e.Archive) }

// Involves reports whether person took part in the episode.
func (e *Episode) Involves(person model.ID) bool {
	return slices.Contains(e.Participants, person)
}

// Others returns the participants apart from person, which is what a recall
// answer names: the people who helped, not the asker.
func (e *Episode) Others(person model.ID) []model.ID {
	out := make([]model.ID, 0, len(e.Participants))
	for _, p := range e.Participants {
		if p != person {
			out = append(out, p)
		}
	}
	return out
}

// Answered returns the people who replied to what Asked opened, in the order
// the source listed them. It returns nothing when Asked is empty: a shape with
// no asker has no answerers, and naming its participants there would credit
// everyone present with answering a question nobody asked. Callers that need
// to tell an episode with no asker apart from one whose asker drew no replies
// read Asked itself.
func (e *Episode) Answered() []model.ID {
	if e.Asked == "" {
		return nil
	}
	return e.Others(e.Asked)
}

// Text returns the searchable text of the episode's own fields, which is
// indexed alongside its conversation text.
func (e *Episode) Text() string {
	return strings.TrimSpace(e.Title + " " + e.Place)
}
