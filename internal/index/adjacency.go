package index

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// Adjacency tuning. Co-membership in a group is normalized by the group's size,
// so a tight team counts for far more than a broad channel, and groups that span
// most of the org are dropped as administrative rather than collaborative.
const (
	// orgWideAbs excludes any group larger than this outright.
	orgWideAbs = 200
	// orgWideFrac excludes a group holding more than this fraction of the org,
	// once the org is large enough for the fraction to be meaningful.
	orgWideFrac = 0.4
	// orgWideMinOrg is the smallest org size at which orgWideFrac applies.
	orgWideMinOrg = 50
	// topicAdjacencyWeight scales shared-topic overlap against shared-group
	// co-membership so expertise overlap contributes without dominating.
	topicAdjacencyWeight = 0.5
	// maxReasonItems caps how many group or topic names a reason lists.
	maxReasonItems = 4
)

// permissionSuffixes are the tier suffixes stripped when folding group names, so
// store-admin, store-write, and store-read count as one "store" group rather
// than three separate ones.
//
//nolint:gochecknoglobals // Read-only lookup table.
var permissionSuffixes = []string{
	"-admin", "-administrators", "-write", "-writers", "-read", "-readers",
	"-readonly", "-viewer", "-viewers", "-developer", "-developers", "-dev",
	"-maintainer", "-maintainers", "-owner", "-owners", "-contributor",
	"-contributors", "_admin", "_write", "_read", "_readonly",
}

// foldGroupName lowercases a group name and strips a trailing permission tier,
// so permission variants of one team collapse to the same group.
func foldGroupName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, s := range permissionSuffixes {
		if strings.HasSuffix(n, s) && len(n) > len(s) {
			return strings.TrimSuffix(n, s)
		}
	}
	return n
}

// isOrgWide reports whether a group of the given size spans so much of the org
// that shared membership in it is administrative, not evidence of adjacency.
func isOrgWide(size, total int) bool {
	if size > orgWideAbs {
		return true
	}
	if total >= orgWideMinOrg && float64(size) > orgWideFrac*float64(total) {
		return true
	}
	return false
}

// Adjacent is one person near the focal person, with the score and the reasons.
type Adjacent struct {
	// ID is the adjacent person's identifier.
	ID model.ID `json:"id"`
	// Name is the adjacent person's display name.
	Name string `json:"name"`
	// Email is the adjacent person's work email.
	Email string `json:"email,omitempty"`
	// Score is the adjacency score, higher is nearer.
	Score float64 `json:"score"`
	// Reasons explain the adjacency in plain terms.
	Reasons []string `json:"reasons,omitempty"`
}

// FindPerson resolves a query to a person by canonical id, email, or an exact
// display-name match, returning nil when nothing matches. When several people
// share the queried name it returns the one with the lowest canonical id, the
// same one every run. A caller about to act destructively must consult
// FindPeople first: picking one of several namesakes is a choice the operator
// has to make, not this function.
func (ix *Index) FindPerson(query string) *model.Person {
	people := ix.FindPeople(query)
	if len(people) == 0 {
		return nil
	}
	return people[0]
}

