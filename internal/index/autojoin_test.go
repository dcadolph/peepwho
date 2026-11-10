package index

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// TestAutoJoin covers the unique-match join, the ambiguity guard, the
// no-match case, and short handles.
func TestAutoJoin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Records       []connector.Record
		WantAmbiguous []string // handle ids left unresolved by a name collision
		WantPeople    int
		WantJoined    int
		WantAlias     string // identity expected after the join; empty skips
		WantOn        string // email of the person carrying the alias
	}{{ // Test 0: A handle joins its unique person by flattened name.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "kim.doe@x.com", Name: "Kim Doe", Source: "t"},
			{Kind: connector.KindPerson, PersonID: "codeowners:kim-doe", Name: "@kim-doe", Source: "t"},
		},
		WantPeople: 1, WantJoined: 1, WantAlias: "codeowners:kim-doe", WantOn: "kim.doe@x.com",
	}, { // Test 1: A handle matching the email local-part joins too.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "kim.doe@x.com", Source: "t"},
			{Kind: connector.KindPerson, PersonID: "github:kimdoe", Name: "@kimdoe", Source: "t"},
		},
		WantPeople: 1, WantJoined: 1, WantAlias: "github:kimdoe", WantOn: "kim.doe@x.com",
	}, { // Test 2: Two candidates with the same flattened name block the join.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "kim.doe@x.com", Name: "Kim Doe", Source: "t"},
			{Kind: connector.KindPerson, Email: "kdoe@y.com", Name: "Kim-Doe", Source: "t"},
			{Kind: connector.KindPerson, PersonID: "codeowners:kim-doe", Name: "@kim-doe", Source: "t"},
		},
		WantPeople: 3, WantJoined: 0, WantAmbiguous: []string{"codeowners:kim-doe"},
	}, { // Test 3: A handle matching nobody stays separate.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "kim.doe@x.com", Name: "Kim Doe", Source: "t"},
			{Kind: connector.KindPerson, PersonID: "github:eve-dev", Name: "@eve-dev", Source: "t"},
		},
		WantPeople: 2, WantJoined: 0,
	}, { // Test 4: A too-short handle never joins.
		Records: []connector.Record{
			{Kind: connector.KindPerson, Email: "al@x.com", Name: "Al", Source: "t"},
			{Kind: connector.KindPerson, PersonID: "github:al", Name: "@al", Source: "t"},
		},
		WantPeople: 2, WantJoined: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build(test.Records)
			res := ix.AutoJoin()
			joined := res.Joined
			ix.Canonicalize()

			if joined != test.WantJoined {
				t.Errorf("joined = %d, want %d", joined, test.WantJoined)
			}
			if !slices.Equal(res.Ambiguous, test.WantAmbiguous) {
				t.Errorf("ambiguous = %v, want %v", res.Ambiguous, test.WantAmbiguous)
			}
			if len(ix.Graph.People) != test.WantPeople {
				t.Errorf("people = %d, want %d: %v",
					len(ix.Graph.People), test.WantPeople, peopleIDs(ix))
			}
			if test.WantAlias == "" {
				return
			}
			var joinedPerson *model.Person
			for _, p := range ix.Graph.People {
				if p.Email == test.WantOn {
					joinedPerson = p
					break
				}
			}
			if joinedPerson == nil {
				t.Fatalf("person %s missing after join: %v", test.WantOn, peopleIDs(ix))
				return
			}
			if !slices.Contains(joinedPerson.Identities, model.ID(test.WantAlias)) {
				t.Errorf("identities = %v, want containing %s", joinedPerson.Identities, test.WantAlias)
			}
		})
	}
}

