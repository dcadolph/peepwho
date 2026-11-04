package index

import (
	"slices"
	"strings"

	"github.com/dcadolph/peepwho/internal/identity"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// joinRun holds the state the AutoJoin passes share while they run, so each
// pass sees the unions and settled handles of every pass before it.
type joinRun struct {
	// g is the graph whose people are being joined.
	g *model.Graph
	// r is the resolver every union is recorded in.
	r *identity.Resolver
	// joins records each inferred merge in the order the passes made it.
	joins []Join
	// blocked lists the handle ids left unresolved because their name matched
	// more than one person.
	blocked []string
	// settled marks the handles already accounted for, so each later pass only
	// considers the ones still unexplained.
	settled map[model.ID]bool
}

// newJoinRun returns a run over g that records its unions in r.
func newJoinRun(g *model.Graph, r *identity.Resolver) *joinRun {
	return &joinRun{g: g, r: r, settled: make(map[model.ID]bool)}
}

// settle unions the handle id into target, marks it settled, and records the
// join with its confidence and reason.
func (jr *joinRun) settle(id, target model.ID, conf float64, reason string) {
	jr.r.Union(target, id)
	jr.settled[id] = true
	jr.joins = append(jr.joins, Join{Alias: id, Canonical: target, Confidence: conf, Reason: reason})
}

// rule adapts a join rule that works on the graph and resolver directly into a
// pass that appends the joins it makes to the run.
func (jr *joinRun) rule(f func(*model.Graph, *identity.Resolver) []Join) func() {
	return func() { jr.joins = append(jr.joins, f(jr.g, jr.r)...) }
}

// nameKeys indexes every canonical person by all of their flattened forms:
// display name, every email local-part, and each joined identity (email or
// handle), so a source-prefixed handle can match on any of them, not just one
// name.
func nameKeys(g *model.Graph, r *identity.Resolver) map[string]map[model.ID]bool {
	byKey := make(map[string]map[model.ID]bool)
	addKey := func(key string, id model.ID) {
		if len(key) < minHandleLen {
			return
		}
		set := byKey[key]
		if set == nil {
			set = make(map[model.ID]bool)
			byKey[key] = set
		}
		set[r.Canonical(id)] = true
	}
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		addKey(flatten(p.Name), id)
		if p.Email != "" {
			addKey(flatten(emailLocal(p.Email)), id)
		}
		for _, alt := range p.Identities {
			addKey(flatten(altKeyPart(alt)), id)
		}
	}
	return byKey
}

// distinctCanonical returns the distinct canonical people in set, minus self.
func distinctCanonical(set map[model.ID]bool, r *identity.Resolver, self model.ID) []model.ID {
	if set == nil {
		return nil
	}
	seen := make(map[model.ID]bool)
	var out []model.ID
	for c := range set {
		cc := r.Canonical(c)
		if cc == self || seen[cc] {
			continue
		}
		seen[cc] = true
		out = append(out, cc)
	}
	return out
}

// pickCandidate chooses which of cands the handle person hp joins. One
// candidate is a unique name match. Several are ambiguous, and an ambiguous
// name must never silently collapse two people, so it merges only when exactly
// one candidate corroborates by a shared team or two shared topics. It reports
// false when no candidate can be chosen.
func pickCandidate(g *model.Graph, hp *model.Person, cands []model.ID) (model.ID, float64, string, bool) {
	if len(cands) == 1 {
		return cands[0], confUniqueName, "unique name match", true
	}
	var matches []model.ID
	var mConf float64
	var mReason string
	for _, c := range cands {
		if cf, why := corroboration(hp, g.People[c]); cf > 0 {
			matches = append(matches, c)
			mConf, mReason = cf, why
		}
	}
	if len(matches) != 1 {
		return "", 0, "", false
	}
	return matches[0], mConf, "name and " + mReason, true
}

// wholeNameJoins unions each handle-only person with the one canonical person
// whose flattened name, email local-part, or joined identity is the whole
// handle. A handle that matches several people and cannot be corroborated to
// one of them is recorded as blocked.
func (jr *joinRun) wholeNameJoins() {
	byKey := nameKeys(jr.g, jr.r)
	for id, hp := range jr.g.People {
		if !handleOnly(id) {
			continue
		}
		key := flatten(handlePart(id))
		if len(key) < minHandleLen {
			continue
		}
		cands := distinctCanonical(byKey[key], jr.r, jr.r.Canonical(id))
		if len(cands) == 0 {
			continue
		}
		target, conf, reason, ok := pickCandidate(jr.g, hp, cands)
		if !ok {
			jr.blocked = append(jr.blocked, string(id))
			continue
		}
		jr.settle(id, target, conf, reason)
	}
}