// FindPeople resolves a query to every person it could mean, lowest canonical
// id first. A canonical id or normalized email names exactly one person; a
// display name names everyone who shares it. More than one result means the
// query is ambiguous, which a purge or any other destructive caller must
// surface rather than resolve by picking.
func (ix *Index) FindPeople(query string) []*model.Person {
	g := ix.Graph
	r := ix.identityResolver()
	q := strings.TrimSpace(query)
	for _, key := range []model.ID{model.ID(strings.ToLower(q)), model.ID(util.NormalizeEmail(q))} {
		if p := g.People[r.Canonical(key)]; p != nil {
			return []*model.Person{p}
		}
	}
	var out []*model.Person
	for _, p := range g.People {
		if strings.EqualFold(p.Name, q) || strings.EqualFold(p.Email, q) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// candidate accumulates one person's adjacency to the focal person.
type candidate struct {
	// score is the running adjacency score.
	score float64
	// groups are the folded names of shared groups, for the reason text.
	groups []string
	// topics are the names of shared topics, for the reason text.
	topics []string
}

// Near ranks the people nearest the focal person by shared group membership and
// shared topics. Co-membership is size-normalized so a tight group counts more
// than a broad one, org-wide groups are dropped, and permission tiers of one
// group are folded together. It returns at most limit people, nearest first.
func (ix *Index) Near(focal model.ID, limit int) []Adjacent {
	g := ix.Graph
	if g == nil || g.People[focal] == nil {
		return nil
	}
	r := ix.identityResolver()
	total := len(g.People)

	// Build folded groups, unioning members that share a folded name.
	groups := make(map[string]map[model.ID]bool)
	addGroup := func(key string, members []model.ID) {
		set := groups[key]
		if set == nil {
			set = make(map[model.ID]bool)
			groups[key] = set
		}
		for _, m := range members {
			set[r.Canonical(m)] = true
		}
	}
	for _, ch := range g.Channels {
		addGroup("ch:"+foldGroupName(ch.Name), ch.Members)
	}
	teamMembers := make(map[model.ID][]model.ID)
	for id, p := range g.People {
		if p.TeamID != "" {
			teamMembers[p.TeamID] = append(teamMembers[p.TeamID], id)
		}
	}
	for tid, mems := range teamMembers {
		name := string(tid)
		if t := g.Teams[tid]; t != nil && t.Name != "" {
			name = t.Name
		}
		addGroup("tm:"+foldGroupName(name), mems)
	}

	cands := make(map[model.ID]*candidate)
	get := func(id model.ID) *candidate {
		c := cands[id]
		if c == nil {
			c = &candidate{}
			cands[id] = c
		}
		return c
	}

	// Shared-group co-membership, size-normalized. Groups run in sorted key
	// order: the reason text lists the first maxReasonItems shared groups, and
	// score accumulation order fixes the last float bits, so an unsorted walk
	// showed a different four groups and different score bytes run to run.
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		set := groups[key]
		if !set[focal] {
			continue
		}
		size := len(set)
		if size < 2 || isOrgWide(size, total) {
			continue
		}
		w := 1.0 / float64(size-1)
		label := groupLabel(key)
		for m := range set {
			if m == focal {
				continue
			}
			c := get(m)
			c.score += w
			if len(c.groups) < maxReasonItems {
				c.groups = append(c.groups, label)
			}
		}
	}

	// Shared-topic overlap, measured against how much each of the two knows
	// rather than as a raw total. Without that the people who touch everything
	// overlap heavily with everybody, and whoever you ask about, the answer is
	// the same handful of names.
	fp := g.People[focal]
	focalReach := math.Sqrt(fp.Reach())
	for pid, p2 := range g.People {
		if pid == focal || len(p2.Topics) == 0 {
			continue
		}
		var overlap float64
		var shared []string
		// Topics run in sorted order for the same reason the groups above do:
		// the capped shared list and the float accumulation both take their
		// order from this walk.
		for _, tid := range slices.Sorted(maps.Keys(fp.Topics)) {
			aff := fp.Topics[tid]
			if a2, ok := p2.Topics[tid]; ok {
				overlap += math.Min(aff, a2)
				// Every shared topic counts toward how close two people are,
				// but only real subjects are worth naming as the reason: a
				// word mined out of prose says nothing about why these two
				// work near each other.
				if len(shared) < maxReasonItems && g.Topics[tid].Salient() {
					shared = append(shared, topicName(g, tid))
				}
			}
		}
		if overlap > 0 && focalReach > 0 {
			if r := math.Sqrt(p2.Reach()); r > 0 {
				c := get(pid)
				c.score += (overlap / (focalReach * r)) * topicAdjacencyWeight
				c.topics = shared
			}
		}
	}

	return rankAdjacent(g, cands, limit)
}

// groupLabel turns a folded group key into a display label.
func groupLabel(key string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(key, "ch:"), "tm:")
	if strings.HasPrefix(key, "ch:") {
		return "#" + name
	}
	return "team " + titleName(name)
}

// titleName capitalizes each word of a folded group name for a readable label.
func titleName(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
	}
	return strings.Join(words, " ")
}

// topicName returns a topic's display name, or the raw id when unknown.
func topicName(g *model.Graph, tid model.ID) string {
	if t := g.Topics[tid]; t != nil && t.Name != "" {
		return t.Name
	}
	return string(tid)
}

// rankAdjacent sorts the candidates by score and returns the top limit with
// their reasons filled in.
func rankAdjacent(g *model.Graph, cands map[model.ID]*candidate, limit int) []Adjacent {
	out := make([]Adjacent, 0, len(cands))
	for id, c := range cands {
		p := g.People[id]
		if p == nil {
			continue
		}
		out = append(out, Adjacent{
			ID: id, Name: p.Name, Email: p.Email,
			Score: c.score, Reasons: reasonsFor(c),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// reasonsFor builds the human reasons for one candidate.
func reasonsFor(c *candidate) []string {
	var reasons []string
	if len(c.groups) > 0 {
		reasons = append(reasons, "shares "+strings.Join(c.groups, ", "))
	}
	if len(c.topics) > 0 {
		reasons = append(reasons, "topics: "+strings.Join(c.topics, ", "))
	}
	return reasons
}
