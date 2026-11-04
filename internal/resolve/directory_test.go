package resolve

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// TestBuildDirectory verifies the directory lists people, channels, teams,
// and topics with counts, sorted for browsing.
func TestBuildDirectory(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "zoe@x.com", Name: "Zoe Lang",
		Title: "Engineer", Team: "Billing", Org: "Payments",
		Topics: []string{"billing", "retries"}, Source: "t",
	}, {
		Kind: connector.KindPerson, Email: "al@x.com", Name: "Al Ono",
		Team: "Billing", Org: "Payments", Topics: []string{"billing"}, Source: "t",
	}, {
		Kind: connector.KindChannel, Name: "pay-help", Title: "billing questions",
		Members: []string{"zoe@x.com"}, Source: "t",
	}})

	d := BuildDirectory(ix)

	if len(d.People) != 2 || d.People[0].Name != "Al Ono" || d.People[1].Name != "Zoe Lang" {
		t.Errorf("people = %+v, want Al then Zoe by name", d.People)
	}
	if d.People[1].Team != "Billing" || d.People[1].Org != "Payments" || len(d.People[1].Topics) != 2 {
		t.Errorf("zoe row = %+v", d.People[1])
	}
	if len(d.Channels) != 1 || d.Channels[0].Name != "pay-help" || d.Channels[0].Members != 1 {
		t.Errorf("channels = %+v", d.Channels)
	}
	if len(d.Teams) != 1 || d.Teams[0].Name != "Billing" ||
		d.Teams[0].People != 2 || d.Teams[0].Org != "Payments" {
		t.Errorf("teams = %+v", d.Teams)
	}
	// A subject row says who holds the most of it, so the list answers without
	// another click, and how many still work on it, so a subject several people
	// know and nobody touches is visible rather than hidden behind a headcount.
	want := []DirectoryTopic{
		{Name: "billing", People: 2, Lead: "Al Ono"},
		{Name: "retries", People: 1, Lead: "Zoe Lang"},
	}
	if diff := cmp.Diff(want, d.Topics); diff != "" {
		t.Errorf("topics mismatch (-want +got):\n%s", diff)
	}
}

// TestDirectoryMarksWhoHoldsSomethingAlone checks each person carries the two
// readings that make an organization chart worth drawing: what nobody else
// holds, and what they have stopped touching.
//
// Reporting lines are the part every company already has. Whether a seat is the
// only one holding something is the part nobody can see, and it is what turns
// the chart from a directory into a risk view.
func TestDirectoryMarksWhoHoldsSomethingAlone(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Graph.Topics = map[model.ID]*model.Topic{
		"billing": {ID: "billing", Name: "billing", Curated: true},
		"ledger":  {ID: "ledger", Name: "ledger", Curated: true},
	}
	ix.Graph.People = map[model.ID]*model.Person{
		"ada@x.com": {
			ID: "ada@x.com", Name: "Ada",
			// Ada alone holds ledger, and has stopped working on it.
			Topics: map[model.ID]float64{"billing": 5, "ledger": 4},
			Recent: map[model.ID]float64{"billing": 2},
		},
		"bo@x.com": {
			ID: "bo@x.com", Name: "Bo",
			Topics: map[model.ID]float64{"billing": 3},
			Recent: map[model.ID]float64{"billing": 3},
		},
	}
	ix.Canonicalize()

	d := BuildDirectory(ix)
	byName := make(map[string]DirectoryPerson, len(d.People))
	for _, p := range d.People {
		byName[p.Name] = p
	}
	ada, bo := byName["Ada"], byName["Bo"]
	if diff := cmp.Diff([]string{"ledger"}, ada.Alone, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Ada alone (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"ledger"}, ada.Quiet, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Ada quiet (-want +got):\n%s", diff)
	}
	// Two people hold billing, so neither holds it alone.
	if len(bo.Alone) != 0 {
		t.Errorf("Bo alone = %v, want nothing: billing has two holders", bo.Alone)
	}
	if len(bo.Quiet) != 0 {
		t.Errorf("Bo quiet = %v, want nothing: Bo is still working on billing", bo.Quiet)
	}
}

// TestDirectoryRoutesSquadsToTeams covers the CODEOWNERS group owner: a
// @org/team handle is a team on the teams list, carrying the areas it was
// declared over, and never a person on the people list.
func TestDirectoryRoutesSquadsToTeams(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "zoe@x.com", Name: "Zoe Lang",
		Topics: []string{"billing"}, Source: "git",
	}, {
		Kind: connector.KindPerson, Name: "@grafana/alerting-squad",
		PersonID: "codeowners:grafana/alerting-squad",
		Topics:   []string{"alerting", "silences"}, Source: "codeowners", Weight: 1,
	}})

	d := BuildDirectory(ix)

	for _, p := range d.People {
		if strings.Contains(p.Name, "/") {
			t.Errorf("squad %q listed as a person", p.Name)
		}
	}
	var squad *DirectoryTeam
	for i := range d.Teams {
		if d.Teams[i].Name == "@grafana/alerting-squad" {
			squad = &d.Teams[i]
		}
	}
	if squad == nil {
		t.Fatalf("squad missing from teams: %+v", d.Teams)
		return
	}
	if squad.People != 0 {
		t.Errorf("squad membership = %d, want 0 for unknown", squad.People)
	}
	if len(squad.Topics) == 0 {
		t.Errorf("squad row carries no areas: %+v", squad)
	}
}