// domainKeys indexes every canonical person with an email by the flattened
// label of its domain.
func domainKeys(g *model.Graph, r *identity.Resolver) map[string]map[model.ID]bool {
	byDomain := make(map[string]map[model.ID]bool)
	for id, p := range g.People {
		if handleOnly(id) || p.Email == "" {
			continue
		}
		key := flatten(emailDomainLabel(p.Email))
		if len(key) < minHandleLen {
			continue
		}
		set := byDomain[key]
		if set == nil {
			set = make(map[model.ID]bool)
			byDomain[key] = set
		}
		set[r.Canonical(id)] = true
	}
	return byDomain
}

// domainJoins unions a handle nobody's name matched with the one person whose
// address is on a domain spelled like the handle. A maintainer who commits as
// git@pat.dev is the owner writing themselves down as pat, and no name or
// mailbox ties the two together. This is what leaves declared owners looking
// like they do no work at all.
func (jr *joinRun) domainJoins() {
	byDomain := domainKeys(jr.g, jr.r)
	for id := range jr.g.People {
		if !handleOnly(id) || jr.settled[id] {
			continue
		}
		self := jr.r.Canonical(id)
		key := flatten(handlePart(id))
		if len(key) < minHandleLen {
			continue
		}
		var target model.ID
		n := 0
		for c := range byDomain[key] {
			if cc := jr.r.Canonical(c); cc != self {
				target = cc
				n++
			}
		}
		// More than one person on that domain means it is a provider or a
		// company, and points at nobody in particular.
		if n != 1 {
			continue
		}
		jr.settle(id, target, confHandleDomain, "handle matches email domain")
	}
}

// fullNameKey is one canonical person's flattened full name.
type fullNameKey struct {
	// id is the canonical person the name belongs to.
	id model.ID
	// flat is the flattened full name.
	flat string
}

// fullNameKeys lists every non-handle person with a full name of at least
// minTruncation flattened characters. A full name only: a lone given name
// would claim every handle starting with it, and there is usually exactly one
// person called Michael.
func fullNameKeys(g *model.Graph, r *identity.Resolver) []fullNameKey {
	var fullNames []fullNameKey
	for id, p := range g.People {
		if handleOnly(id) || !strings.Contains(strings.TrimSpace(p.Name), " ") {
			continue
		}
		key := flatten(p.Name)
		if len(key) < minTruncation {
			continue
		}
		fullNames = append(fullNames, fullNameKey{id: r.Canonical(id), flat: key})
	}
	return fullNames
}

// shortenedNameJoins unions a handle that is a person's full name with the end
// cut off, or with something stuck on it. Neither spelling matches whole, so
// the passes before it do not see them as one person. The match must land on
// exactly one person.
func (jr *joinRun) shortenedNameJoins() {
	fullNames := fullNameKeys(jr.g, jr.r)
	for id := range jr.g.People {
		if !handleOnly(id) || jr.settled[id] {
			continue
		}
		self := jr.r.Canonical(id)
		key := flatten(handlePart(id))
		if len(key) < minTruncation {
			continue
		}
		var target model.ID
		n := 0
		for _, cand := range fullNames {
			if cand.id == self {
				continue
			}
			if !strings.HasPrefix(key, cand.flat) && !strings.HasPrefix(cand.flat, key) {
				continue
			}
			if cand.id != target {
				target = cand.id
				n++
			}
		}
		if n != 1 {
			continue
		}
		jr.settle(id, target, confHandleTruncation, "handle is a shortened name")
	}
}

// surnameKey is one canonical person's distinctive surname forms.
type surnameKey struct {
	// id is the canonical person the surname belongs to.
	id model.ID
	// initialSurname is the first initial followed by the surname, flattened.
	initialSurname string
	// surname is the flattened surname.
	surname string
}

// surnameKeys lists every non-handle person with at least two name words and a
// surname long enough to be distinctive and not among the common ones.
func surnameKeys(g *model.Graph, r *identity.Resolver) []surnameKey {
	var surnames []surnameKey
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		words := strings.Fields(strings.TrimSpace(p.Name))
		if len(words) < 2 {
			continue
		}
		sur := flatten(words[len(words)-1])
		first := flatten(words[0])
		if len(sur) < minSurname || first == "" || commonSurnames[sur] {
			continue
		}
		surnames = append(surnames, surnameKey{
			id: r.Canonical(id), initialSurname: first[:1] + sur, surname: sur,
		})
	}
	return surnames
}

// orgTokens learns the organization's own name from its team handles, such as
// grafana from codeowners:grafana/alerting-squad. It is noise a personal handle
// may carry: @zelar-grafana at grafana/grafana.
func orgTokens(g *model.Graph) map[string]bool {
	orgs := make(map[string]bool)
	for id := range g.People {
		h := handlePart(id)
		if org, _, ok := strings.Cut(h, "/"); ok {
			if t := flatten(org); len(t) >= minHandleLen {
				orgs[t] = true
			}
		}
	}
	return orgs
}