// peopleIDs lists the graph's person ids for failure messages.
func peopleIDs(ix *Index) []model.ID {
	out := make([]model.ID, 0, len(ix.Graph.People))
	for id := range ix.Graph.People {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// TestAutoJoinCorroboration checks that an ambiguous handle (two people share a
// flattened name) merges into the one it corroborates with via a shared team.
func TestAutoJoinCorroboration(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		{Kind: connector.KindPerson, Email: "alice@x.com", Name: "Alice Smith", Team: "Payments", Source: "t"},
		{Kind: connector.KindPerson, Email: "alice@y.com", Name: "Alice Smith", Team: "Sales", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:alice-smith", Name: "@alice-smith", Team: "Payments", Source: "t"},
	}
	ix := New()
	ix.Build(recs)
	res := ix.AutoJoin()
	ix.Canonicalize()
	if res.Joined != 1 {
		t.Errorf("joined = %d, want 1 (corroborated by shared team)", res.Joined)
	}
	if len(ix.Graph.People) != 2 {
		t.Errorf("people = %d, want 2: %v", len(ix.Graph.People), peopleIDs(ix))
	}
}

// TestAutoJoinAmbiguousNoCorroboration checks that an ambiguous handle with no
// shared signal stays unresolved and reported, never silently merged.
func TestAutoJoinAmbiguousNoCorroboration(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{
		{Kind: connector.KindPerson, Email: "alice@x.com", Name: "Alice Smith", Team: "Payments", Source: "t"},
		{Kind: connector.KindPerson, Email: "alice@y.com", Name: "Alice Smith", Team: "Sales", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:alice-smith", Name: "@alice-smith", Team: "Marketing", Source: "t"},
	}
	ix := New()
	ix.Build(recs)
	res := ix.AutoJoin()
	if res.Joined != 0 {
		t.Errorf("joined = %d, want 0 (no corroboration)", res.Joined)
	}
	if len(res.Ambiguous) != 1 {
		t.Errorf("ambiguous = %v, want exactly the blocked handle", res.Ambiguous)
	}
}

// TestAutoJoinConfidence checks that each inferred join carries a confidence and
// evidence: a unique name match is strong, a team-corroborated ambiguous match
// is weaker, and JoinsFor surfaces the join under the person it merged into.
func TestAutoJoinConfidence(t *testing.T) {
	t.Parallel()

	// A unique name match scores confUniqueName.
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "kim.doe@x.com", Name: "Kim Doe", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:kimdoe", Name: "@kimdoe", Source: "t"},
	})
	res := ix.AutoJoin()
	ix.Canonicalize()
	if len(res.Joins) != 1 {
		t.Fatalf("joins = %d, want 1", len(res.Joins))
	}
	if got := res.Joins[0]; got.Confidence != confUniqueName || got.Reason != "unique name match" {
		t.Errorf("join = %+v, want confidence %v reason %q", got, confUniqueName, "unique name match")
	}
	if fr := ix.JoinsFor("kim.doe@x.com"); len(fr) != 1 || fr[0].Alias != "github:kimdoe" {
		t.Errorf("JoinsFor = %+v, want the github alias", fr)
	}

	// A team-corroborated ambiguous match scores confSharedTeam with team evidence.
	ix2 := New()
	ix2.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "alice@x.com", Name: "Alice Smith", Team: "Payments", Source: "t"},
		{Kind: connector.KindPerson, Email: "alice@y.com", Name: "Alice Smith", Team: "Sales", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:alice-smith", Name: "@alice-smith", Team: "Payments", Source: "t"},
	})
	res2 := ix2.AutoJoin()
	ix2.Canonicalize()
	if len(res2.Joins) != 1 {
		t.Fatalf("joins = %d, want 1", len(res2.Joins))
	}
	if got := res2.Joins[0]; got.Confidence != confSharedTeam || got.Reason != "name and shared team" {
		t.Errorf("join = %+v, want confidence %v reason %q", got, confSharedTeam, "name and shared team")
	}
}

// TestJoinsPersist checks the confidence ledger round-trips through Save and
// Load, and that a re-index keeps a restored join it does not re-infer.
func TestJoinsPersist(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/index.json"

	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "kim.doe@x.com", Name: "Kim Doe", Source: "t"},
		{Kind: connector.KindPerson, PersonID: "github:kimdoe", Name: "@kimdoe", Source: "t"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joins := got.Joins()
	if len(joins) != 1 || joins[0].Alias != "github:kimdoe" || joins[0].Confidence != confUniqueName {
		t.Fatalf("restored joins = %+v, want the github join at %v", joins, confUniqueName)
	}

	// A re-index does not re-infer an already-merged join, but the restored
	// ledger keeps it so the confidence and evidence are not lost.
	got.AutoJoin()
	if jf := got.JoinsFor("kim.doe@x.com"); len(jf) != 1 || jf[0].Confidence != confUniqueName {
		t.Errorf("JoinsFor after re-autojoin = %+v, want the restored join kept", jf)
	}
}

