package feedback

import (
	"time"
	"unicode"
)

// Bundle is the redacted report a user can choose to hand to whotoask's makers.
//
// Nothing composes it but an explicit request, nothing sends it but the user's
// own hands, and nothing in it identifies the organization: a question asked of
// whotoask is itself a fact about the company, so queries are reduced to their
// shape and every name, address, and message stays home. The one free-text
// field is the comments the user typed as feedback, carried verbatim because
// they were written to be read by someone fixing the product, and because the
// user reads the whole file before it goes anywhere.
type Bundle struct {
	// Schema names the shape of this file, so a reader knows what can and
	// cannot appear in it before opening it.
	Schema string `json:"schema"`
	// Note says, inside the artifact itself, what was left out.
	Note string `json:"note"`
	// Version is the whotoask build that wrote the bundle.
	Version string `json:"version"`
	// Votes is what the feedback amounted to.
	Votes VoteStats `json:"votes"`
	// QueryShapes is how the voted questions were shaped, never what they said.
	QueryShapes []QueryShape `json:"queryShapes,omitempty"`
	// Comments are the notes the user typed with their votes, verbatim.
	Comments []BundleComment `json:"comments,omitempty"`
}

// VoteStats is the arithmetic of the feedback store.
type VoteStats struct {
	// Total is how many votes were cast.
	Total int `json:"total"`
	// Helpful and NotHelpful split the total by verdict.
	Helpful    int `json:"helpful"`
	NotHelpful int `json:"notHelpful"`
	// OnPeople and OnChannels split the total by what was voted on.
	OnPeople   int `json:"onPeople"`
	OnChannels int `json:"onChannels"`
}

// QueryShape is one question reduced to its silhouette: how many words it had
// and how the vote went. The words themselves never leave.
type QueryShape struct {
	// Words is how many words the question had.
	Words int `json:"words"`
	// Helpful is whether the answer was voted helpful.
	Helpful bool `json:"helpful"`
}

// BundleComment is one typed note, with its verdict and when it was written.
type BundleComment struct {
	// Vote is "helpful" or "not helpful".
	Vote string `json:"vote"`
	// Comment is the user's note, exactly as typed.
	Comment string `json:"comment"`
	// Time is when the vote was cast.
	Time time.Time `json:"time"`
}

// BundleNote is the redaction statement written into every bundle.
const BundleNote = "Queries, names, addresses, and message text never appear in this file. " +
	"Questions are reduced to a word count, and the only free text is what was " +
	"typed as feedback. Read everything before sending it anywhere."

// NewBundle reduces the feedback store to the report. It is a pure function of
// the entries, so what it keeps and what it drops can be tested directly.
func NewBundle(version string, entries []Entry) Bundle {
	b := Bundle{Schema: "whotoask-feedback-bundle/1", Note: BundleNote, Version: version}
	for _, e := range entries {
		b.Votes.Total++
		verdict := "not helpful"
		if e.Vote == Helpful {
			b.Votes.Helpful++
			verdict = "helpful"
		} else {
			b.Votes.NotHelpful++
		}
		if e.Person != "" {
			b.Votes.OnPeople++
		}
		if e.Channel != "" {
			b.Votes.OnChannels++
		}
		b.QueryShapes = append(b.QueryShapes, QueryShape{
			Words: countWords(e.Query), Helpful: e.Vote == Helpful,
		})
		if e.Comment != "" {
			b.Comments = append(b.Comments, BundleComment{
				Vote: verdict, Comment: e.Comment, Time: e.Time,
			})
		}
	}
	return b
}

// countWords counts runs of non-space characters.
func countWords(s string) int {
	n, in := 0, false
	for _, r := range s {
		if unicode.IsSpace(r) {
			in = false
			continue
		}
		if !in {
			n++
			in = true
		}
	}
	return n
}
