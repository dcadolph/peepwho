package index

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
)

// handleRec builds a handle-only person record such as github:kim-doe.
func handleRec(id, team string) connector.Record { //nolint:unparam // Team keeps the shared record shape.
	return connector.Record{
		Kind: connector.KindPerson, PersonID: id, Name: "@" + handlePart(model.ID(id)),
		Team: team, Source: "t",
	}
}

// emailRec builds a person record keyed by email.
func emailRec(email, name, team string) connector.Record { //nolint:unparam // Team keeps the shared record shape.
	return connector.Record{
		Kind: connector.KindPerson, Email: email, Name: name, Team: team, Source: "t",
	}
}

// topicRec builds a person record with subjects, so rules that want shared
// work can be fed.
func topicRec(email, name string, topics ...string) connector.Record {
	return connector.Record{
		Kind: connector.KindPerson, Email: email, Name: name, Topics: topics, Source: "git",
	}
}

// describeJoinsConf renders joins as sorted "alias>canonical: reason @conf" lines.
func describeJoinsConf(joins []Join) []string {
	out := make([]string, 0, len(joins))
	for _, j := range joins {
		out = append(out, fmt.Sprintf("%s>%s: %s @%.2f", j.Alias, j.Canonical, j.Reason, j.Confidence))
	}
	slices.Sort(out)
	return out
}

// TestAutoJoinPassOrder pins the order of the AutoJoin passes. Each case holds
// an input on which two adjacent passes disagree, so swapping that pair fails
// exactly that case; pinning every adjacent pair pins the whole order.
func TestAutoJoinPassOrder(t *testing.T) {
	t.Parallel()
	work := []string{"remote-write", "series-churn", "head-compaction"}
	tests := []struct {
		Records    []connector.Record
		WantJoins  []string
		WantPeople int
	}{{ // Test 0: Whole name beats address domain for github:pat.
		Records: []connector.Record{
			emailRec("pat@y.com", "Pat", ""),
			emailRec("git@pat.dev", "Quinn Oak", ""),
			handleRec("github:pat", ""),
		},
		WantJoins:  []string{"github:pat>pat@y.com: unique name match @0.90"},
		WantPeople: 2,
	}, { // Test 1: Address domain beats shortened name for github:rubenhalvo.
		Records: []connector.Record{
			emailRec("git@rubenhalvo.dev", "Quinn Oak", ""),
			emailRec("rh@x.com", "Ruben Halvorsen", ""),
			handleRec("github:rubenhalvo", ""),
		},
		WantJoins:  []string{"github:rubenhalvo>git@rubenhalvo.dev: handle matches email domain @0.85"},
		WantPeople: 2,
	}, { // Test 2: Shortened name beats surname for github:kristiansen.
		Records: []connector.Record{
			emailRec("ko@x.com", "Kristiansen Olav", ""),
			emailRec("mia@x.com", "Mia Kristiansen", ""),
			handleRec("github:kristiansen", ""),
		},
		WantJoins:  []string{"github:kristiansen>ko@x.com: handle is a shortened name @0.80"},
		WantPeople: 2,
	}, { // Test 3: Surname runs before the email variant merge, so two records refuse the handle.
		Records: []connector.Record{
			emailRec("anna.zelarrondo@x.com", "Anna Zelarrondo", ""),
			emailRec("annazelarrondo@y.com", "Anna Zelarrondo", ""),
			handleRec("github:zelarrondo", ""),
		},
		WantJoins: []string{
			"annazelarrondo@y.com>anna.zelarrondo@x.com: matching email variant @0.85",
		},
		WantPeople: 2,
	}, { // Test 4: Email variant records the merge before same name does.
		Records: []connector.Record{
			topicRec("john.quill@x.com", "John Quill", work...),
			topicRec("johnquill@y.com", "John Quill", work...),
		},
		WantJoins:  []string{"johnquill@y.com>john.quill@x.com: matching email variant @0.85"},
		WantPeople: 1,
	}, { // Test 5: Same name records the merge before same surname does.
		Records: []connector.Record{
			topicRec("george.sz@corp.com", "George Szentmiklos", work...),
			topicRec("123+gsz@users.noreply.github.com", "George Szentmiklos", work...),
		},
		WantJoins: []string{
			"gsz@users.noreply.github.com>george.sz@corp.com: same name and shared subjects @0.65",
		},
		WantPeople: 1,
	}, { // Test 6: Same surname runs before same local, so three surname holders stay three.
		Records: []connector.Record{
			topicRec("123+gyo@users.noreply.github.com", "Gyorgy Szentmiklos", work...),
			topicRec("gszent@x.com", "George Szentmiklos", work...),
			topicRec("gszent@y.com", "Georg Szentmiklos", work...),
		},
		WantJoins: []string{
			"gszent@y.com>gszent@x.com: same distinctive mailbox name at two domains, " +
				"and shared subjects @0.65",
		},
		WantPeople: 2,
	}, { // Test 7: Same local runs before login name, so the mailbox pair stands on its own work.
		Records: []connector.Record{
			topicRec("123+dtwarnok@users.noreply.github.com", "Darek Twarnok", "alpha-one"),
			topicRec("dtwarnok@zmail.com", "dtwarnok", work...),
			topicRec("dtwarnok@zoogle.com", "Darek Twarnok", work...),
		},
		WantJoins: []string{
			"dtwarnok@zmail.com>dtwarnok@users.noreply.github.com: " + loginNameReason + " @0.90",
			"dtwarnok@zoogle.com>dtwarnok@zmail.com: same distinctive mailbox name at two domains, " +
				"and shared subjects @0.65",
		},
		WantPeople: 1,
	}, { // Test 8: Login name records the merge before same-name mailbox does.
		Records: []connector.Record{
			emailRec("123+fancivez@users.noreply.github.com", "fancivez", ""),
			emailRec("fancive@x.com", "fancivez", ""),
		},
		WantJoins: []string{
			"fancivez@users.noreply.github.com>fancive@x.com: " + loginNameReason + " @0.90",
		},
		WantPeople: 1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build(test.Records)
			res := ix.AutoJoin()
			ix.Canonicalize()
			got := describeJoinsConf(res.Joins)
			if diff := cmp.Diff(test.WantJoins, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("joins mismatch (-want +got):\n%s", diff)
			}
			if got := len(ix.Graph.People); got != test.WantPeople {
				t.Errorf("people = %d, want %d: %v", got, test.WantPeople, peopleIDs(ix))
			}
		})
	}
}
