package index

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
)

// splitAliceRecords returns the same human seen by two sources that cannot
// join on their own: org-csv knows the email, GitHub only the login.
func splitAliceRecords() []connector.Record {
	return []connector.Record{{
		Kind: connector.KindPerson, Email: "alice@corp.com", Name: "Alice Smith",
		Title: "Engineer", Topics: []string{"payments"}, Source: "org-csv",
	}, {
		Kind: connector.KindPerson, PersonID: "github:alice", Name: "@alice",
		Topics: []string{"terraform"}, Source: "github",
	}}
}

func TestAliasJoinsAtBuild(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Alias("alice@corp.com", "github:alice")
	ix.Build(splitAliceRecords())

	if got := len(ix.Graph.People); got != 1 {
		t.Fatalf("people = %d, want 1", got)
	}
	p := ix.Graph.People["alice@corp.com"]
	if p == nil {
		t.Fatal("missing canonical person alice@corp.com")
		return
	}
	if diff := cmp.Diff([]model.ID{"github:alice"}, p.Identities); diff != "" {
		t.Errorf("identities mismatch (-want +got):\n%s", diff)
	}
	if p.Name != "Alice Smith" {
		t.Errorf("name = %q, want the real name to beat the @handle", p.Name)
	}
	for _, query := range []string{"payments", "terraform"} {
		got := ix.Search(query, 3)
		if len(got) != 1 || got[0].Person.ID != "alice@corp.com" {
			t.Errorf("Search(%q) did not resolve to the joined person: %+v", query, got)
		}
	}
}

func TestCanonicalizeMergesExisting(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		splitAliceRecords()[0],
		splitAliceRecords()[1],
		{
			Kind: connector.KindPerson, Email: "bob@corp.com", Name: "Bob Jones",
			Manager: "github:alice", Topics: []string{"kafka"}, Source: "org-csv",
		},
		{
			Kind: connector.KindChannel, Name: "infra", Title: "infra questions",
			Members: []string{"github:alice", "bob@corp.com"}, Source: "slack",
		},
	})
	if got := len(ix.Graph.People); got != 3 {
		t.Fatalf("people before = %d, want 3", got)
	}

	ix.Alias("alice@corp.com", "github:alice")
	ix.Canonicalize()

	if got := len(ix.Graph.People); got != 2 {
		t.Fatalf("people after = %d, want 2", got)
	}
	p := ix.Graph.People["alice@corp.com"]
	if p == nil {
		t.Fatal("missing canonical person alice@corp.com")
		return
	}
	if p.Name != "Alice Smith" || p.Title != "Engineer" {
		t.Errorf("merged person lost fields: %+v", p)
	}
	if diff := cmp.Diff([]model.ID{"github:alice"}, p.Identities); diff != "" {
		t.Errorf("identities mismatch (-want +got):\n%s", diff)
	}
	if got := ix.Graph.People["bob@corp.com"].ManagerID; got != "alice@corp.com" {
		t.Errorf("manager = %q, want alice@corp.com", got)
	}
	wantMembers := []model.ID{"alice@corp.com", "bob@corp.com"}
	if diff := cmp.Diff(wantMembers, ix.Graph.Channels["infra"].Members); diff != "" {
		t.Errorf("members mismatch (-want +got):\n%s", diff)
	}
	got := ix.Search("terraform", 3)
	if len(got) != 1 || got[0].Person.ID != "alice@corp.com" {
		t.Errorf("Search(terraform) after merge: %+v", got)
	}
}

func TestAliasesSurviveSaveLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")

	ix := New()
	ix.Alias("alice@corp.com", "github:alice")
	ix.Build(splitAliceRecords()[:1])
	if err := ix.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	loaded.Add(splitAliceRecords()[1:])
	loaded.Canonicalize()

	if got := len(loaded.Graph.People); got != 1 {
		t.Fatalf("people = %d, want 1", got)
	}
	got := loaded.Search("terraform", 3)
	if len(got) != 1 || got[0].Person.ID != "alice@corp.com" {
		t.Errorf("Search(terraform) after reload+merge: %+v", got)
	}
}