// TestAutoJoinPrunesGhostJoins checks that replacing an index's sources drops a
// join whose people are gone, so no ghost person is reported or re-persisted.
func TestAutoJoinPrunesGhostJoins(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "kim@x.com", Name: "Kim", Source: "s"},
		{Kind: connector.KindPerson, PersonID: "github:kim", Name: "@kim", Source: "s"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if len(ix.Joins()) != 1 {
		t.Fatalf("setup: joins = %d, want 1", len(ix.Joins()))
	}
	// Replace source "s" with an unrelated person; the old join is now a ghost.
	ix.Add([]connector.Record{
		{Kind: connector.KindPerson, Email: "dave@x.com", Name: "Dave", Source: "s"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Joins()); got != 0 {
		t.Errorf("joins after source replace = %d (%+v), want 0 ghosts", got, ix.Joins())
	}
}

// TestAutoJoinEmailVariants checks two email records for one person, whose local
// parts differ only by dots or domain, merge when names agree, and that a shared
// local part with different names stays separate.
func TestAutoJoinEmailVariants(t *testing.T) {
	t.Parallel()

	// Dot and cross-domain variants of the same-named person merge.
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "john.smith@corp.com", Name: "John Smith", Source: "a"},
		{Kind: connector.KindPerson, Email: "johnsmith@corp.onmicrosoft.com", Name: "John Smith", Source: "b"},
	})
	res := ix.AutoJoin()
	ix.Canonicalize()
	if len(ix.Graph.People) != 1 {
		t.Errorf("email variants not merged: %d people %v", len(ix.Graph.People), peopleIDs(ix))
	}
	var variant bool
	for _, j := range res.Joins {
		if j.Reason == "matching email variant" && j.Confidence == confEmailVariant {
			variant = true
		}
	}
	if !variant {
		t.Errorf("no email-variant join recorded: %+v", res.Joins)
	}

	// Same local part, different names: two people, never collapsed.
	ix2 := New()
	ix2.Build([]connector.Record{
		{Kind: connector.KindPerson, Email: "jsmith@corp.com", Name: "John Smith", Source: "a"},
		{Kind: connector.KindPerson, Email: "jsmith@partner.com", Name: "Jane Smith", Source: "b"},
	})
	ix2.AutoJoin()
	ix2.Canonicalize()
	if len(ix2.Graph.People) != 2 {
		t.Errorf("different-named people wrongly merged: %d people %v", len(ix2.Graph.People), peopleIDs(ix2))
	}
}

// TestAutoJoinLinksHandleToItsOwnDomain checks a handle nobody's name matches is
// still joined to the person whose address sits on a domain of the same name.
// This is the case that leaves declared owners looking like they do no work:
// ownership is written as a handle, the work arrives as an address, and neither
// the display name nor the mailbox connects them.
func TestAutoJoinLinksHandleToItsOwnDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name     string
		People   map[model.ID]*model.Person
		WantJoin bool
		WantTo   model.ID
	}{{ // Test 0: A personal domain names exactly one person.
		People: map[model.ID]*model.Person{
			"github:pat": {ID: "github:pat", Name: "pat"},
			"git@pat.dev": {
				ID: "git@pat.dev", Name: "Pat Example", Email: "git@pat.dev",
			},
		},
		WantJoin: true, WantTo: "git@pat.dev",
	}, { // Test 1: A public provider is shared, so it points at nobody.
		People: map[model.ID]*model.Person{
			"github:gmail": {ID: "github:gmail", Name: "gmail"},
			"a@gmail.com":  {ID: "a@gmail.com", Name: "Ada Lovelace", Email: "a@gmail.com"},
			"b@gmail.com":  {ID: "b@gmail.com", Name: "Bo Diddley", Email: "b@gmail.com"},
		},
		WantJoin: false,
	}, { // Test 2: No domain of that name, so nothing is claimed.
		People: map[model.ID]*model.Person{
			"github:nobody": {ID: "github:nobody", Name: "nobody"},
			"a@example.com": {ID: "a@example.com", Name: "Ada Lovelace", Email: "a@example.com"},
		},
		WantJoin: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Graph.People = test.People
			res := ix.AutoJoin()

			var got *Join
			for i, j := range res.Joins {
				if strings.HasPrefix(string(j.Alias), "github:") {
					got = &res.Joins[i]
				}
			}
			if !test.WantJoin {
				if got != nil {
					t.Fatalf("joined %s to %s on %q, want no join",
						got.Alias, got.Canonical, got.Reason)
				}
				return
			}
			if got == nil {
				t.Fatal("the handle was left unlinked, though one address sits on its domain")
				return
			}
			if got.Canonical != test.WantTo {
				t.Errorf("joined to %s, want %s", got.Canonical, test.WantTo)
			}
			if got.Reason != "handle matches email domain" {
				t.Errorf("reason = %q, want it to name the evidence", got.Reason)
			}
		})
	}
}

