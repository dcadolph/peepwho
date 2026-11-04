package index

import (
	"maps"
	"slices"
	"strings"

	"github.com/dcadolph/peepwho/internal/model"
)

// Profile is everything the index knows about one person: the person, their
// org placement, their manager, and the channels they are active in.
type Profile struct {
	// Person is the profiled individual.
	Person *model.Person
	// Team is the person's team, if known.
	Team *model.Team
	// Org is the person's organization, if known.
	Org *model.Org
	// Manager is the person's manager, if known and indexed.
	Manager *model.Person
	// Channels lists the channels naming the person as an active member.
	Channels []*model.Channel
	// Joins are the inferred identity merges folded into this person, with the
	// confidence and evidence for each.
	Joins []Join
}

// Profile returns the full profile for id, resolving identity aliases, or
// false when the person is not in the graph.
func (ix *Index) Profile(id model.ID) (Profile, bool) {
	cid := ix.identityResolver().Canonical(model.ID(strings.ToLower(string(id))))
	p := ix.Graph.People[cid]
	if p == nil {
		return Profile{}, false
	}
	out := Profile{Person: p, Joins: ix.JoinsFor(cid)}
	if p.TeamID != "" {
		out.Team = ix.Graph.Teams[p.TeamID]
	}
	if p.OrgID != "" {
		out.Org = ix.Graph.Orgs[p.OrgID]
	}
	if p.ManagerID != "" {
		out.Manager = ix.Graph.People[p.ManagerID]
	}
	ids := slices.Sorted(maps.Keys(ix.Graph.Channels))
	for _, chID := range ids {
		ch := ix.Graph.Channels[chID]
		if slices.Contains(ch.Members, p.ID) {
			out.Channels = append(out.Channels, ch)
		}
	}
	return out, true
}
