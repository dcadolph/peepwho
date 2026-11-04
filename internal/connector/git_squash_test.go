package connector

import (
	"fmt"
	"testing"
	"time"
)

// TestUnshareSquashMailboxes pins the shared-mailbox repair measured on
// esphome: one noreply address authored commits under three names, and each
// commit moves to its author name's own mailbox, while everything without a
// safe destination stays put.
func TestUnshareSquashMailboxes(t *testing.T) {
	t.Parallel()
	shared := "154711427+swoboda1337@users.noreply.github.com"
	job := func(name, email string) commitJob {
		return commitJob{Email: email, Name: name, When: time.Now()}
	}
	tests := []struct {
		Name      string
		In        []commitJob
		WantEmail []string
	}{{
		// Test 0: Commits under the shared mailbox move to each author's own
		// address; the owner's own commits stay.
		Name: "tangle repaired",
		In: []commitJob{
			job("Jonathan Swoboda", shared),
			job("J. Nick Varlow", shared),
			job("Kevin Darnell", shared),
			job("J. Nick Varlow", "nick@varlow.org"),
			job("Kevin Darnell", "kevin@lanternhome.org"),
		},
		WantEmail: []string{shared, "nick@varlow.org", "kevin@lanternhome.org", "nick@varlow.org", "kevin@lanternhome.org"},
	}, {
		// Test 1: A noreply mailbox with one name is one person, untouched.
		Name: "own noreply kept",
		In: []commitJob{
			job("Jesse Morrow", "5172840+jessemorrow@users.noreply.github.com"),
			job("Jesse Morrow", "5172840+jessemorrow@users.noreply.github.com"),
		},
		WantEmail: []string{
			"5172840+jessemorrow@users.noreply.github.com",
			"5172840+jessemorrow@users.noreply.github.com",
		},
	}, {
		// Test 2: A person with two own mailboxes gets their most active one.
		Name: "primary wins",
		In: []commitJob{
			job("J. Nick Varlow", shared),
			job("Someone Else", shared),
			job("J. Nick Varlow", "nick@varlow.org"),
			job("J. Nick Varlow", "nick@hearthcore.io"),
			job("J. Nick Varlow", "nick@hearthcore.io"),
		},
		WantEmail: []string{"nick@hearthcore.io", shared,
			"nick@varlow.org", "nick@hearthcore.io", "nick@hearthcore.io"},
	}, {
		// Test 3: A single-word name never moves, and a name with no unique
		// mailbox in the window stays where it was.
		Name: "no safe destination",
		In: []commitJob{
			job("Nick", shared),
			job("Grace Kim", shared),
			job("Someone Else", shared),
		},
		WantEmail: []string{shared, shared, shared},
	}, {
		// Test 4: A real shared corporate mailbox is not a noreply, untouched
		// even with two names on it.
		Name: "real mailbox untouched",
		In: []commitJob{
			job("Ana Ruiz", "team@corp.com"),
			job("Bo Chen", "team@corp.com"),
		},
		WantEmail: []string{"team@corp.com", "team@corp.com"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			got := unshareSquashMailboxes(append([]commitJob(nil), test.In...))
			for i, want := range test.WantEmail {
				if got[i].Email != want {
					t.Errorf("commit %d (%s): email = %q, want %q", i, test.In[i].Name, got[i].Email, want)
				}
			}
		})
	}
}