// TestAutoJoinLinksShortenedHandles checks a handle that is a person's name with
// the end cut off, or with something stuck on it, is joined to that person, and
// that the guards which keep it from claiming the wrong person hold.
func TestAutoJoinLinksShortenedHandles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name     string
		People   map[model.ID]*model.Person
		WantJoin bool
		WantTo   model.ID
	}{{ // Test 0: The handle is the name cut short.
		People: map[model.ID]*model.Person{
			"github:rubenhal": {ID: "github:rubenhal", Name: "rubenhal"},
			"m@x.com":         {ID: "m@x.com", Name: "Ruben Halvorsen", Email: "m@x.com"},
		},
		WantJoin: true, WantTo: "m@x.com",
	}, { // Test 1: The handle is the name with a suffix stuck on it.
		People: map[model.ID]*model.Person{
			"github:gmarchetti-st": {ID: "github:gmarchetti-st", Name: "gmarchetti-st"},
			"g@x.com":              {ID: "g@x.com", Name: "G Marchetti", Email: "g@x.com"},
		},
		WantJoin: true, WantTo: "g@x.com",
	}, { // Test 2: A lone given name claims nobody, however well it matches.
		People: map[model.ID]*model.Person{
			"github:michaelquorn": {ID: "github:michaelquorn", Name: "michaelquorn"},
			"m@x.com":             {ID: "m@x.com", Name: "Michael", Email: "m@x.com"},
		},
		WantJoin: false,
	}, { // Test 3: Too little agreement to be more than a coincidence.
		People: map[model.ID]*model.Person{
			"github:andrew-lumenfox": {ID: "github:andrew-lumenfox", Name: "andrew-lumenfox"},
			"a@x.com":                {ID: "a@x.com", Name: "Andre W.", Email: "a@x.com"},
		},
		WantJoin: false,
	}, { // Test 4: Two people it could be is nobody it is.
		People: map[model.ID]*model.Person{
			"github:jonathanre": {ID: "github:jonathanre", Name: "jonathanre"},
			"a@x.com":           {ID: "a@x.com", Name: "Jonathan Renfrew", Email: "a@x.com"},
			"b@x.com":           {ID: "b@x.com", Name: "Jonathan Reyes", Email: "b@x.com"},
		},
		WantJoin: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Graph.People = test.People
			res := ix.AutoJoin()

			var got *Join
			for i, j := range res.Joins {
				if strings.HasPrefix(string(j.Alias), "github:") {
					got = &res.Joins[i]
				}
			}
			if !test.WantJoin {
				if got != nil {
					t.Fatalf("joined %s to %s on %q, want no join",
						got.Alias, got.Canonical, got.Reason)
				}
				return
			}
			if got == nil {
				t.Fatal("the handle was left unlinked, though it is that person's name shortened")
				return
			}
			if got.Canonical != test.WantTo {
				t.Errorf("joined to %s, want %s", got.Canonical, test.WantTo)
			}
		})
	}
}

// TestSameSurnameJoinsOnePersonWhoseGivenNameDiffers covers one maintainer whose
// two records disagree about their given name: a commit signed one way and a
// CODEOWNERS entry resolving to another. Nothing else links them, and on
// prometheus/prometheus that split put an area its owner had committed to
// fifty-three times down as having moved to somebody else.
func TestSameSurnameJoinsOnePersonWhoseGivenNameDiffers(t *testing.T) {
	t.Parallel()

	subjects := []string{"remote-write", "series-churn", "head-compaction", "block-storage"}
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "György Szentmiklos", Email: "gyorgy.k@corp.com",
			Topics: subjects, Source: "git"},
		{Kind: connector.KindPerson, Name: "George Szentmiklos", Email: "szentrama@users.noreply.github.com",
			Topics: subjects, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 1 {
		names := make([]string, 0, got)
		for _, p := range ix.Graph.People {
			names = append(names, p.Name)
		}
		t.Errorf("one maintainer resolves to %d people (%v), want 1", got, names)
	}
}