func TestDualIdentityRecordAutoJoins(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, PersonID: "github:carol", Name: "@carol",
		Topics: []string{"deploys"}, Source: "github",
	}, {
		Kind: connector.KindPerson, PersonID: "github:carol", Email: "carol@corp.com",
		Name: "Carol Lee", Topics: []string{"kubernetes"}, Source: "github",
	}})
	ix.Canonicalize()

	if got := len(ix.Graph.People); got != 1 {
		t.Fatalf("people = %d, want 1", got)
	}
	p := ix.Graph.People["carol@corp.com"]
	if p == nil {
		t.Fatal("missing canonical person carol@corp.com")
		return
	}
	if diff := cmp.Diff([]model.ID{"github:carol"}, p.Identities); diff != "" {
		t.Errorf("identities mismatch (-want +got):\n%s", diff)
	}
	got := ix.Search("deploys", 3)
	if len(got) != 1 || got[0].Person.ID != "carol@corp.com" {
		t.Errorf("Search(deploys): %+v", got)
	}
}

// TestAMergeKeepsBothSidesDirectWork covers what ownership is decided on. The
// merge carried Topics and Stated and silently dropped Direct, so joining a
// person's github handle to their commits DESTROYED the ownership evidence the
// join existed to connect: a maintainer with thirty-eight changes in an area
// lost it to a colleague with thirteen.
func TestAMergeKeepsBothSidesDirectWork(t *testing.T) {
	t.Parallel()
	dst := &model.Person{ID: "a@x.com", Direct: map[model.ID]float64{"storage": 20}}
	src := &model.Person{ID: "github:a", Direct: map[model.ID]float64{"storage": 18, "tsdb": 4}}
	mergePerson(dst, src)
	if got := dst.Direct["storage"]; got != 38 {
		t.Errorf("storage direct = %v after merge, want 38: the absorbed side's work vanished", got)
	}
	if got := dst.Direct["tsdb"]; got != 4 {
		t.Errorf("tsdb direct = %v, want 4", got)
	}
}

// TestPersonLinksFoldPersistAndForget verifies a person seen in two Slack
// workspaces keeps both direct message links sorted through the email join,
// a save and load keeps them, and forgetting the person takes them along.
func TestPersonLinksFoldPersistAndForget(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U2", Name: "Jane Roe",
		Email: "jane@corp.com", Link: "slack://user?team=T1&id=U2",
	}, {
		Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Name: "Jane Roe",
		Email: "jane@corp.com", Link: "slack://user?team=T0&id=U1",
	}, {
		Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Text: "retries",
		Link: "slack://user?team=T0&id=U1",
	}, {
		Kind: connector.KindPerson, Source: "git", Name: "Ada Lane", Email: "ada@corp.com",
	}})
	ix.Canonicalize()
	want := []string{"slack://user?team=T0&id=U1", "slack://user?team=T1&id=U2"}
	jane := ix.Graph.People["jane@corp.com"]
	if jane == nil {
		t.Fatal("missing canonical person jane@corp.com")
		return
	}
	if diff := cmp.Diff(want, jane.Links); diff != "" {
		t.Errorf("links after build mismatch (-want +got):\n%s", diff)
	}
	if ada := ix.Graph.People["ada@corp.com"]; ada == nil || ada.Links != nil {
		t.Errorf("a person with no Slack identity has links: %+v", ada)
	}

	path := filepath.Join(t.TempDir(), "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if diff := cmp.Diff(want, loaded.Graph.People["jane@corp.com"].Links); diff != "" {
		t.Errorf("links after load mismatch (-want +got):\n%s", diff)
	}

	loaded.Forget(loaded.Graph.People["jane@corp.com"])
	for id, p := range loaded.Graph.People {
		if len(p.Links) != 0 {
			t.Errorf("%s still holds links after the purge: %v", id, p.Links)
		}
	}
}

// TestMergePersonUnionsLinks verifies a merge keeps both sides' deep links once
// each, sorted, so a handle absorbed into an email keeps its direct message.
func TestMergePersonUnionsLinks(t *testing.T) {
	t.Parallel()
	dst := &model.Person{ID: "a@x.com", Links: []string{"slack://user?team=T1&id=U2"}}
	src := &model.Person{ID: "slack:U1", Links: []string{"slack://user?team=T0&id=U1", "slack://user?team=T1&id=U2"}}
	mergePerson(dst, src)
	want := []string{"slack://user?team=T0&id=U1", "slack://user?team=T1&id=U2"}
	if diff := cmp.Diff(want, dst.Links); diff != "" {
		t.Errorf("links mismatch (-want +got):\n%s", diff)
	}
}
