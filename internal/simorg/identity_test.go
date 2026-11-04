package simorg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/slack"
)

// buildIdentity runs the real ingest path over a company built to break
// identity resolution: the org chart, then Slack, then CODEOWNERS, in the
// order a person would actually add them.
func buildIdentity(t *testing.T, spec IdentitySpec) (*index.Index, *IdentityOrg) {
	t.Helper()
	org := GenerateIdentity(spec)
	t.Cleanup(org.Close)

	dir := t.TempDir()
	csvPath := filepath.Join(dir, "org.csv")
	ownersPath := filepath.Join(dir, "CODEOWNERS")
	if err := os.WriteFile(csvPath, []byte(org.CSV), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if err := os.WriteFile(ownersPath, []byte(org.CodeOwners), 0o600); err != nil {
		t.Fatalf("write codeowners: %v", err)
	}

	ctx := context.Background()
	ix := index.New()
	for _, step := range []struct {
		Name   string
		Source connector.Source
	}{
		{"org-csv", connector.NewOrgCSV(csvPath)},
		{"slack", connector.NewSlackWithClient(
			slack.New("x", slack.WithBaseURL(org.Slack.URL)),
			connector.SlackOptions{MaxMessages: 100000})},
		{"codeowners", connector.NewCodeOwners(ownersPath)},
	} {
		recs, err := step.Source.Fetch(ctx)
		if err != nil {
			t.Fatalf("%s: %v", step.Name, err)
		}
		ix.Add(recs)
	}
	ix.AutoJoin()
	ix.Canonicalize()
	return ix, org
}

// TestIdentityNeverConfusesPeople is the test that matters most for a tool
// whose whole job is naming the right human. Two people who share a display
// name must never become one: merging them would hand one person's
// conversations, expertise, and past work to somebody else. Peepwho cannot be
// run against a real workspace before it has customers, so the traps a real
// workspace contains are planted deliberately and at volume.
func TestIdentityNeverConfusesPeople(t *testing.T) {
	t.Parallel()
	ix, org := buildIdentity(t, IdentitySpec{Seed: 3})

	overMerged := 0
	for _, group := range org.MustNotMerge {
		seen := make(map[model.ID][]model.ID)
		for _, id := range group {
			canonical := ix.Canonical(id)
			seen[canonical] = append(seen[canonical], id)
		}
		for canonical, ids := range seen {
			if len(ids) > 1 {
				overMerged++
				if overMerged <= 5 {
					t.Errorf("distinct people merged into %q: %v", canonical, ids)
				}
			}
		}
	}
	if overMerged > 0 {
		t.Errorf("%d groups of distinct people were merged", overMerged)
	}
}

// TestIdentityJoinsOnePersonAcrossSources verifies the opposite failure: one
// human listed in the org chart, talking in Slack, and owning code under a
// bare handle has to end up as a single person, not three.
func TestIdentityJoinsOnePersonAcrossSources(t *testing.T) {
	t.Parallel()
	ix, org := buildIdentity(t, IdentitySpec{Seed: 3})

	split, checked := 0, 0
	for _, identifiers := range org.MustMerge {
		checked++
		canonical := ix.Canonical(identifiers[0])
		for _, id := range identifiers[1:] {
			if got := ix.Canonical(id); got != canonical {
				split++
				if split <= 5 {
					t.Errorf("one person came apart: %q resolved to %q, but %q resolved to %q",
						identifiers[0], canonical, id, got)
				}
				break
			}
		}
	}
	// Some handles are legitimately ambiguous and are left alone on purpose,
	// so this asks for the great majority rather than perfection.
	if rate := float64(checked-split) / float64(checked); rate < 0.95 {
		t.Errorf("only %.0f%% of people joined across all three sources, want at least 95%%", rate*100)
	}
	t.Logf("joined %d of %d people across org chart, Slack, and CODEOWNERS", checked-split, checked)
}

// TestIdentityKeepsOneTeam verifies a person belongs to the team the org chart
// gives them, however widely they talk. Talking in seven other teams' channels
// is normal behavior and must not reassign anyone.
func TestIdentityKeepsOneTeam(t *testing.T) {
	t.Parallel()
	ix, org := buildIdentity(t, IdentitySpec{Seed: 3, CrossTeamTalkers: 40})

	wrong, checked := 0, 0
	for id, wantTeam := range org.WantTeam {
		person := ix.Graph.People[ix.Canonical(id)]
		if person == nil {
			t.Errorf("person %q vanished from the graph", id)
			continue
		}
		checked++
		team := ix.Graph.Teams[person.TeamID]
		if team == nil || team.Name != wantTeam {
			wrong++
			if wrong <= 5 {
				got := "none"
				if team != nil {
					got = team.Name
				}
				t.Errorf("%q is on team %q, want %q", id, got, wantTeam)
			}
		}
	}
	if wrong > 0 {
		t.Errorf("%d of %d people ended up on the wrong team", wrong, checked)
	}
}

// TestIdentityAtVolume runs the same traps at sizes a real company reaches.
// Name collisions get likelier as an organization grows, which is exactly when
// a people tool must not start guessing.
func TestIdentityAtVolume(t *testing.T) {
	t.Parallel()
	sizes := []IdentitySpec{
		{People: 200, SharedNames: 30, RoleAccounts: 8, CrossTeamTalkers: 50, Seed: 5},
		{People: 800, SharedNames: 120, RoleAccounts: 16, CrossTeamTalkers: 200, Seed: 6},
	}
	for _, spec := range sizes {
		t.Run(fmt.Sprintf("%d people", spec.People), func(t *testing.T) {
			t.Parallel()
			ix, org := buildIdentity(t, spec)

			merged := 0
			for _, group := range org.MustNotMerge {
				seen := make(map[model.ID]int)
				for _, id := range group {
					seen[ix.Canonical(id)]++
				}
				for _, n := range seen {
					if n > 1 {
						merged++
					}
				}
			}
			if merged > 0 {
				t.Errorf("%d collisions merged distinct people at %d people", merged, spec.People)
			}

			wrongTeam := 0
			for id, wantTeam := range org.WantTeam {
				person := ix.Graph.People[ix.Canonical(id)]
				if person == nil {
					continue
				}
				if team := ix.Graph.Teams[person.TeamID]; team == nil || team.Name != wantTeam {
					wrongTeam++
				}
			}
			if wrongTeam > 0 {
				t.Errorf("%d people on the wrong team at %d people", wrongTeam, spec.People)
			}
			t.Logf("%d people, %d in graph: no confusion, no team drift",
				spec.People, len(ix.Graph.People))
		})
	}
}