// TestSameSurnameKeepsTwoColleaguesApart is the guard that matters, and the one
// shared subjects cannot provide on its own. Two people who share a surname AND
// a team share plenty of subjects precisely because they work together, so
// subject overlap argues for merging the very pair that must never be merged.
// Two real mailboxes are two people.
func TestSameSurnameKeepsTwoColleaguesApart(t *testing.T) {
	t.Parallel()

	subjects := []string{"remote-write", "series-churn", "head-compaction", "block-storage"}
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Dan Hrabek", Email: "dan.hrabek@corp.com",
			Topics: subjects, Source: "git"},
		{Kind: connector.KindPerson, Name: "Jan Hrabek", Email: "jan.hrabek@corp.com",
			Topics: subjects, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("two colleagues sharing a surname and a team merged into %d; "+
			"they have two real addresses and are two people", got)
	}
}

// TestSameSurnameNeedsSharedSubjects checks a surname alone never merges anyone.
func TestSameSurnameNeedsSharedSubjects(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Miko Thalberg", Email: "miko@corp.com",
			Topics: []string{"billing-retries", "payment-gateway"}, Source: "git"},
		{Kind: connector.KindPerson, Name: "Eric Thalberg", Email: "eric@users.noreply.github.com",
			Topics: []string{"search-indexing", "query-planner"}, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("two people sharing only a surname merged into %d", got)
	}
}

// TestSameLocalJoinsOnePersonAcrossTwoMailboxes covers the split the handle
// join is right to refuse: one maintainer writing from dtwarnok@gmail.com and
// dtwarnok@google.com matches the handle from BOTH, so the ambiguity guard
// blocks it, and the work stays halved. The mailboxes themselves are the
// evidence: a distinctive local shared across domains, by two records whose
// names could not have joined them.
func TestSameLocalJoinsOnePersonAcrossTwoMailboxes(t *testing.T) {
	t.Parallel()
	subjects := []string{"remote-write", "block-storage", "head-compaction"}
	ix := New()
	var recs []connector.Record
	for _, email := range []string{"dtwarnok@gmail.com", "dtwarnok@google.com"} {
		name := "dtwarnok"
		if email == "dtwarnok@google.com" {
			name = "Darek Twarnok"
		}
		recs = append(recs, connector.Record{Kind: connector.KindPerson,
			Name: name, Email: email, Topics: subjects, Source: "git"})
	}
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 1 {
		t.Errorf("one maintainer resolves to %d people, want 1", got)
	}
}

// TestSameLocalKeepsTwoMichaelsApart is the collision the rule must refuse:
// a bare given name as a local is what half the Michaels at any company use,
// and both records displaying the same single word means the local carries
// nothing the (rightly distrusted) bare name did not.
func TestSameLocalKeepsTwoMichaelsApart(t *testing.T) {
	t.Parallel()
	subjects := []string{"billing", "ledger", "invoices"}
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Michael", Email: "michael@a.com", Topics: subjects, Source: "git"},
		{Kind: connector.KindPerson, Name: "Michael", Email: "michael@b.com", Topics: subjects, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("two Michaels merged into %d; a bare given name joins nobody", got)
	}
}

// TestSameLocalKeepsTwoAndrewsApart is the collision measured on esphome: two
// full names sharing a first name, that first name as the local at two
// domains, and enough shared subjects to pass the corroboration bar. The
// disagreeing surnames are affirmative evidence of two people, so the local
// must count for nothing here.
func TestSameLocalKeepsTwoAndrewsApart(t *testing.T) {
	t.Parallel()
	subjects := []string{"uart-driver", "modbus", "sensor-core"}
	ix := New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Andrew Pellow", Email: "andrew@northfen.example", Topics: subjects, Source: "git"},
		{Kind: connector.KindPerson, Name: "Andrew Corrin", Email: "andrew@quillcorp.example", Topics: subjects, Source: "git"},
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("two Andrews merged into %d; a shared given name joins nobody", got)
	}
}