// handleForms returns the flattened handle key followed by each spelling of it
// with an organization token cut from its end or its start, keeping only what
// is still at least minSurname long.
func handleForms(key string, orgs map[string]bool) []string {
	forms := []string{key}
	for org := range orgs {
		if rest := strings.TrimSuffix(key, org); rest != key && len(rest) >= minSurname {
			forms = append(forms, rest)
		}
		if rest := strings.TrimPrefix(key, org); rest != key && len(rest) >= minSurname {
			forms = append(forms, rest)
		}
	}
	return forms
}

// surnameReason reports why the handle form k names cand, or "" when it does
// not. Stripped says k had the organization's name cut from it.
func surnameReason(k string, stripped bool, cand surnameKey) string {
	switch {
	case k == cand.initialSurname:
		return "handle is an initial and surname"
	case k == cand.surname:
		return "handle is a surname"
	case stripped && len(k) >= minSurname && strings.HasPrefix(cand.surname, k):
		// A bare fragment is too loose on its own; carrying the
		// organization's name is what says it was a chosen handle.
		return "handle is a surname fragment and the organization's name"
	}
	return ""
}

// matchSurname finds the person the handle forms name by a surname, skipping
// self. It returns the last person matched, how many times the matched person
// changed while scanning, and the reason of the last match. A count other than
// one means the forms do not point at exactly one person.
func matchSurname(key string, forms []string, self model.ID, surnames []surnameKey) (model.ID, int, string) {
	var target model.ID
	n := 0
	reason := ""
	for _, k := range forms {
		stripped := k != key
		for _, cand := range surnames {
			if cand.id == self {
				continue
			}
			why := surnameReason(k, stripped, cand)
			if why == "" {
				continue
			}
			if cand.id != target {
				target = cand.id
				n++
			}
			reason = why
		}
	}
	return target, n, reason
}

// surnameHandleJoins unions a handle built from a surname, which matches
// nothing in the passes before it: tbekmuratov is neither the whole of
// timurbekmuratov nor a prefix of it, and zelar-grafana buries a surname
// fragment under the organization's own name. Three surname forms are tried,
// always requiring the surname to be distinctive and the match to land on
// exactly one person.
func (jr *joinRun) surnameHandleJoins() {
	surnames := surnameKeys(jr.g, jr.r)
	orgs := orgTokens(jr.g)
	for id := range jr.g.People {
		if !handleOnly(id) || jr.settled[id] {
			continue
		}
		self := jr.r.Canonical(id)
		key := flatten(handlePart(id))
		if len(key) < minSurname {
			continue
		}
		target, n, reason := matchSurname(key, handleForms(key, orgs), self, surnames)
		if n != 1 {
			continue
		}
		jr.settle(id, target, confHandleSurname, reason)
	}
}

// localKeys groups the distinct canonical people by their email local part
// with dots dropped, leaving out role mailboxes. Plus-tags are already folded
// by NormalizeEmail.
func localKeys(g *model.Graph, r *identity.Resolver) map[string][]model.ID {
	byLocal := make(map[string][]model.ID)
	for id, p := range g.People {
		if p.Email == "" || util.IsRoleEmail(p.Email) {
			continue
		}
		key := dotStrip(emailLocal(strings.ToLower(p.Email)))
		if len(key) < minHandleLen {
			continue
		}
		canon := r.Canonical(id)
		if !slices.Contains(byLocal[key], canon) {
			byLocal[key] = append(byLocal[key], canon)
		}
	}
	return byLocal
}

// emailVariantJoins merges people who are one person under an email variant:
// their email local parts match once dots are dropped (first.last vs
// firstlast, or the same mailbox on a primary and an onmicrosoft domain),
// confirmed by an identical display name so two different people who share a
// local part are never collapsed.
func (jr *joinRun) emailVariantJoins() {
	for key, group := range localKeys(jr.g, jr.r) {
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				jr.emailVariantPair(key, group[i], group[j])
			}
		}
	}
}

// emailVariantPair merges the people behind ga and gb, who share the dotless
// local part key, when both carry the same full name and that name is key.
func (jr *joinRun) emailVariantPair(key string, ga, gb model.ID) {
	a, b := jr.r.Canonical(ga), jr.r.Canonical(gb)
	if a == b {
		return
	}
	pa, pb := jr.g.People[a], jr.g.People[b]
	if pa == nil || pb == nil {
		return
	}
	name := flatten(pa.Name)
	// The local part must BE the full name (john.smith, not a bare
	// first name), so two different people who only share a first
	// name at different domains are never collapsed. A name of one
	// word is that bare first name however the address is spelled,
	// and two people called Michael at two domains are not evidence
	// of one person with two addresses.
	if name == "" || name != flatten(pb.Name) || name != key {
		return
	}
	if !strings.Contains(strings.TrimSpace(pa.Name), " ") {
		return
	}
	jr.r.Union(a, b)
	target := jr.r.Canonical(a)
	other := b
	if target == b {
		other = a
	}
	jr.joins = append(jr.joins, Join{
		Alias: other, Canonical: target, Confidence: confEmailVariant, Reason: "matching email variant",
	})
}
