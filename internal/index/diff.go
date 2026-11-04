package index

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// Changes describes which people, teams, and channels entered or left the graph
// between a previous index and a new one.
type Changes struct {
	// PeopleJoined are people present now but not before.
	PeopleJoined []string `json:"people_joined,omitempty"`
	// PeopleLeft are people present before but not now.
	PeopleLeft []string `json:"people_left,omitempty"`
	// TeamsAdded are teams present now but not before.
	TeamsAdded []string `json:"teams_added,omitempty"`
	// TeamsRemoved are teams present before but not now.
	TeamsRemoved []string `json:"teams_removed,omitempty"`
	// ChannelsAdded are channels present now but not before.
	ChannelsAdded []string `json:"channels_added,omitempty"`
	// ChannelsRemoved are channels present before but not now.
	ChannelsRemoved []string `json:"channels_removed,omitempty"`
}

// Empty reports whether nothing changed.
func (c Changes) Empty() bool {
	return len(c.PeopleJoined)+len(c.PeopleLeft)+
		len(c.TeamsAdded)+len(c.TeamsRemoved)+
		len(c.ChannelsAdded)+len(c.ChannelsRemoved) == 0
}

// Summary is a one-line count of the changes.
func (c Changes) Summary() string {
	var parts []string
	count := func(n int, sign, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s%d %s", sign, n, label))
		}
	}
	count(len(c.PeopleJoined), "+", "people")
	count(len(c.PeopleLeft), "-", "people")
	count(len(c.ChannelsAdded), "+", "channels")
	count(len(c.ChannelsRemoved), "-", "channels")
	count(len(c.TeamsAdded), "+", "teams")
	count(len(c.TeamsRemoved), "-", "teams")
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// Merge folds later, a set of changes made after c, into c. Each list becomes
// the sorted union of both, except that a name later reports on one side of a
// pair is taken off the other side, so someone who left and came back reads
// as joined and someone who joined and then left reads as left. Merge entries
// oldest first to get the net of a run of folds.
func (c Changes) Merge(later Changes) Changes {
	return Changes{
		PeopleJoined:    mergeSide(c.PeopleJoined, later.PeopleJoined, later.PeopleLeft),
		PeopleLeft:      mergeSide(c.PeopleLeft, later.PeopleLeft, later.PeopleJoined),
		TeamsAdded:      mergeSide(c.TeamsAdded, later.TeamsAdded, later.TeamsRemoved),
		TeamsRemoved:    mergeSide(c.TeamsRemoved, later.TeamsRemoved, later.TeamsAdded),
		ChannelsAdded:   mergeSide(c.ChannelsAdded, later.ChannelsAdded, later.ChannelsRemoved),
		ChannelsRemoved: mergeSide(c.ChannelsRemoved, later.ChannelsRemoved, later.ChannelsAdded),
	}
}

// mergeSide returns the sorted union of earlier and later without any name in
// opposite, the later change on the other side of the pair.
func mergeSide(earlier, later, opposite []string) []string {
	drop := make(map[string]bool, len(opposite))
	for _, s := range opposite {
		drop[s] = true
	}
	seen := make(map[string]bool, len(earlier)+len(later))
	var out []string
	for _, list := range [][]string{earlier, later} {
		for _, s := range list {
			if drop[s] || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Diff reports what changed from the prev graph to the cur graph.
func Diff(prev, cur *model.Graph) Changes {
	if prev == nil {
		prev = model.NewGraph()
	}
	if cur == nil {
		cur = model.NewGraph()
	}
	return Changes{
		PeopleJoined:    missingPeople(cur.People, prev.People),
		PeopleLeft:      missingPeople(prev.People, cur.People),
		TeamsAdded:      missingTeams(cur.Teams, prev.Teams),
		TeamsRemoved:    missingTeams(prev.Teams, cur.Teams),
		ChannelsAdded:   missingChannels(cur.Channels, prev.Channels),
		ChannelsRemoved: missingChannels(prev.Channels, cur.Channels),
	}
}

// missingPeople returns labels of people in a whose id is absent from b,
// skipping anyone merely re-keyed rather than gone. When a later source adds
// an email for a person previously keyed by a handle, the canonical id
// changes while the person stays, and without this check the same human is
// reported as one person leaving and another joining. Only exact identifier
// overlap counts as a re-keying; a shared display name never does, because two
// different people with one name really can swap.
func missingPeople(a, b map[model.ID]*model.Person) []string {
	bKeys := identityKeys(b)
	var out []string
	for id, p := range a {
		if _, ok := b[id]; ok {
			continue
		}
		if rekeyed(id, p, bKeys) {
			continue
		}
		out = append(out, personLabel(id, p))
	}
	sort.Strings(out)
	return out
}

// identityKeys collects every identifier anyone in the graph answers to: the
// canonical id, the raw identities folded into it, and the mailbox in
// normalized form.
func identityKeys(people map[model.ID]*model.Person) map[model.ID]bool {
	keys := make(map[model.ID]bool, len(people))
	for id, p := range people {
		for _, k := range personKeys(id, p) {
			keys[k] = true
		}
	}
	return keys
}

// personKeys returns every identifier one person answers to. A role mailbox is
// not one of them: two automation accounts can share ci@corp.com without being
// the same account, and treating that as identity would hide one of them
// leaving behind the other joining.
func personKeys(id model.ID, p *model.Person) []model.ID {
	keys := []model.ID{id}
	if p == nil {
		return keys
	}
	keys = append(keys, p.Identities...)
	if p.Email != "" && !util.IsRoleEmail(p.Email) {
		keys = append(keys, model.ID(util.NormalizeEmail(p.Email)))
	}
	return keys
}

// rekeyed reports whether the person exists in the other graph under a
// different canonical id, which is a change of key rather than of people.
func rekeyed(id model.ID, p *model.Person, otherKeys map[model.ID]bool) bool {
	for _, k := range personKeys(id, p) {
		if otherKeys[k] {
			return true
		}
	}
	return false
}

// personLabel is a human label for a person, preferring name then email.
func personLabel(id model.ID, p *model.Person) string {
	switch {
	case p != nil && p.Name != "":
		return p.Name
	case p != nil && p.Email != "":
		return p.Email
	default:
		return string(id)
	}
}

// missingTeams returns names of teams in a whose id is absent from b.
func missingTeams(a, b map[model.ID]*model.Team) []string {
	var out []string
	for id, t := range a {
		if _, ok := b[id]; !ok {
			name := string(id)
			if t != nil && t.Name != "" {
				name = t.Name
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// missingChannels returns names of channels in a whose id is absent from b.
func missingChannels(a, b map[model.ID]*model.Channel) []string {
	var out []string
	for id, ch := range a {
		if _, ok := b[id]; !ok {
			name := string(id)
			if ch != nil && ch.Name != "" {
				name = ch.Name
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