// TestHandleJoinsOnSurnameForms covers the three surname shapes a handle
// takes, measured on grafana: an initial plus the surname, the bare surname,
// and a surname fragment carrying the organization's own name. Each must land
// on exactly one person; a surname two people share joins neither.
func TestHandleJoinsOnSurnameForms(t *testing.T) {
	t.Parallel()
	subjects := []string{"dashboards", "panels"}
	build := func(recs ...connector.Record) *Index {
		ix := New()
		ix.Build(recs)
		ix.AutoJoin()
		ix.Canonicalize()
		return ix
	}
	person := func(name, email string) connector.Record {
		return connector.Record{Kind: connector.KindPerson, Name: name, Email: email,
			Topics: subjects, Source: "git"}
	}
	handle := func(h string) connector.Record {
		return connector.Record{Kind: connector.KindPerson, Name: "@" + h,
			PersonID: "codeowners:" + h, Topics: subjects, Source: "codeowners", Weight: 1}
	}

	// Test 0: Initial plus surname.
	ix := build(person("Timur Bekmuratov", "timur@x.com"), handle("tbekmuratov"))
	if got := len(ix.Graph.People); got != 1 {
		t.Errorf("initial+surname resolves to %d people, want 1", got)
	}

	// Test 1: A surname fragment with the organization's name, learned from a
	// team handle in the same file.
	ix = build(person("Anna Zelarrondo", "anna@x.com"),
		handle("zelar-grafana"), handle("grafana/docs-squad"))
	joined := false
	for _, j := range ix.Joins() {
		if string(j.Alias) == "codeowners:zelar-grafana" {
			joined = true
		}
	}
	if !joined {
		t.Errorf("zelar-grafana did not join Anna Zelarrondo: %+v", ix.Joins())
	}

	// Test 2: A surname held by two people joins neither.
	ix = build(person("Ana Silva", "ana@x.com"), person("Bo Silva", "bo@x.com"),
		handle("silva"))
	ids := 0
	for range ix.Graph.People {
		ids++
	}
	if ids != 3 {
		t.Errorf("shared surname collapsed to %d people, want 3 kept apart", ids)
	}

	// Test 3: A short surname is not distinctive and joins nobody.
	ix = build(person("Kim Doe", "kim@x.com"), handle("kdoe"))
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("short surname joined anyway: %d people, want 2", got)
	}
}

// TestSameSurnameRefusesCommonAndCrowdedSurnames pins the two ways "exactly
// two records" lied on grafana: a surname on the common list joins nobody
// however few hold it, and a one-word name ending in the surname counts as a
// third holder, so Arjun and Aditya Singh stay two people even while
// RaghavSingh hides from the word split.
func TestSameSurnameRefusesCommonAndCrowdedSurnames(t *testing.T) {
	t.Parallel()
	subjects := []string{"dashboards", "alerting", "panels"}
	person := func(name, email string) connector.Record {
		return connector.Record{Kind: connector.KindPerson, Name: name, Email: email,
			Topics: subjects, Source: "git"}
	}

	// Test 0: The common list blocks even a clean-looking pair.
	ix := New()
	ix.Build([]connector.Record{
		person("Arjun Singh", "48213907+arjunsin@users.noreply.github.com"),
		person("Aditya Singh", "adityacodes07@gmail.com"),
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 2 {
		t.Errorf("two Singhs merged into %d, want 2 kept apart", got)
	}

	// Test 1: A crowded rare surname is blocked by the holder count: the
	// one-word third holder makes it three, not two.
	ix = New()
	ix.Build([]connector.Record{
		person("Ana Szentmiklos", "1+anak@users.noreply.github.com"),
		person("Bo Szentmiklos", "bo@x.com"),
		person("CelSzentmiklos", "cel@x.com"),
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 3 {
		t.Errorf("crowded surname merged into %d, want 3 kept apart", got)
	}

	// Test 2: The motivating merge still works: a rare surname held by exactly
	// two records, one behind a private address.
	ix = New()
	ix.Build([]connector.Record{
		person("Gyorgy Szentmiklos", "5+gyk@users.noreply.github.com"),
		person("George Szentmiklos", "george@x.com"),
	})
	ix.AutoJoin()
	ix.Canonicalize()
	if got := len(ix.Graph.People); got != 1 {
		t.Errorf("the one maintainer resolves to %d people, want 1", got)
	}
}
