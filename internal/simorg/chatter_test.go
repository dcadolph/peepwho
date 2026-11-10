package simorg

import (
	"context"
	"testing"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestAnswerDropsChatter asks the demo company about every subject by name and
// checks that an answer led by a declared match carries nobody and no channel
// that only mentioned the words, which is what padded "kafka lag" out to five
// with a recruiter and a benefits channel.
func TestAnswerDropsChatter(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	res := resolve.NewKeyword(ix)
	trimmed := 0
	for _, s := range subjects {
		ans, err := res.Resolve(context.Background(), s.Topic, 5)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", s.Topic, err)
		}
		if len(ans.People) > 0 && ans.People[0].Declared {
			for _, m := range ans.People {
				if !m.Declared {
					t.Errorf("%q: undeclared %s rode along behind a declared leader", s.Topic, m.Person.ID)
				}
			}
		}
		if len(ans.Channels) > 0 && ans.Channels[0].Declared {
			for _, m := range ans.Channels {
				if !m.Declared {
					t.Errorf("%q: undeclared channel %s rode along behind a declared leader",
						s.Topic, m.Channel.Name)
				}
			}
		}
		if len(ans.People) < len(ix.Search(s.Topic, 5)) {
			trimmed++
		}
	}
	if trimmed == 0 {
		t.Error("no answer was trimmed, so this test proves nothing about trimming")
	}
}

// TestAnswerKeepsOwner asks every question in the simulated company and checks
// that trimming never costs the right person: whenever the untrimmed ranking
// held the owner, the answer still does. A common word such as "people" hits a
// whole team by name, and treating that as declared once dropped the login
// owner who led on what they had said.
func TestAnswerKeepsOwner(t *testing.T) {
	t.Parallel()
	built, err := Build(Spec{Seed: 7}, t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const limit = 5
	res := resolve.NewKeyword(built.Index)
	for _, q := range built.Org.Questions {
		if q.WantPerson == "" {
			continue
		}
		ans, err := res.Resolve(context.Background(), q.Text, limit)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", q.Text, err)
		}
		held := false
		for _, m := range built.Index.Search(q.Text, limit) {
			held = held || m.Person.ID == q.WantPerson
		}
		kept := false
		for _, m := range ans.People {
			kept = kept || m.Person.ID == q.WantPerson
		}
		if held && !kept {
			t.Errorf("%q: owner %s was in the untrimmed ranking and is gone from the answer",
				q.Text, q.WantPerson)
		}
	}
}
