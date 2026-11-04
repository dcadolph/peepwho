package index

import (
	"slices"
	"strings"

	"github.com/dcadolph/peepwho/internal/identity"
	"github.com/dcadolph/peepwho/internal/model"
)

// Canonicalize rewrites the graph so every person appears once under their
// canonical identifier, merging people the identity resolver maps together
// and re-pointing channel members and manager links. Run it after loading
// aliases over an existing index; records added after aliases load already
// key canonically. A merged person keeps at most one embedding vector, so
// re-embed afterward when vectors matter.
func (ix *Index) Canonicalize() {
	r := ix.identityResolver()
	g := ix.Graph

	ids := make([]model.ID, 0, len(g.People))
	for id := range g.People {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		cid := r.Canonical(id)
		if cid == id {
			continue
		}
		src := g.People[id]
		delete(g.People, id)
		if dst := g.People[cid]; dst != nil {
			mergePerson(dst, src)
		} else {
			src.Identities = append(src.Identities, src.ID)
			src.ID = cid
			g.People[cid] = src
		}
		movePostings(ix.postings, id, cid)
		mergePersonText(ix.texts, id, cid)
		moveVec(ix.personVecs, id, cid)
	}

	for _, p := range g.People {
		if p.ManagerID != "" {
			p.ManagerID = r.Canonical(p.ManagerID)
		}
		p.Identities = cleanIdentities(p.Identities, p.ID)
	}
	for _, ch := range g.Channels {
		ch.Members = canonicalMembers(ch.Members, r)
	}
	markUbiquitous(g)
	ix.refreshStats()
}

// Scaffolding bounds. A topic held by more than ubiquityShare of an
// organization is its scaffolding rather than a subject inside it, and
// ubiquityFloor is the size below which the rule is not applied at all: in a
// small company a real subject genuinely can be held by most of it, and there
// are too few people for the share to mean anything.
const (
	ubiquityShare = 0.35
	ubiquityFloor = 60
)

// markUbiquitous flags the topics nearly everybody holds. In a code base those
// are the repository's own name, the language it is written in, and the
// directory every file lives under: stated as plainly as any real subject, and
// useless for telling one person from another. Left unflagged they lead the
// directory, crowd the risk report, and turn up as what somebody is known for.
func markUbiquitous(g *model.Graph) {
	if len(g.People) < ubiquityFloor {
		return
	}
	holders := make(map[model.ID]int, len(g.Topics))
	for _, p := range g.People {
		for tid := range p.Topics {
			holders[tid]++
		}
	}
	cut := float64(len(g.People)) * ubiquityShare
	for tid, topic := range g.Topics {
		topic.Ubiquitous = float64(holders[tid]) > cut
	}
}

// mergePerson folds src into dst: identity fields fill blanks, topic weights
// add, and src's identifiers are remembered as aliases.
func mergePerson(dst, src *model.Person) {
	if betterName(dst.Name, src.Name) {
		dst.Name = src.Name
	}
	if dst.Email == "" {
		dst.Email = src.Email
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.TeamID == "" {
		dst.TeamID = src.TeamID
	}
	if dst.OrgID == "" {
		dst.OrgID = src.OrgID
	}
	if dst.ManagerID == "" {
		dst.ManagerID = src.ManagerID
	}
	if dst.Topics == nil {
		dst.Topics = make(map[model.ID]float64)
	}
	for t, w := range src.Topics {
		dst.Topics[t] += w
	}
	// Direct is what ownership is decided on, and a merge that dropped the
	// absorbed record's share handed areas to whoever avoided being merged: on
	// a real project the maintainer with thirty-eight changes in an area lost
	// it to a colleague with thirteen, because his own were on the record his
	// github handle absorbed.
	for t, w := range src.Direct {
		if dst.Direct == nil {
			dst.Direct = make(map[model.ID]float64)
		}
		dst.Direct[t] += w
	}
	for t, w := range src.Recent {
		if dst.Recent == nil {
			dst.Recent = make(map[model.ID]float64)
		}
		dst.Recent[t] += w
	}
	for t, w := range src.Stated {
		if dst.Stated == nil {
			dst.Stated = make(map[model.ID]float64)
		}
		dst.Stated[t] += w
	}
	dst.Identities = append(dst.Identities, src.Identities...)
	dst.Identities = append(dst.Identities, src.ID)
	for _, t := range src.Owns {
		if !slices.Contains(dst.Owns, t) {
			dst.Owns = append(dst.Owns, t)
		}
	}
}

// movePostings shifts from's weight onto to for every term.
func movePostings(postings map[string]map[model.ID]float64, from, to model.ID) {
	for _, m := range postings {
		if w, ok := m[from]; ok {
			m[to] += w
			delete(m, from)
		}
	}
}

// mergePersonText folds from's reason text into to: fields fill blanks,
// topics append, and free text concatenates.
func mergePersonText(texts map[model.ID]*personText, from, to model.ID) {
	src := texts[from]
	if src == nil {
		return
	}
	delete(texts, from)
	dst := texts[to]
	if dst == nil {
		texts[to] = src
		return
	}
	dst.Titles = append(dst.Titles, src.Titles...)
	dst.Teams = append(dst.Teams, src.Teams...)
	dst.Topics = append(dst.Topics, src.Topics...)
	dst.Text = strings.TrimSpace(dst.Text + " " + src.Text)
}

// moveVec carries from's embedding vector over to to when to has none.
func moveVec(vecs map[model.ID][]float32, from, to model.ID) {
	if v, ok := vecs[from]; ok {
		if _, exists := vecs[to]; !exists {
			vecs[to] = v
		}
		delete(vecs, from)
	}
}

// cleanIdentities dedupes and sorts a person's alias list, dropping the
// person's own identifier. It returns nil when nothing remains.
func cleanIdentities(ids []model.ID, self model.ID) []model.ID {
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ids = slices.DeleteFunc(ids, func(id model.ID) bool { return id == self || id == "" })
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// canonicalMembers resolves each member to its canonical identifier,
// preserving order and dropping duplicates.
func canonicalMembers(members []model.ID, r *identity.Resolver) []model.ID {
	if len(members) == 0 {
		return members
	}
	out := members[:0]
	for _, m := range members {
		cm := r.Canonical(m)
		if !slices.Contains(out, cm) {
			out = append(out, cm)
		}
	}
	return out
}
