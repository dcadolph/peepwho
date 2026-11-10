package recall

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
)

// maxSolutionNotes caps the messages shown for one conversation. A person is
// reading to recognize what happened, not to re-read the channel.
const maxSolutionNotes = 12

// Summarizer writes a short account of how something was worked out. It is
// optional: without one, the conversation is shown as it was written. The
// llm package's clients satisfy it.
type Summarizer interface {
	// Chat answers a prompt under a system instruction.
	Chat(ctx context.Context, system, user string) (string, error)
}

// summarySystem instructs the model to report only what the conversation says.
const summarySystem = "You summarize work conversations for the person who took part in them. " +
	"Report only what the conversation itself says. Never invent a fix, a name, or a cause."

// SetSummarizer attaches a model that writes how a conversation resolved.
// Whatever it is, the text it receives is conversation content, so callers
// must have cleared it against the egress policy first.
func (r *Resolver) SetSummarizer(s Summarizer) { r.summarizer = s }

// Solution is what a kept conversation says about how something was worked
// out. It exists only for conversations whose content whotoask holds.
type Solution struct {
	// Summary is a written account of how it resolved, present only when a
	// model wrote one.
	Summary string `json:"summary,omitempty"`
	// Notes are the messages themselves, in the order they were written.
	Notes []Note `json:"notes"`
	// Truncated reports that the conversation ran longer than what is shown.
	Truncated bool `json:"truncated,omitempty"`
	// Source is where to read the rest, which is the conversation's own link.
	Source string `json:"source,omitempty"`
}

// Note is one message from a kept conversation.
type Note struct {
	// Author is who wrote it.
	Author string `json:"author"`
	// Text is what they wrote.
	Text string `json:"text"`
	// At is when, as a date.
	At string `json:"at,omitempty"`
}

// solution renders the kept content of a conversation, or nil when whotoask
// holds only a pointer to it.
func (r *Resolver) solution(ctx context.Context, ep *episode.Episode) *Solution {
	if !ep.Archived() {
		return nil
	}
	kept, truncated := trimConversation(ep.Archive)
	out := &Solution{Source: ep.Permalink, Truncated: truncated}
	out.Notes = make([]Note, 0, len(kept))
	for _, n := range kept {
		out.Notes = append(out.Notes, Note{
			Author: r.name(n.Author),
			Text:   n.Text,
			At:     dateOf(n.At),
		})
	}
	if r.summarizer != nil {
		// A model that fails or stalls costs the summary, never the answer:
		// the conversation itself is already the useful part.
		if summary, err := r.summarizer.Chat(ctx, summarySystem, solutionPrompt(ep, out.Notes)); err == nil {
			out.Summary = strings.TrimSpace(summary)
		}
	}
	return out
}

// trimConversation cuts a long conversation to a readable length, keeping both
// ends: the problem is stated at the start and settled at the end, so trimming
// only the tail would drop the very thing this feature exists to show. It
// reports whether anything was left out.
func trimConversation(notes []episode.Note) ([]episode.Note, bool) {
	if len(notes) <= maxSolutionNotes {
		return notes, false
	}
	head := maxSolutionNotes / 3
	tail := maxSolutionNotes - head
	kept := make([]episode.Note, 0, maxSolutionNotes)
	kept = append(kept, notes[:head]...)
	return append(kept, notes[len(notes)-tail:]...), true
}

// name renders a person for display, falling back to the identifier when the
// graph has never seen them.
func (r *Resolver) name(id model.ID) string {
	p := r.person(id)
	switch {
	case p.Name != "":
		return p.Name
	case p.Email != "":
		return p.Email
	default:
		return p.ID
	}
}

// solutionPrompt asks for a short account of how the conversation resolved,
// and insists on saying so when it did not. A conversation that trails off
// without an answer is common, and reporting one as solved would be worse than
// saying nothing.
func solutionPrompt(ep *episode.Episode, notes []Note) string {
	var b strings.Builder
	b.WriteString("Below is a work conversation. In at most three sentences, say how the " +
		"problem was resolved and what the fix was. Use only what the conversation says. " +
		"If it does not reach a resolution, reply exactly: No resolution in this conversation.\n\n")
	if ep.Place != "" {
		fmt.Fprintf(&b, "Channel: %s\n", ep.Place)
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "%s: %s\n", n.Author, n.Text)
	}
	return b.String()
}

// dateOf renders a note's time as a date, or the empty string when unknown.
func dateOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.DateOnly)
}
