package index

import (
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestSameNameMailboxJoins covers the split the same-name rule misses. That
// rule wants three shared subjects, which an occasional contributor never
// reaches, so somebody who lands a little work from each of two addresses
// stays two people. Each case here is a real shape taken from public history.
func TestSameNameMailboxJoins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name  string
		A     connector.Record
		B     connector.Record
		Want  int
		Shape string
	}{{ // Test 0: One mailbox continues the other past a suffix.
		A:    person("Fancivez", "fancive@example.com", "storage"),
		B:    person("Fancivez", "fancivez@example.com", "docs"),
		Want: 1, Shape: "a suffix on the same mailbox",
	}, { // Test 1: The difference is only digits, and a noreply account number
		// must not be mistaken for part of the name.
		A:    person("Anas Khan", "83116240+anxkhn@users.noreply.github.com", "storage"),
		B:    person("Anas Khan", "anxkhn28@example.com", "docs"),
		Want: 1, Shape: "digits on the same mailbox",
	}, { // Test 2: Both mailboxes spell the whole name, in either order.
		A:    person("Rushabh Mehta", "139112780+rushabhmehta2005@users.noreply.github.com", "storage"),
		B:    person("Rushabh Mehta", "mehtarushabh2005@example.com", "docs"),
		Want: 1, Shape: "the whole name both ways round",
	}, { // Test 3: A shortened given name in front of the surname.
		A:    person("Solomon Jacobs", "solojacobs@example.com", "storage"),
		B:    person("Solomon Jacobs", "solomonjacobs@example.net", "docs"),
		Want: 1, Shape: "a shortened given name",
	}, { // Test 4: Nothing but the name agrees, which is the case that must
		// never merge: the mailboxes spell two different things.
		A:    person("Pavel Rysnik", "126406830+sakuuj@users.noreply.github.com", "storage"),
		B:    person("Pavel Rysnik", "pavelrysnik@example.com", "docs"),
		Want: 2, Shape: "unrelated mailboxes",
	}, { // Test 5: A company that issues name-based mailboxes tells two
		// colleagues of the same name apart with a number. Ignoring those
		// digits merged 351 pairs of distinct people in a simulated org.
		A:    person("Ana Dubois", "ana.dubois380@corp.com", "storage"),
		B:    person("Ana Dubois", "ana.dubois764@corp.com", "docs"),
		Want: 2, Shape: "the same spelling told apart by a number",
	}, { // Test 6: Identical locals at two domains are two people sharing a
		// common given name until something better says otherwise.
		A:    person("Michael", "michael@example.com", "storage"),
		B:    person("Michael", "michael@example.net", "docs"),
		Want: 2, Shape: "the same common local at two domains",
	}, { // Test 7: A one-letter abbreviation would claim every Smith whose
		// given name starts with an a.
		A:    person("Alex Smith", "asmith@example.com", "storage"),
		B:    person("Alex Smith", "alexsmith@example.net", "docs"),
		Want: 2, Shape: "too short an abbreviation",
	}, { // Test 8: A local that is merely the given name identifies a first
		// name, not a person.
		A:    person("Andrew Pellow", "andrew@example.com", "storage"),
		B:    person("Andrew Pellow", "andrewpellow@example.net", "docs"),
		Want: 2, Shape: "a bare given name",
	}}

	for testNum, test := range tests {
		t.Run(test.Shape, func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build([]connector.Record{test.A, test.B})
			ix.AutoJoin()
			ix.Canonicalize()
			if got := len(ix.Graph.People); got != test.Want {
				for id, p := range ix.Graph.People {
					t.Logf("person %s (%s)", id, p.Name)
				}
				t.Errorf("test %d (%s): people = %d, want %d",
					testNum, test.Shape, got, test.Want)
			}
		})
	}
}
