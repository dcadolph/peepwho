package index

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// newTestJoinRun builds an index from recs and returns a join run over it with
// the scaffolding marked, as AutoJoin does before its passes.
func newTestJoinRun(t *testing.T, recs []connector.Record) *joinRun {
	t.Helper()
	ix := New()
	ix.Build(recs)
	markUbiquitous(ix.Graph)
	return newJoinRun(ix.Graph, ix.identityResolver())
}

// describeJoins renders joins as sorted "alias>canonical: reason" lines.
func describeJoins(joins []Join) []string {
	out := make([]string, 0, len(joins))
	for _, j := range joins {
		out = append(out, fmt.Sprintf("%s>%s: %s", j.Alias, j.Canonical, j.Reason))
	}
	slices.Sort(out)
	return out
}

// joinPassCase is one table case for a pass that joins handle-only people.
type joinPassCase struct {
	// Records are the records the index is built from.
	Records []connector.Record
	// Settled are handles marked settled before the pass runs.
	Settled []model.ID
	// WantJoins are the joins the pass makes, as describeJoins renders them.
	WantJoins []string
	// WantBlocked are the handles the pass leaves ambiguous.
	WantBlocked []string
}

// runJoinPassCases runs each case through the pass that pass selects on a
// fresh run and compares the joins and blocked handles it leaves.
func runJoinPassCases(t *testing.T, tests []joinPassCase, pass func(*joinRun) func()) {
	t.Helper()
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := newTestJoinRun(t, test.Records)
			for _, id := range test.Settled {
				run.settled[id] = true
			}
			pass(run)()
			got := describeJoins(run.joins)
			if diff := cmp.Diff(test.WantJoins, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("joins mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantBlocked, run.blocked, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("blocked mismatch (-want +got):\n%s", diff)
			}
			for _, j := range run.joins {
				if !run.settled[j.Alias] {
					t.Errorf("joined handle %s was not marked settled", j.Alias)
				}
			}
		})
	}
}

// TestWholeNameJoins covers the whole-name pass: a unique match joins, an
// ambiguous one joins only when exactly one candidate corroborates, and a short
// handle never joins.
func TestWholeNameJoins(t *testing.T) {
	t.Parallel()
	tests := []joinPassCase{{ // Test 0: A handle joins the one person of that name.
		Records: []connector.Record{
			emailRec("kim.doe@x.com", "Kim Doe", ""),
			handleRec("codeowners:kim-doe", ""),
		},
		WantJoins: []string{"codeowners:kim-doe>kim.doe@x.com: unique name match"},
	}, { // Test 1: Two people of that name and no corroboration block the handle.
		Records: []connector.Record{
			emailRec("alice@x.com", "Alice Smith", "Payments"),
			emailRec("alice@y.com", "Alice Smith", "Sales"),
			handleRec("github:alice-smith", "Marketing"),
		},
		WantBlocked: []string{"github:alice-smith"},
	}, { // Test 2: A shared team picks one of two people of that name.
		Records: []connector.Record{
			emailRec("alice@x.com", "Alice Smith", "Payments"),
			emailRec("alice@y.com", "Alice Smith", "Sales"),
			handleRec("github:alice-smith", "Payments"),
		},
		WantJoins: []string{"github:alice-smith>alice@x.com: name and shared team"},
	}, { // Test 3: A handle shorter than minHandleLen never joins.
		Records: []connector.Record{
			emailRec("al@x.com", "Al", ""),
			handleRec("github:al", ""),
		},
	}}
	runJoinPassCases(t, tests, func(r *joinRun) func() { return r.wholeNameJoins })
}

