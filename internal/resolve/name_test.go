package resolve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
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
		Name: "misspelled", Query: "holy dunn", WantFirst: "holly.dunn8@corp.com", WantWhy: "name",
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

// TestNameCoversAllNeedsAnExactPart checks the multi-word name match: every
// word has to match a part of the name, one of them exactly, and only the last
// word may be the opening letters of a part. Before this, "data mart" matched a
// colleague called Dana Martin, one edit on the first word and a prefix on the
// second, and that colleague was placed ahead of everybody who knows the
// subject.
func TestNameCoversAllNeedsAnExactPart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name       string
		Asked      []string
		WantResult bool
	}{{ // Test 0: Both words exact.
		Name: "holly dunn", Asked: []string{"holly", "dunn"}, WantResult: true,
	}, { // Test 1: One word misspelled beside one exact.
		Name: "holly dunn", Asked: []string{"holy", "dunn"}, WantResult: true,
	}, { // Test 2: The last word still being typed.
		Name: "holly dunn", Asked: []string{"holly", "du"}, WantResult: true,
	}, { // Test 3: The words in the other order, the last one still being typed.
		Name: "holly dunn", Asked: []string{"dunn", "hol"}, WantResult: true,
	}, { // Test 4: Nothing exact, a misspelling and opening letters.
		Name: "dana martin", Asked: []string{"data", "mart"}, WantResult: false,
	}, { // Test 5: Nothing exact on the name the match was built for.
		Name: "holly dunn", Asked: []string{"holy", "dun"}, WantResult: false,
	}, { // Test 6: Opening letters on a word that is not the last.
		Name: "holly dunn", Asked: []string{"hol", "dunn"}, WantResult: false,
	}, { // Test 7: A word the name does not hold.
		Name: "holly dunn", Asked: []string{"holly", "kafka"}, WantResult: false,
	}, { // Test 8: No name at all.
		Name: "", Asked: []string{"holly", "dunn"}, WantResult: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := nameCoversAll(test.Name, test.Asked); got != test.WantResult {
				t.Errorf("nameCoversAll(%q, %v) = %t, want %t",
					test.Name, test.Asked, got, test.WantResult)
			}
		})
	}
}

// TestNameMatchYieldsToAStatedSubject checks a multi-word name match stands
// aside when the question is a subject and the word search already found
// somebody who states it. "price book" is Price Booker as far as it was typed,
// and it is also what a pricing team calls its subject.
func TestNameMatchYieldsToAStatedSubject(t *testing.T) {
	t.Parallel()
	expert := &model.Person{ID: "erin@corp.com", Name: "Erin Stone", Email: "erin@corp.com"}
	direct := []model.Match{{Person: expert, Declared: true}}
	reach := []model.Match{{Person: expert, Declared: true, Reach: true}}
	chatter := []model.Match{{Person: expert}}
	tests := []struct {
		Query     string
		Subject   bool
		Ranked    [][]model.Match
		WantCount int
	}{{ // Test 0: The question is a subject somebody states.
		Query: "price book", Subject: true, Ranked: [][]model.Match{direct}, WantCount: 0,
	}, { // Test 1: The one who states it sits in the second list, the departed.
		Query: "price book", Subject: true, Ranked: [][]model.Match{nil, direct}, WantCount: 0,
	}, { // Test 2: The subject is only mentioned, never stated.
		Query: "price book", Subject: true, Ranked: [][]model.Match{chatter}, WantCount: 1,
	}, { // Test 3: Only a subject that travels with it is stated.
		Query: "price book", Subject: true, Ranked: [][]model.Match{reach}, WantCount: 1,
	}, { // Test 4: The word search ranked nobody.
		Query: "price book", Subject: true, WantCount: 1,
	}, { // Test 5: The graph holds no such subject.
		Query: "price book", Subject: false, Ranked: [][]model.Match{direct}, WantCount: 1,
	}, { // Test 6: The whole name still finds its holder beside a stated subject.
		Query: "price booker", Subject: true, Ranked: [][]model.Match{direct}, WantCount: 1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := index.New()
			ix.Graph.People = map[model.ID]*model.Person{
				"price@corp.com": {
					ID: "price@corp.com", Name: "Price Booker", Email: "price@corp.com",
				},
			}
			if test.Subject {
				ix.Graph.Topics = map[model.ID]*model.Topic{
					"price-book": {ID: "price-book", Name: "price-book", Curated: true},
				}
			}
			got := nameMatch(ix, test.Query, 5, test.Ranked...)
			if len(got) != test.WantCount {
				t.Errorf("nameMatch(%q) = %d matches, want %d",
					test.Query, len(got), test.WantCount)
			}
		})
	}
}

// TestNameLookalikeLeavesASubjectToItsExpert runs both guards through the
// keyword and the semantic resolver. "data mart" sits one edit and a few
// opening letters from Dana Martin, and "price book" is Price Booker as far as
// it was typed, yet each is a subject somebody states, so the expert answers
// and nobody is matched by name.
func TestNameLookalikeLeavesASubjectToItsExpert(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Dana Martin", Email: "dana@corp.com", Title: "Talent Manager",
			Team: "People", Source: "org-csv"},
		{Name: "Walter Schrute", Email: "walter@corp.com", Title: "Staff Analyst",
			Team: "Insight", Topics: []string{"data-mart"}, Source: "org-csv"},
		{Name: "Price Booker", Email: "price@corp.com", Title: "Recruiter",
			Team: "People", Source: "org-csv"},
		{Name: "Erin Stone", Email: "erin@corp.com", Title: "Staff Analyst",
			Team: "Revenue", Topics: []string{"price-book"}, Source: "org-csv"},
	})
	ix.Canonicalize()
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	for _, subject := range []model.ID{"data-mart", "price-book"} {
		if !ix.Graph.Topics[subject].Salient() {
			t.Fatalf("%s is not a subject of the test company, so the test proves nothing", subject)
		}
	}
	resolvers := []Resolver{NewKeyword(ix), NewSemantic(ix, fakeEmbedder{})}

	tests := []struct {
		Query     string
		Resolver  int
		WantFirst string
	}{{ // Test 0: A misspelling and opening letters away from a name, by words.
		Query: "data mart", Resolver: 0, WantFirst: "walter@corp.com",
	}, { // Test 1: A name as far as it was typed, by words.
		Query: "price book", Resolver: 0, WantFirst: "erin@corp.com",
	}, { // Test 2: A misspelling and opening letters away from a name, by meaning.
		Query: "data mart", Resolver: 1, WantFirst: "walter@corp.com",
	}, { // Test 3: A name as far as it was typed, by meaning.
		Query: "price book", Resolver: 1, WantFirst: "erin@corp.com",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ans, err := resolvers[test.Resolver].Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("resolve %q: %v", test.Query, err)
			}
			if len(ans.People) == 0 {
				t.Fatalf("resolve %q returned nobody", test.Query)
			}
			if got := string(ans.People[0].Person.ID); got != test.WantFirst {
				t.Errorf("resolve %q top = %s, want %s", test.Query, got, test.WantFirst)
			}
			for _, m := range ans.People {
				if slices.Contains(m.Reasons, "name") {
					t.Errorf("resolve %q matched %s by name", test.Query, m.Person.ID)
				}
			}
		})
	}
}
