package index

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
)

// slackAccount is a Slack person record for email with the given status.
func slackAccount(id, email string, status model.AccountStatus) connector.Record {
	return connector.Record{
		Source: "slack", PersonID: "slack:" + id, Name: "Sam Two", Email: email,
		Topics: []string{"payments"}, Account: status,
	}
}

// TestAccountStatusMerge checks that a person is deactivated only when every
// account a status-aware source knows for them is, whatever order the records
// arrive in and whichever path joins them.
func TestAccountStatusMerge(t *testing.T) {
	t.Parallel()
	gone, live := model.AccountDeactivated, model.AccountActive
	tests := []struct {
		Records         []connector.Record
		Alias           [2]model.ID
		WantDeactivated bool
	}{{ // Test 0: One deactivated Slack account.
		Records:         []connector.Record{slackAccount("u1", "sam@x.com", gone)},
		WantDeactivated: true,
	}, { // Test 1: Two Slack accounts, active then deactivated, stay active.
		Records: []connector.Record{
			slackAccount("u1", "sam@x.com", live), slackAccount("u2", "sam@x.com", gone),
		},
	}, { // Test 2: The same two accounts in the other order stay active.
		Records: []connector.Record{
			slackAccount("u2", "sam@x.com", gone), slackAccount("u1", "sam@x.com", live),
		},
	}, { // Test 3: A source that cannot see accounts never hides a deactivation.
		Records: []connector.Record{
			{Source: "github", PersonID: "github:sam", Email: "sam@x.com", Topics: []string{"payments"}},
			slackAccount("u1", "sam@x.com", gone),
		},
		WantDeactivated: true,
	}, { // Test 4: No status-aware source means nobody is marked.
		Records: []connector.Record{
			{Source: "github", PersonID: "github:sam", Email: "sam@x.com", Topics: []string{"payments"}},
		},
	}, { // Test 5: Two people joined later by alias keep the active account.
		Records: []connector.Record{
			slackAccount("u1", "sam@x.com", live), slackAccount("u2", "sam@old.com", gone),
		},
		Alias: [2]model.ID{"sam@x.com", "sam@old.com"},
	}, { // Test 6: Joined later by alias, both deactivated, the person is.
		Records: []connector.Record{
			slackAccount("u1", "sam@x.com", gone), slackAccount("u2", "sam@old.com", gone),
		},
		Alias:           [2]model.ID{"sam@x.com", "sam@old.com"},
		WantDeactivated: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ix := New()
			ix.Build(test.Records)
			if test.Alias[0] != "" {
				ix.Alias(test.Alias[0], test.Alias[1])
				ix.Canonicalize()
			}
			p := ix.Graph.People[ix.Canonical("sam@x.com")]
			if p == nil {
				t.Fatalf("sam@x.com is missing from %v", slices.Sorted(maps.Keys(ix.Graph.People)))
			}
			if diff := cmp.Diff(test.WantDeactivated, p.Deactivated()); diff != "" {
				t.Errorf("deactivated mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAccountStatusFold checks that a newer listing of the same account says
// what it is now, so an account deactivated since the last run is.
func TestAccountStatusFold(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{slackAccount("u1", "sam@x.com", model.AccountActive)})
	ix.MergeIncremental([]connector.Record{slackAccount("u1", "sam@x.com", model.AccountDeactivated)})
	if !ix.Graph.People["sam@x.com"].Deactivated() {
		t.Error("an account deactivated since the last run is still active")
	}
}

// TestSearchSplitDeparted checks that the split happens before the cut, so the
// active people fill the limit and the departed are returned apart.
func TestSearchSplitDeparted(t *testing.T) {
	t.Parallel()
	recs := []connector.Record{slackAccount("u0", "gone@x.com", model.AccountDeactivated)}
	recs[0].Topics = []string{"payments", "payments", "payments"}
	for i := range 3 {
		recs = append(recs, slackAccount(fmt.Sprint("u", i+1), fmt.Sprintf("a%d@x.com", i), model.AccountActive))
	}
	ix := New()
	ix.Build(recs)
	people, departed := ix.SearchSplit("payments", 2)
	if len(people) != 2 || slices.ContainsFunc(people, func(m model.Match) bool { return m.Person.Deactivated() }) {
		t.Errorf("people = %v, want two active people", people)
	}
	if len(departed) != 1 || departed[0].Person.Email != "gone@x.com" || len(departed[0].Reasons) == 0 {
		t.Errorf("departed = %v, want gone@x.com with reasons", departed)
	}
	if diff := cmp.Diff(people, ix.Search("payments", 2)); diff != "" {
		t.Errorf("Search and SearchSplit disagree (-split +search):\n%s", diff)
	}
}

// TestAccountStatusPersists checks that a saved index keeps the status and that
// a graph written before the field existed loads with nobody deactivated.
func TestAccountStatusPersists(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{slackAccount("u1", "sam@x.com", model.AccountDeactivated)})
	path := filepath.Join(t.TempDir(), "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Graph.People["sam@x.com"].Deactivated() {
		t.Error("the saved index lost the deactivated status")
	}

	var legacy model.Person
	if err := json.Unmarshal([]byte(`{"ID":"sam@x.com","Name":"Sam","Topics":{"payments":1}}`), &legacy); err != nil {
		t.Fatalf("decode legacy person: %v", err)
	}
	if legacy.Deactivated() || legacy.Account != model.AccountUnknown {
		t.Errorf("legacy person account = %v, want unknown", legacy.Account)
	}
	active, err := json.Marshal(&model.Person{ID: "a"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(active, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := fields["Account"]; ok {
		t.Errorf("a person with unknown status writes Account: %s", active)
	}
}
