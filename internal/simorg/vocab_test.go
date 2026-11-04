package simorg

import (
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/text"
)

// TestSubjectVocabulariesAreDisjoint guards the assumption the whole quality
// harness rests on: each subject owns its words outright, so exactly one
// person can be the right answer. If two subjects ever share a stem, a scored
// miss stops meaning what the test says it means.
func TestSubjectVocabulariesAreDisjoint(t *testing.T) {
	t.Parallel()
	owner := make(map[string]string)
	for _, s := range subjects {
		// The question is built from the topic name, so those words have to be
		// in the vocabulary or the owner cannot be found by asking.
		for _, want := range text.Terms(s.Topic) {
			found := false
			for _, w := range s.Words {
				if len(text.Terms(w)) > 0 && text.Terms(w)[0] == want {
					found = true
				}
			}
			if !found {
				t.Errorf("subject %q: no word stems to %q, so the question cannot match its owner",
					s.Topic, want)
			}
		}
		for _, w := range s.Words {
			terms := text.Terms(w)
			if len(terms) == 0 {
				t.Errorf("subject %q: word %q tokenizes to nothing", s.Topic, w)
				continue
			}
			stem := terms[0]
			if other, taken := owner[stem]; taken && other != s.Topic {
				t.Errorf("stem %q is claimed by both %q and %q", stem, other, s.Topic)
			}
			owner[stem] = s.Topic
		}
	}

	// A team name outranks a text mention by a wide margin, so a team sharing
	// a word with a subject would hand the win to everyone on that team.
	for _, team := range teams {
		for _, stem := range text.Terms(team) {
			if subject, taken := owner[stem]; taken {
				t.Errorf("team %q shares the stem %q with subject %q, which would outrank its owner",
					team, stem, subject)
			}
		}
	}

	// Filler templates add sentence shape to generated messages. A filler word
	// sharing a stem with a subject would hand that subject's owner free hits
	// from everyone else's chatter, which corrupts every score at once.
	for _, group := range [][]string{fillers.Owner, fillers.Chatter} {
		for _, tpl := range group {
			for _, stem := range text.Terms(strings.ReplaceAll(tpl, "%s", " ")) {
				if subject, taken := owner[stem]; taken {
					t.Errorf("filler word stem %q collides with subject %q in %q", stem, subject, tpl)
				}
			}
		}
	}

	// Job titles are matched too, for the same reason, and a title is the most
	// dangerous of these: it applies to everyone holding it at once.
	titles := []string{"Engineer", "Senior Engineer", "Staff Engineer", "Manager"}
	titles = append(titles, icTitles...)
	for _, pool := range titlesByTeam {
		titles = append(titles, pool...)
	}
	for _, title := range titles {
		for _, stem := range text.Terms(title) {
			if subject, taken := owner[stem]; taken {
				t.Errorf("title %q shares the stem %q with subject %q", title, stem, subject)
			}
		}
	}
}