// TestDomainJoins covers the address-domain pass: a handle spelled like the
// domain of exactly one person's address joins them, and a domain shared by
// several people or a settled handle does not.
func TestDomainJoins(t *testing.T) {
	t.Parallel()
	tests := []joinPassCase{{ // Test 0: The one person on pat.dev owns the handle pat.
		Records: []connector.Record{
			emailRec("git@pat.dev", "Pat Quill", ""),
			handleRec("github:pat", ""),
		},
		WantJoins: []string{"github:pat>git@pat.dev: handle matches email domain"},
	}, { // Test 1: A domain two people share points at nobody.
		Records: []connector.Record{
			emailRec("ann@acme.com", "Ann Ode", ""),
			emailRec("bob@acme.com", "Bob Ode", ""),
			handleRec("github:acme", ""),
		},
	}, { // Test 2: A handle an earlier pass settled is left alone.
		Records: []connector.Record{
			emailRec("git@pat.dev", "Pat Quill", ""),
			handleRec("github:pat", ""),
		},
		Settled: []model.ID{"github:pat"},
	}}
	runJoinPassCases(t, tests, func(r *joinRun) func() { return r.domainJoins })
}

// TestShortenedNameJoins covers the truncated or padded handle pass: a handle
// that starts a full name, or that a full name starts, joins when it agrees on
// minTruncation characters with exactly one person.
func TestShortenedNameJoins(t *testing.T) {
	t.Parallel()
	tests := []joinPassCase{{ // Test 0: A handle cut short of the full name joins.
		Records: []connector.Record{
			emailRec("ruben@x.com", "Ruben Halvorsen", ""),
			handleRec("github:rubenhal", ""),
		},
		WantJoins: []string{"github:rubenhal>ruben@x.com: handle is a shortened name"},
	}, { // Test 1: A handle padded past the full name joins.
		Records: []connector.Record{
			emailRec("ruben@x.com", "Ruben Halvorsen", ""),
			handleRec("github:rubenhalvorsen-st", ""),
		},
		WantJoins: []string{"github:rubenhalvorsen-st>ruben@x.com: handle is a shortened name"},
	}, { // Test 2: A handle starting two full names is refused.
		Records: []connector.Record{
			emailRec("ruben@x.com", "Ruben Halvorsen", ""),
			emailRec("rh@y.com", "Ruben Halvorsenko", ""),
			handleRec("github:rubenhal", ""),
		},
	}, { // Test 3: A lone given name never claims a handle.
		Records: []connector.Record{
			emailRec("m@x.com", "Michaelangelo", ""),
			handleRec("github:michaelangelo-dev", ""),
		},
	}, { // Test 4: Fewer than minTruncation characters of agreement is refused.
		Records: []connector.Record{
			emailRec("ruben@x.com", "Ruben Halvorsen", ""),
			handleRec("github:rubenha", ""),
		},
	}}
	runJoinPassCases(t, tests, func(r *joinRun) func() { return r.shortenedNameJoins })
}

// TestSurnameHandleJoins covers the surname pass: an initial and surname, a bare
// surname, and a surname fragment carrying the organization's name each join,
// while a bare fragment, a surname two people hold, and a common surname do not.
func TestSurnameHandleJoins(t *testing.T) {
	t.Parallel()
	tests := []joinPassCase{{ // Test 0: An initial and surname joins.
		Records: []connector.Record{
			emailRec("timur@x.com", "Timur Bekmuratov", ""),
			handleRec("github:tbekmuratov", ""),
		},
		WantJoins: []string{"github:tbekmuratov>timur@x.com: handle is an initial and surname"},
	}, { // Test 1: A bare surname joins.
		Records: []connector.Record{
			emailRec("timur@x.com", "Timur Bekmuratov", ""),
			handleRec("github:bekmuratov", ""),
		},
		WantJoins: []string{"github:bekmuratov>timur@x.com: handle is a surname"},
	}, { // Test 2: A surname fragment with the organization's name joins.
		Records: []connector.Record{
			emailRec("anna@x.com", "Anna Zelarrondo", ""),
			handleRec("codeowners:grafana/alerting-squad", ""),
			handleRec("github:zelar-grafana", ""),
		},
		WantJoins: []string{
			"github:zelar-grafana>anna@x.com: handle is a surname fragment and the organization's name",
		},
	}, { // Test 3: A bare surname fragment is too loose to join.
		Records: []connector.Record{
			emailRec("anna@x.com", "Anna Zelarrondo", ""),
			handleRec("github:zelar", ""),
		},
	}, { // Test 4: A surname two people hold is refused.
		Records: []connector.Record{
			emailRec("timur@x.com", "Timur Bekmuratov", ""),
			emailRec("aliya@y.com", "Aliya Bekmuratov", ""),
			handleRec("github:bekmuratov", ""),
		},
	}, { // Test 5: A common surname is never distinctive.
		Records: []connector.Record{
			emailRec("john@x.com", "John Smith", ""),
			handleRec("github:jsmith", ""),
		},
	}}
	runJoinPassCases(t, tests, func(r *joinRun) func() { return r.surnameHandleJoins })
}

