package resolve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// nameIndex builds a small company where one person owns a subject and another
// has a surname that looks like a common word, which is the case a loose name
// match would break.
func nameIndex() *index.Index {
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Holly Dunn", Email: "holly.dunn8@corp.com", Title: "People Partner",
			Team: "People", Topics: []string{"vacation"}, Source: "org-csv"},
		{Name: "Bob Kafka", Email: "bob@corp.com", Title: "Analyst",
			Team: "Finance", Topics: []string{"payroll"}, Source: "org-csv"},
		{Name: "Tess Bright", Email: "tess@corp.com", Title: "Staff Engineer",
			Team: "Ledger", Topics: []string{"kafka"}, Source: "org-csv"},
	})
	ix.Canonicalize()
	return ix
}

// TestAskFindsPeopleByName covers the most natural thing to type into something
// that finds people: a colleague's name. Expertise ranking cannot answer it on
// its own, since a name is identity rather than a subject.
func TestAskFindsPeopleByName(t *testing.T) {
	t.Parallel()
	ix := nameIndex()
	resolver := NewKeyword(ix)

	tests := []struct {
		Name      string
		Query     string
		WantFirst string
		WantWhy   string
	}{{ // Test 0: The plain name.
		Name: "plain name", Query: "Holly Dunn", WantFirst: "holly.dunn8@corp.com", WantWhy: "name",
	}, { // Test 1: The name wrapped in the question people actually type.
		Name: "asked as a question", Query: "who is holly dunn", WantFirst: "holly.dunn8@corp.com", WantWhy: "name",
	}, { // Test 2: Misspelled, which is how half-remembered colleagues get typed.
		Name: "misspelled", Query: "holy dun", WantFirst: "holly.dunn8@corp.com", WantWhy: "name",
	}, { // Test 3: By email, which is what gets pasted out of a calendar invite.
		Name: "by email", Query: "holly.dunn8@corp.com", WantFirst: "holly.dunn8@corp.com", WantWhy: "name",
	}, { // Test 4: A subject whose expert shares a surname with nobody.
		Name: "subject still ranks by expertise", Query: "who knows vacation",
		WantFirst: "holly.dunn8@corp.com", WantWhy: "vacation",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ans, err := resolver.Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("resolve %q: %v", test.Query, err)
			}
			if len(ans.People) == 0 {
				t.Fatalf("resolve %q returned nobody", test.Query)
			}
			if got := string(ans.People[0].Person.ID); got != test.WantFirst {
				t.Errorf("resolve %q top = %s, want %s", test.Query, got, test.WantFirst)
			}
			if why := strings.Join(ans.People[0].Reasons, " "); !strings.Contains(why, test.WantWhy) {
				t.Errorf("resolve %q reasons = %v, want to mention %q",
					test.Query, ans.People[0].Reasons, test.WantWhy)
			}
		})
	}
}

// TestNameMatchDoesNotHijackSubjects is the guard on the feature above: a
// surname that looks like a subject must never let its owner answer a question
// about that subject. Bob Kafka knows payroll, and the question is about kafka.
func TestNameMatchDoesNotHijackSubjects(t *testing.T) {
	t.Parallel()
	ix := nameIndex()
	ans, err := NewKeyword(ix).Resolve(context.Background(), "who knows kafka", 5)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ans.People) == 0 {
		t.Fatal("resolve returned nobody")
	}
	if got := string(ans.People[0].Person.ID); got != "tess@corp.com" {
		t.Errorf("top = %s, want the kafka expert tess@corp.com, not the person named Kafka", got)
	}
	// A single word never triggers a name match, so nothing claims to be one.
	for _, m := range ans.People {
		if strings.Contains(strings.Join(m.Reasons, " "), "name") {
			t.Errorf("%s matched by name on a one-word subject query: %v", m.Person.ID, m.Reasons)
		}
	}
}

// TestNameMatchNamesWhatTheyKnow checks the answer to "who is this" says what
// the person is known for, which is what the question was really after.
func TestNameMatchNamesWhatTheyKnow(t *testing.T) {
	t.Parallel()
	ans, err := NewKeyword(nameIndex()).Resolve(context.Background(), "Holly Dunn", 3)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	why := strings.Join(ans.People[0].Reasons, " ")
	if !strings.Contains(why, "knows vacation") {
		t.Errorf("reasons = %v, want them to say what she knows", ans.People[0].Reasons)
	}
}

// TestNameMatchDoesNotLetAMailboxHijackASubject checks that asking about a
// subject returns the people who work on it, even when somebody's address
// happens to be named after it.
//
// Found on a real repository: asking "github" returned the holder of
// github@example.com ahead of every maintainer, because a matching mailbox was
// read as naming that person and named people are placed in front of the ranked
// answer. Addresses called billing, support, admin, and info are everywhere.
func TestNameMatchDoesNotLetAMailboxHijackASubject(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Graph.Topics = map[model.ID]*model.Topic{
		"github": {ID: "github", Name: "github", Curated: true},
	}
	ix.Graph.People = map[model.ID]*model.Person{
		"github@example.com": {
			ID: "github@example.com", Name: "Aaron", Email: "github@example.com",
		},
		"maintainer@example.com": {
			ID: "maintainer@example.com", Name: "Franck", Email: "maintainer@example.com",
			Topics: map[model.ID]float64{"github": 40},
		},
	}
	if got := nameMatch(ix, "github", 5); len(got) != 0 {
		t.Errorf("a mailbox answered a question about a subject: %+v", got)
	}
	// Asking for the person by their address still finds them.
	got := nameMatch(ix, "github@example.com", 5)
	if len(got) != 1 || got[0].Person == nil || got[0].Person.Name != "Aaron" {
		t.Errorf("asking by full address = %+v, want the person who holds it", got)
	}
	// And a mailbox that is not also a subject still names its holder.
	ix.Graph.Topics = map[model.ID]*model.Topic{}
	if got := nameMatch(ix, "github", 5); len(got) != 1 {
		t.Errorf("with no such subject, the mailbox should still name its holder: %+v", got)
	}
}