// TestOrgTokens covers learning the organization's name from team handles.
func TestOrgTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Records    []connector.Record
		WantTokens map[string]bool
	}{{ // Test 0: A team handle names its organization.
		Records:    []connector.Record{handleRec("codeowners:grafana/alerting-squad", "")},
		WantTokens: map[string]bool{"grafana": true},
	}, { // Test 1: A personal handle and a too-short organization name yield nothing.
		Records: []connector.Record{
			handleRec("github:kim-doe", ""),
			handleRec("codeowners:ab/core", ""),
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := newTestJoinRun(t, test.Records)
			if diff := cmp.Diff(test.WantTokens, orgTokens(run.g), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("tokens mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestHandleForms covers stripping the organization's name from a handle.
func TestHandleForms(t *testing.T) {
	t.Parallel()
	orgs := map[string]bool{"grafana": true}
	tests := []struct {
		Key       string
		WantForms []string
	}{{ // Test 0: The organization's name is cut from the end.
		Key: "zelargrafana", WantForms: []string{"zelargrafana", "zelar"},
	}, { // Test 1: The organization's name is cut from the start.
		Key: "grafanazelar", WantForms: []string{"grafanazelar", "zelar"},
	}, { // Test 2: What is left shorter than minSurname is refused.
		Key: "abgrafana", WantForms: []string{"abgrafana"},
	}, { // Test 3: A handle without the organization's name stays whole.
		Key: "zelarrondo", WantForms: []string{"zelarrondo"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantForms, handleForms(test.Key, orgs)); diff != "" {
				t.Errorf("forms mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestEmailVariantJoins covers the email-variant pass: two addresses whose
// dotless local part is the shared full name merge, and a differing name, a
// one-word name, or a local part that is not the name do not.
func TestEmailVariantJoins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Records   []connector.Record
		WantPairs []string
	}{{ // Test 0: A dotted and an undotted mailbox of one full name merge.
		Records: []connector.Record{
			emailRec("john.quill@x.com", "John Quill", ""),
			emailRec("johnquill@y.com", "John Quill", ""),
		},
		WantPairs: []string{"john.quill@x.com|johnquill@y.com: matching email variant"},
	}, { // Test 1: Different names under one local part stay apart.
		Records: []connector.Record{
			emailRec("john.quill@x.com", "John Quill", ""),
			emailRec("johnquill@y.com", "Johnny Quill", ""),
		},
	}, { // Test 2: A one-word name is a bare first name and stays apart.
		Records: []connector.Record{
			emailRec("michael@x.com", "Michael", ""),
			emailRec("michael@y.com", "Michael", ""),
		},
	}, { // Test 3: A local part that is not the full name stays apart.
		Records: []connector.Record{
			emailRec("jquill@x.com", "John Quill", ""),
			emailRec("j.quill@y.com", "John Quill", ""),
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := newTestJoinRun(t, test.Records)
			run.emailVariantJoins()
			got := make([]string, 0, len(run.joins))
			for _, j := range run.joins {
				pair := []string{string(j.Alias), string(j.Canonical)}
				slices.Sort(pair)
				got = append(got, fmt.Sprintf("%s|%s: %s", pair[0], pair[1], j.Reason))
			}
			if diff := cmp.Diff(test.WantPairs, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("joins mismatch (-want +got):\n%s", diff)
			}
			if len(run.joins) == 1 && run.r.Canonical(run.joins[0].Alias) != run.joins[0].Canonical {
				t.Errorf("alias %s does not resolve to %s", run.joins[0].Alias, run.joins[0].Canonical)
			}
		})
	}
}
