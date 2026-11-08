package index

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/identity"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// minHandleLen keeps trivially short handles from joining anyone.
const minHandleLen = 3

// minTruncation is how many characters a handle and a name must agree on before
// one is read as a shortening of the other. Measured on a real project: at six
// the rule claims andrew-lumenfox for somebody displayed as Andre W., and at
// eight every match it makes is the right one.
const minTruncation = 8

// JoinResult reports what AutoJoin did: how many handle-only people it unioned
// to a canonical person, and which handles it left separate because their name
// or email collided across more than one distinct person.
type JoinResult struct {
	// Joined is how many handle-only people were unioned to a canonical person.
	Joined int
	// Joins records each merge with its confidence and evidence.
	Joins []Join
	// Ambiguous lists the handle ids left unresolved because they matched more
	// than one person, so a reader can add them to the alias file.
	Ambiguous []string
}

// Join records one inferred identity merge: a handle-only id folded into a
// canonical person, with how sure the merge is and the evidence for it. Joins
// by shared email or provider id are not inferences and are not recorded here.
type Join struct {
	// Alias is the handle-only id that was folded in, such as "github:kim-doe".
	Alias model.ID
	// Canonical is the person the alias was folded into.
	Canonical model.ID
	// Confidence is how sure the merge is, from 0 to 1. It is a heuristic prior
	// on the evidence, not a calibrated probability.
	Confidence float64
	// Reason names the evidence, such as "unique name match".
	Reason string
}

// Confidence priors for each kind of inferred join. A name that points at one
// person is strong; a colliding name rescued by corroboration is weaker, and a
// shared team corroborates more firmly than a couple of shared topics.
const (
	confUniqueName   = 0.9
	confEmailVariant = 0.85
	confSharedTeam   = 0.8
	confSharedTopics = 0.7
	// confHandleDomain is the confidence in a handle that matches the domain of
	// exactly one person's email, such as pat owning git@pat.dev. A domain
	// somebody registered identifies them about as firmly as a matching mailbox
	// does, and public providers can never match, since hundreds of people share
	// them and a pointer to hundreds of people points at nobody. It sits below a
	// name match because a company domain with a single person indexed against
	// it will match that company's handle, which is right when the handle is how
	// that vendor maintains its own code and wrong when it is not. The merge is
	// listed with its evidence either way, which is what makes it correctable.
	confHandleDomain = 0.85
	// confHandleTruncation is the confidence in a handle and a full name where
	// one spelling starts the other, such as gmarchetti-st and G Marchetti, or
	// rubenhal and Ruben Halvorsen. It needs minTruncation characters of
	// agreement, because six is reached by coincidence and eight is not, and it
	// needs a full name on the other side, since a lone given name would claim
	// every handle that happens to start with it.
	confHandleTruncation = 0.8
	// confHandleSurname is the confidence in a handle built from a surname:
	// the first initial plus the surname (tbekmuratov and Timur Bekmuratov),
	// the bare surname, or a surname fragment with the organization's own name
	// stuck on (zelar-grafana and Anna Zelarrondo). The surname must be long
	// enough to be distinctive and held by exactly one person, or the handle
	// stays unexplained.
	confHandleSurname = 0.75
	// confSameName is the confidence in two records being one person on the
	// strength of a full name and the work they both do. It sits lowest of the
	// inferred joins because nothing identifies the two to each other directly.
	confSameName = 0.65
)

// AutoJoin unions each handle-only person, such as github:kim-doe or
// codeowners:kim-doe, with the one canonical person whose flattened name or
// email local-part matches the handle, so kim-doe, Kim Doe, and
// kim.doe@example.com become one node without an alias file. A handle that
// matches nobody or more than one person stays separate; the alias file
// remains the override for those. It also merges people who share an email
// local part under a dotted or cross-domain variant when their names agree. It
// returns a JoinResult with the join count and the handles left ambiguous; run
// Canonicalize afterward to merge the graph.
func (ix *Index) AutoJoin() JoinResult {
	r := ix.identityResolver()
	g := ix.Graph

	// Which topics are the organization's scaffolding has to be known before any
	// merge is judged, not after. Several rules take shared subjects as the
	// evidence that two records are one person, and count only salient ones for
	// exactly that reason: agreeing on the words everybody holds proves nothing.
	// Canonicalize marks scaffolding at the end of its own run, so until this was
	// moved ahead of the passes the flag was always false while they ran and the
	// guard they document never fired once. Two people could clear a three-subject
	// bar on docs, tests, and the directory every file sits under.
	markUbiquitous(g)

	// The passes run in this order, each seeing the unions of every pass before
	// it, and the handle passes only considering handles still unexplained.
	run := newJoinRun(g, r)
	passes := []func(){
		run.wholeNameJoins,
		run.domainJoins,
		run.shortenedNameJoins,
		run.surnameHandleJoins,
		run.emailVariantJoins,
		run.rule(sameNameJoins),
		run.rule(sameSurnameJoins),
		run.rule(sameLocalJoins),
		run.rule(loginNameJoins),
		run.rule(sameNameMailboxJoins),
	}
	for _, pass := range passes {
		pass()
	}
	sort.Strings(run.blocked)
	ix.joins = pruneJoins(mergeJoins(ix.joins, run.joins), ix)
	return JoinResult{Joined: len(run.joins), Joins: run.joins, Ambiguous: run.blocked}
}

// mergeJoins overlays freshly inferred joins onto any restored from a prior
// index, keyed by alias: a re-inferred join takes the new confidence, while a
// restored join this run could not re-derive is kept so its confidence
// survives. The result is sorted by alias for a stable ledger.
func mergeJoins(restored, fresh []Join) []Join {
	if len(restored) == 0 {
		sort.Slice(fresh, func(i, j int) bool { return fresh[i].Alias < fresh[j].Alias })
		return fresh
	}
	byAlias := make(map[model.ID]Join, len(restored)+len(fresh))
	for _, j := range restored {
		byAlias[j.Alias] = j
	}
	for _, j := range fresh {
		byAlias[j.Alias] = j
	}
	out := make([]Join, 0, len(byAlias))
	for _, j := range byAlias {
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// pruneJoins drops joins whose person is gone from the graph, which happens when
// a source is replaced and its people no longer exist, so a stale merge is not
// shown or re-persisted. It also refreshes each kept join's canonical to the
// current resolution so the persisted ledger does not go stale.
func pruneJoins(joins []Join, ix *Index) []Join {
	r := ix.identityResolver()
	out := make([]Join, 0, len(joins))
	for _, j := range joins {
		canon := r.Canonical(j.Alias)
		if _, ok := ix.Graph.People[canon]; !ok {
			continue
		}
		j.Canonical = canon
		out = append(out, j)
	}
	return out
}

// minSameNameSubjects is how many real subjects two records must both hold
// before a shared name is taken as evidence they are one person.
const minSameNameSubjects = 3

// sameNameJoins merges the records of one person who appears twice. A
// maintainer commits from work and from home, or from a GitHub noreply address
// and their own, and nothing in either record links the two: no shared handle,
// no matching local part. What is left is the name they sign with and the work
// they do, and when a full name and several real subjects both agree, the two
// records are one person.
//
// It is deliberately narrow: a single-word name never qualifies, and
// scaffolding subjects that everybody holds do not count as agreement.
//
// The bar is per pair, not per name. It used to skip any name held by more than
// two records on the theory that three of a name means a common name, and that
// theory is wrong about the people this exists for. A maintainer commits from a
// laptop, a work address, a GitHub noreply, and a hostname git invented, and
// four records of one person is ordinary. Measured on a real repository the
// rule refused all five records of one maintainer, so every directory that
// rested on him alone was reported as covered by three people, which is the
// finding inverted. Judging each pair on its own evidence keeps the guard and
// drops the miscount: two genuinely different people of one name do not share
// three real subjects, and if they do, they shared them at two records too.
func sameNameJoins(g *model.Graph, r *identity.Resolver) []Join {
	byName := make(map[string][]model.ID)
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		name := strings.TrimSpace(strings.ToLower(p.Name))
		if name == "" || !strings.Contains(name, " ") {
			continue
		}
		c := r.Canonical(id)
		if !slices.Contains(byName[name], c) {
			byName[name] = append(byName[name], c)
		}
	}
	names := slices.Sorted(maps.Keys(byName))

	var out []Join
	for _, name := range names {
		ids := byName[name]
		if len(ids) < 2 {
			continue
		}
		slices.Sort(ids)
		out = append(out, joinPairs(g, r, ids, "same name and shared subjects",
			func(a, b *model.Person) bool {
				if numberedColleagues(a.Email, b.Email) {
					return false
				}
				return sharedSubjects(g, a, b) >= minSameNameSubjects
			})...)
	}
	return out
}

// joinPairs merges every pair in a group that agrees under want, and reports
// the merges it made.
//
// Each pair is judged on the two original records, never on what a previous
// merge in the same group produced, so joining one record does not drag in
// another that agreed with nothing: one person's four mailboxes collapse
// together, while a group holding two different people of one name splits the
// way the evidence splits it. Pairs are tried in sorted order, so the result
// does not depend on how the graph was walked.
func joinPairs(
	g *model.Graph, r *identity.Resolver, ids []model.ID, reason string,
	want func(a, b *model.Person) bool,
) []Join {
	var out []Join
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := r.Canonical(ids[i]), r.Canonical(ids[j])
			if a == b {
				continue
			}
			pa, pb := g.People[ids[i]], g.People[ids[j]]
			if pa == nil || pb == nil || !want(pa, pb) {
				continue
			}
			r.Union(a, b)
			target := r.Canonical(a)
			other := b
			if target == b {
				other = a
			}
			out = append(out, Join{
				Alias: other, Canonical: target,
				Confidence: confSameName, Reason: reason,
			})
		}
	}
	return out
}

// minSurname is how long a surname must be before it is distinctive enough to
// join two records on. Short ones are held by too many unrelated people.
const minSurname = 5

// commonSurnames are surnames held by so many unrelated people that sharing
// one proves nothing, whatever their length. Length was the only guard, and it
// merged a Arjun Singh with a Aditya Singh on grafana: five letters,
// most common surname on earth. The list is small and deterministic, and it
// only ever blocks a join, never makes one.
var commonSurnames = map[string]bool{
	"singh": true, "kumar": true, "sharma": true, "patel": true, "gupta": true,
	"smith": true, "jones": true, "brown": true, "davis": true, "miller": true,
	"wilson": true, "moore": true, "taylor": true, "thomas": true, "martin": true,
	"garcia": true, "martinez": true, "rodriguez": true, "lopez": true,
	"gonzalez": true, "hernandez": true, "perez": true, "sanchez": true,
	"muller": true, "schmidt": true, "schneider": true, "fischer": true,
	"weber": true, "meyer": true, "wagner": true, "nguyen": true, "silva": true,
	"santos": true, "oliveira": true, "souza": true, "ivanov": true,
	"petrov": true, "murphy": true, "kelly": true, "walsh": true, "novak": true,
	"kowalski": true, "nowak": true, "andersson": true, "johansson": true,
	"hansen": true, "jensen": true, "nielsen": true, "yilmaz": true,
	"johnson": true, "williams": true, "anderson": true, "jackson": true,
	"white": true, "harris": true, "thompson": true, "robinson": true,
	"clark": true, "lewis": true, "walker": true, "young": true, "allen": true,
	"wright": true, "scott": true, "green": true, "adams": true, "baker": true,
}

// surnameHolders counts the people whose name carries surname, including a
// one-word name that ends with it: RaghavSingh holds singh as surely as
// Arjun Singh does, and skipping him is how "exactly two Singhs" passed
// with three in the graph.
func surnameHolders(g *model.Graph, r *identity.Resolver, surname string) int {
	seen := make(map[model.ID]bool)
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		flat := flatten(p.Name)
		if !strings.HasSuffix(flat, flatten(surname)) {
			continue
		}
		seen[r.Canonical(id)] = true
	}
	return len(seen)
}

// sameSurnameJoins merges one person whose two records disagree about their
// given name. A commit signed György Szentmiklos and a CODEOWNERS entry
// resolving to George Szentmiklos are the same maintainer, and nothing joins
// them: the addresses share no local part, the handle matches neither name, and
// sameNameJoins needs the whole name to agree.
//
// It is worth catching because of what it costs. Ownership drift is a claim
// that the person written down is no longer the one doing the job, so a split
// owner holds a fraction of their own work and any passer-by clears the bar. On
// prometheus/prometheus this exact split put tsdb, an area its owner had
// committed to fifty-three times, down as having moved to somebody else.
//
// Three guards, and the third is the one that matters. The surname must be
// distinctive and held by exactly two records, or it is a common name. They
// must share several real subjects. And at least one side must be a private
// commit address rather than a real mailbox, because two colleagues who share a
// surname have two real addresses, and they are also the pair most likely to
// share subjects: they work together. Without that guard, shared subjects argue
// FOR merging the very people who must not be merged.
//
// Measured across home-assistant/core, prometheus/prometheus and cli/cli, this
// makes exactly one merge, the correct one. The candidates it declines share no
// subjects at all: Dan and Jan Hrabek, Guillaume and Kai Wexler, Miko and Eric
// Thalberg.
func sameSurnameJoins(g *model.Graph, r *identity.Resolver) []Join {
	bySurname := make(map[string][]model.ID)
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		fields := strings.Fields(strings.ToLower(strings.TrimSpace(p.Name)))
		if len(fields) < 2 {
			continue
		}
		surname := fields[len(fields)-1]
		if len(surname) < minSurname {
			continue
		}
		c := r.Canonical(id)
		if !slices.Contains(bySurname[surname], c) {
			bySurname[surname] = append(bySurname[surname], c)
		}
	}
	surnames := slices.Sorted(maps.Keys(bySurname))

	var out []Join
	for _, surname := range surnames {
		ids := bySurname[surname]
		if len(ids) != 2 {
			continue
		}
		if commonSurnames[surname] || surnameHolders(g, r, surname) != 2 {
			continue
		}
		slices.Sort(ids)
		if !privateCommitAddress(ids[0]) && !privateCommitAddress(ids[1]) {
			continue
		}
		if sharedSubjects(g, g.People[ids[0]], g.People[ids[1]]) < minSameNameSubjects {
			continue
		}
		r.Union(ids[0], ids[1])
		target := r.Canonical(ids[0])
		other := ids[1]
		if target == ids[1] {
			other = ids[0]
		}
		out = append(out, Join{
			Alias: other, Canonical: target,
			Confidence: confSameName, Reason: "same surname, one private address, and shared subjects",
		})
	}
	return out
}

// minLocal is how long an email local-part must be before it is distinctive
// enough to join two records on.
const minLocal = 5

// sameLocalJoins merges one person who writes from two mailboxes that share a
// distinctive local part: dtwarnok@gmail.com and dtwarnok@google.com are one
// maintainer, and nothing else connects them — the names may differ, the
// handle join refuses precisely because BOTH candidates match it, and the
// work splits across the two so each looks smaller than the person is. On a
// real project that split handed an area to a colleague whose records had
// merged, over the maintainer whose records had not.
//
// The guards mirror sameSurnameJoins: the local must be long enough to be a
// chosen name rather than a common word, held by exactly two email records,
// not a role mailbox, and the two must share several real subjects. Unlike the
// surname rule both sides here are real mailboxes on purpose: sharing a
// distinctive local ACROSS domains is the signature of one person's personal
// and work addresses, where sharing a surname is the signature of a family.
func sameLocalJoins(g *model.Graph, r *identity.Resolver) []Join {
	byLocal := make(map[string][]model.ID)
	for id, p := range g.People {
		if handleOnly(id) {
			continue
		}
		local := dotStrip(emailLocal(strings.ToLower(p.Email)))
		if len(local) < minLocal || util.IsRoleEmail(p.Email) {
			continue
		}
		c := r.Canonical(id)
		if !slices.Contains(byLocal[local], c) {
			byLocal[local] = append(byLocal[local], c)
		}
	}
	locals := slices.Sorted(maps.Keys(byLocal))

	var out []Join
	for _, local := range locals {
		ids := byLocal[local]
		if len(ids) != 2 {
			continue
		}
		slices.Sort(ids)
		a, b := g.People[ids[0]], g.People[ids[1]]
		// When both records display the same bare single-word name, the local
		// is that name over again and adds nothing: michael@a.com and
		// michael@b.com are two Michaels until something better says
		// otherwise, and single-word names are exactly what the name rules
		// refuse. The join earns its confidence when the mailboxes agree while
		// the names could not have: dtwarnok@gmail.com signs as dtwarnok and
		// dtwarnok@google.com signs as Darek Twarnok, and only the local
		// connects them.
		an, bn := strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)
		if strings.EqualFold(an, bn) && !strings.Contains(an, " ") {
			continue
		}
		// A full name whose given name IS the local makes the local a first
		// name, not a chosen handle: andrew@northfen.example signing Andrew Pellow
		// and andrew@quillcorp.example signing Andrew Corrin are two Andrews, and
		// their disagreeing surnames say so. Measured on esphome, this exact
		// pair merged before the guard. A single-word name equal to the local
		// stays exempt on purpose, because that is the true positive's
		// signature: dtwarnok@gmail.com signs as dtwarnok.
		if givenNameLocal(an, local) || givenNameLocal(bn, local) {
			continue
		}
		if initialSurnameCollision(an, bn, local) {
			continue
		}
		if sharedSubjects(g, a, b) < minSameNameSubjects {
			continue
		}
		r.Union(ids[0], ids[1])
		target := r.Canonical(ids[0])
		other := ids[1]
		if target == ids[1] {
			other = ids[0]
		}
		out = append(out, Join{
			Alias: other, Canonical: target,
			Confidence: confSameName, Reason: "same distinctive mailbox name at two domains, and shared subjects",
		})
	}
	return out
}

// commonGivenNames are single-word names held by so many unrelated people that
// two records carrying one prove nothing about each other. The list exists for
// the same reason commonSurnames does: a chosen handle and a common first name
// are the same shape, nothing in the graph separates them, and the cost of
// confusing the two is naming the wrong person. It only ever blocks a join.
var commonGivenNames = map[string]bool{
	"michael": true, "james": true, "john": true, "robert": true, "david": true,
	"william": true, "richard": true, "joseph": true, "thomas": true, "charles": true,
	"daniel": true, "matthew": true, "andrew": true, "joshua": true, "christopher": true,
	"paul": true, "mark": true, "george": true, "peter": true, "steven": true,
	"kevin": true, "brian": true, "edward": true, "ronald": true, "timothy": true,
	"jason": true, "jeffrey": true, "ryan": true, "jacob": true, "gary": true,
	"nicholas": true, "eric": true, "stephen": true, "jonathan": true, "larry": true,
	"justin": true, "scott": true, "brandon": true, "benjamin": true, "samuel": true,
	"frank": true, "gregory": true, "raymond": true, "alexander": true, "patrick": true,
	"jack": true, "dennis": true, "jerry": true, "tyler": true, "aaron": true,
	"mary": true, "patricia": true, "jennifer": true, "linda": true, "elizabeth": true,
	"barbara": true, "susan": true, "jessica": true, "sarah": true, "karen": true,
	"nancy": true, "lisa": true, "margaret": true, "betty": true, "sandra": true,
	"ashley": true, "dorothy": true, "kimberly": true, "emily": true, "donna": true,
	"michelle": true, "carol": true, "amanda": true, "melissa": true, "deborah": true,
	"laura": true, "rebecca": true, "sharon": true, "cynthia": true, "kathleen": true,
	"anna": true, "maria": true, "julia": true, "sofia": true, "olga": true,
	"wei": true, "li": true, "ming": true, "chen": true, "yang": true,
	"ahmed": true, "mohamed": true, "mohammed": true, "ali": true, "omar": true,
	"raj": true, "amit": true, "rahul": true, "priya": true, "anil": true,
	"alex": true, "sam": true, "chris": true, "dan": true, "mike": true,
	"tom": true, "ben": true, "nick": true, "matt": true, "joe": true,
}

// numberedColleagues reports whether two addresses are a company telling two
// people of one name apart with a number.
//
// It is the shape mailboxesAgree was written around, applied as a veto where
// only the name is doing the work. js1@example.com and js2@example.com spell the same
// thing and differ only by a digit each, which is an issued convention rather
// than one person's two mailboxes: nobody numbers themselves twice. A noreply
// account number is not that, so the login is what gets compared.
func numberedColleagues(a, b string) bool {
	la, lb := mailboxLocal(a), mailboxLocal(b)
	if la == "" || lb == "" {
		return false
	}
	return hasDigit(la) && hasDigit(lb) && digitStrip(la) == digitStrip(lb)
}

// initialSurnameCollision reports whether a shared mailbox local is an initial
// and a surname that two different people both answer to.
//
// It is the one collision a distinctive local cannot rule out, because an
// initial and a surname is a convention a company issues rather than a name
// somebody chose: John Smith at jsmith@example.com and Jane Smith at
// jsmith@partner.com share a local for the same reason they share a surname,
// and the rule's own argument, that a matching local across domains is one
// person's two addresses, does not hold for an address neither of them picked.
// Two colleagues are also the pair most likely to share subjects, so the
// evidence that would rescue the join argues for the wrong answer.
func initialSurnameCollision(a, b, local string) bool {
	surname := func(name string) string {
		words := strings.Fields(strings.ToLower(strings.TrimSpace(name)))
		if len(words) < 2 {
			return ""
		}
		return flatten(words[len(words)-1])
	}
	given := func(name string) string {
		words := strings.Fields(strings.ToLower(strings.TrimSpace(name)))
		if len(words) < 2 {
			return ""
		}
		return flatten(words[0])
	}
	sa, sb := surname(a), surname(b)
	if sa == "" || sa != sb {
		return false
	}
	ga, gb := given(a), given(b)
	if ga == "" || gb == "" || ga == gb {
		// One given name, or one of them missing: this is not two people telling
		// themselves apart, and the other guards judge it.
		return false
	}
	return local == ga[:1]+sa
}

// privateCommitAddress reports whether an id is one GitHub issues in place of a
// real mailbox, which is the shape a second record of one person usually takes.
func privateCommitAddress(id model.ID) bool {
	_, ok := util.GitHubNoreplyLogin(string(id))
	return ok
}

// sharedSubjects counts the real subjects two people both hold. Scaffolding
// that everybody in an organization holds agrees by default and proves nothing,
// so it does not count towards the total.
func sharedSubjects(g *model.Graph, a, b *model.Person) int {
	if a == nil || b == nil {
		return 0
	}
	n := 0
	for tid := range a.Topics {
		if _, ok := b.Topics[tid]; !ok {
			continue
		}
		if g.Topics[tid].Salient() {
			n++
		}
	}
	return n
}

// corroboration reports how firmly two people are the same despite an ambiguous
// name match, returning the confidence prior and the evidence, or (0, "") when
// nothing corroborates. A shared team counts for more than shared topics.
func corroboration(a, b *model.Person) (float64, string) {
	if a == nil || b == nil {
		return 0, ""
	}
	if a.TeamID != "" && a.TeamID == b.TeamID {
		return confSharedTeam, "shared team"
	}
	shared := 0
	for tid := range a.Topics {
		if _, ok := b.Topics[tid]; ok {
			shared++
			if shared >= 2 {
				return confSharedTopics, "shared topics"
			}
		}
	}
	return 0, ""
}

// Joins returns the inferred identity merges from the last AutoJoin, each with
// its confidence and evidence. Joins by shared email or provider id are not
// listed: they are identity, not inference.
func (ix *Index) Joins() []Join { return ix.joins }

// JoinsFor returns the inferred joins that resolve into the person canonical,
// so a caller can show how that person's identities were merged.
func (ix *Index) JoinsFor(canonical model.ID) []Join {
	r := ix.identityResolver()
	want := r.Canonical(canonical)
	var out []Join
	for _, j := range ix.joins {
		if r.Canonical(j.Alias) == want {
			out = append(out, j)
		}
	}
	return out
}

// CanonicalID returns the canonical identifier id resolves to, so a caller
// outside the package can group aliases under the person they belong to.
func (ix *Index) CanonicalID(id model.ID) model.ID {
	return ix.identityResolver().Canonical(id)
}

// altKeyPart returns the flattenable part of an alternate identity: the email
// local-part, the handle after a source prefix, or the whole slug.
func altKeyPart(alt model.ID) string {
	s := string(alt)
	if strings.Contains(s, "@") {
		return emailLocal(s)
	}
	if strings.IndexByte(s, ':') > 0 {
		return handlePart(alt)
	}
	return s
}

// handleOnly reports whether id is a source-prefixed handle, such as
// github:kim-doe, rather than an email or a name slug.
func handleOnly(id model.ID) bool {
	s := string(id)
	return strings.IndexByte(s, ':') > 0 && !strings.Contains(s, "@")
}

// handlePart returns the handle after the source prefix.
func handlePart(id model.ID) string {
	s := string(id)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// givenNameLocal reports whether a multi-word display name's given name is the
// local itself, which reduces the local to a common first name and disqualifies
// it as evidence.
func givenNameLocal(name, local string) bool {
	words := strings.Fields(strings.ToLower(name))
	return len(words) >= 2 && dotStrip(words[0]) == local
}

// dotStrip removes dots from an email local-part so first.last and firstlast
// compare equal.
func dotStrip(local string) string { return strings.ReplaceAll(local, ".", "") }

// emailLocal returns the part of an email before the at sign.
func emailLocal(email string) string {
	if i := strings.IndexByte(email, '@'); i >= 0 {
		return email[:i]
	}
	return email
}

// emailDomainLabel returns the registrable label of an address's domain, so
// git@pat.dev gives pat. Anything more of the domain would never match a
// handle, and anything less would match half the addresses in the graph.
func emailDomainLabel(email string) string {
	i := strings.IndexByte(email, '@')
	if i < 0 {
		return ""
	}
	parts := strings.Split(strings.ToLower(email[i+1:]), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2]
}

// flatten lowercases s and keeps only letters and digits, so kim-doe,
// kim.doe, and Kim Doe all compare equal.
func flatten(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// loginNameJoins merges the person who signs some commits with their GitHub
// login as a display name and others from an ordinary mailbox.
//
// It is a gap the handle pass cannot reach. That pass resolves people who are
// nothing but a handle, and a GitHub noreply address is not one: the build
// unions it with its own login immediately, so by the time the passes run
// there is no handle-only record left to match. What remains is two full
// people, one holding the login and one whose whole display name IS that
// login, and no rule compared them. Both were keyed under the same string and
// the key was never read.
//
// The evidence is strong because a login is chosen and unique on the very
// platform the commits came through, so a whole display name equal to one is
// not the coincidence that a shared given name is. The guards are uniqueness
// on both sides: exactly one person holds the login and exactly one other is
// named it. Anything ambiguous is left alone rather than corroborated, since a
// wrongly merged pair is the expensive mistake here and the alias file remains
// the override.
// loginNameReason names the evidence for a login-and-display-name merge.
const loginNameReason = "signs commits with the GitHub login their other address holds"

func loginNameJoins(g *model.Graph, r *identity.Resolver) []Join {
	// Every login each canonical person holds, from a noreply address or from
	// a github identity they were already unioned with.
	holders := make(map[string]map[model.ID]bool)
	named := make(map[string]map[model.ID]bool)
	note := func(m map[string]map[model.ID]bool, key string, id model.ID) {
		if len(key) < minHandleLen {
			return
		}
		set := m[key]
		if set == nil {
			set = make(map[model.ID]bool)
			m[key] = set
		}
		set[r.Canonical(id)] = true
	}
	for id, p := range g.People {
		c := r.Canonical(id)
		if login, ok := util.GitHubNoreplyLogin(p.Email); ok {
			note(holders, flatten(login), c)
		}
		for _, alt := range p.Identities {
			if source, handle, ok := strings.Cut(string(alt), ":"); ok && source == "github" {
				note(holders, flatten(handle), c)
			}
			if login, ok := util.GitHubNoreplyLogin(string(alt)); ok {
				note(holders, flatten(login), c)
			}
		}
		// Only a single-word display name can be a login. A person with a
		// spaced full name is signing as themselves, and reading it as a
		// handle would let "Kim Doe" claim the login "kimdoe" on nothing but
		// the spaces being removed.
		if name := strings.TrimSpace(p.Name); name != "" && !strings.ContainsAny(name, " \t") {
			note(named, flatten(name), c)
		}
	}

	logins := slices.Sorted(maps.Keys(holders))

	var out []Join
	for _, login := range logins {
		hs, ns := holders[login], named[login]
		if len(hs) != 1 || len(ns) == 0 {
			continue
		}
		var holder model.ID
		for id := range hs {
			holder = id
		}
		// The holder usually carries the login as its own name too, which is
		// not a second person; only the others are candidates.
		var cands []model.ID
		for id := range ns {
			if id != holder {
				cands = append(cands, id)
			}
		}
		if len(cands) != 1 {
			continue
		}
		other := cands[0]
		if r.Canonical(other) == r.Canonical(holder) {
			continue
		}
		r.Union(holder, other)
		target := r.Canonical(holder)
		alias := other
		if target == other {
			alias = holder
		}
		out = append(out, Join{
			Alias: alias, Canonical: target,
			Confidence: confUniqueName,
			Reason:     loginNameReason,
		})
	}
	return out
}

// Stem lengths for reading a mailbox as a spelling of somebody's name. Each is
// the point measured on real history where coincidence stops: four characters
// of surname and three of a given name are chosen, one or two are not.
const (
	minMailboxStem = 5
	minSurnameStem = 4
	minGivenStem   = 3
)

// sameNameMailboxReason names the evidence for a same-name mailbox merge.
const sameNameMailboxReason = "same name, and both mailboxes spell it"

// sameNameMailboxJoins merges two records of one person whose mailboxes agree
// even though their work does not overlap enough for the same-name rule.
//
// That rule wants three shared subjects, which a frequent contributor reaches
// and an occasional one never does: somebody who lands two commits from a
// laptop and one from a phone signs the same name both times and touches
// different files, so the two halves stay apart and each looks smaller than
// the person is. Measured on prometheus, six people were split this way.
//
// The evidence here is the mailbox rather than the work. Three shapes count,
// and each is guarded against the false positive it invites:
//
//   - One local continues the other, so the difference is a suffix or digits.
//     Identical locals are refused, because two people who merely share a
//     common given name at two domains is what that looks like and the local
//     rule already weighs it.
//   - Both locals spell the whole name, in either order.
//   - Both are a given name and a surname, where the given name may be
//     shortened. The shortening must run to three characters, or "asmith"
//     would claim every Smith whose given name starts with an a.
//
// A local that is merely the given name is refused throughout: two people
// called Andrew are two people, whatever their surnames spell.
func sameNameMailboxJoins(g *model.Graph, r *identity.Resolver) []Join {
	byName := make(map[string][]model.ID)
	for id, p := range g.People {
		if handleOnly(id) || util.IsRoleEmail(p.Email) {
			continue
		}
		name := strings.TrimSpace(strings.ToLower(p.Name))
		if name == "" || len(flatten(name)) < minMailboxStem {
			continue
		}
		c := r.Canonical(id)
		if !slices.Contains(byName[name], c) {
			byName[name] = append(byName[name], c)
		}
	}
	names := slices.Sorted(maps.Keys(byName))

	var out []Join
	for _, name := range names {
		ids := byName[name]
		if len(ids) < 2 {
			continue
		}
		slices.Sort(ids)
		out = append(out, joinPairs(g, r, ids, sameNameMailboxReason,
			func(a, b *model.Person) bool {
				if !strings.Contains(name, " ") && commonGivenNames[flatten(name)] {
					// The rule admits a single-word name because a chosen handle
					// is one: fancive and fancivez are one person called
					// Fancivez. A common given name has the same shape and none
					// of the meaning, so michael and michael2 would merge two
					// Michaels. No structure separates the two cases, which is
					// why this is a list, the same way common surnames are.
					return false
				}
				return mailboxesAgree(name, mailboxLocal(a.Email), mailboxLocal(b.Email))
			})...)
	}
	return out
}

// mailboxLocal reduces an address to the name it spells. A GitHub noreply
// address carries an account number in front of the login, so its local part
// read literally is digits and a plus sign and matches nothing; the login is
// the part the person chose and the only part worth comparing.
func mailboxLocal(email string) string {
	if login, ok := util.GitHubNoreplyLogin(email); ok {
		return dotStrip(login)
	}
	return dotStrip(emailLocal(strings.ToLower(email)))
}

// mailboxesAgree reports whether two email locals spell the same name well
// enough to be one person, given that both records already display that name.
//
// The trap it must not fall into is the company that issues name-based
// mailboxes. Two colleagues who share a name are told apart by a number, so
// their addresses agree on every letter and differ only in digits, which is
// the shape a careless rule reads as one person with two mailboxes. Measured
// on a simulated org of 800, ignoring digits merged 351 pairs of distinct
// people. So the two must differ in how they SPELL the name: a number is what
// separates two colleagues, and a different spelling is what one person's two
// mailboxes look like.
func mailboxesAgree(name, la, lb string) bool {
	if la == "" || lb == "" || la == lb {
		// Identical locals carry no information beyond the name itself.
		return false
	}
	// A local that is just the given name identifies a first name, not a
	// person, and must never be half of the evidence.
	if givenNameLocal(name, la) || givenNameLocal(name, lb) {
		return false
	}
	da, db := digitStrip(la), digitStrip(lb)
	if da == db {
		// The same spelling twice. It is one person only when the difference
		// is digits that one side simply does not have, as when somebody adds
		// a year to a handle. Digits on both sides are a company telling two
		// people apart, never a person telling themselves apart.
		return hasDigit(la) != hasDigit(lb)
	}
	if continues(da, db) || continues(db, da) {
		return true
	}
	// Both spell the whole name, but in opposite orders. One convention is
	// issued to everybody, so two colleagues never come out reversed.
	if spellsWholeName(name, da) && spellsWholeName(name, db) &&
		forwardName(name, da) != forwardName(name, db) {
		return true
	}
	// Both are a given name and a surname, and the given name is written out
	// on one side and shortened on the other. Identical heads would just be
	// the same spelling again.
	ha, oka := givenHead(name, da)
	hb, okb := givenHead(name, db)
	return oka && okb && ha != hb
}

// hasDigit reports whether s contains a digit.
func hasDigit(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
}

// forwardName reports whether a local spells the name in reading order rather
// than surname first.
func forwardName(name, local string) bool {
	return local == flatten(name)
}

// continues reports whether long carries on from short: the same mailbox with
// a suffix, or with digits that digit stripping already removed.
func continues(short, long string) bool {
	return len(short) >= minMailboxStem && strings.HasPrefix(long, short)
}

// spellsWholeName reports whether a local is the person's whole name run
// together, in either order, since mailboxes are issued both ways.
func spellsWholeName(name, local string) bool {
	words := strings.Fields(name)
	if len(words) < 2 {
		return false
	}
	forward := flatten(name)
	var b strings.Builder
	for i := len(words) - 1; i >= 0; i-- {
		b.WriteString(flatten(words[i]))
	}
	return local == forward || local == b.String()
}

// givenHead splits a local into the surname it ends with and the given name in
// front of it, returning that head. The head must run to minGivenStem, or "a"
// would make "asmith" the mailbox of every Smith whose given name starts with
// one.
func givenHead(name, local string) (string, bool) {
	words := strings.Fields(name)
	if len(words) < 2 {
		return "", false
	}
	given, surname := flatten(words[0]), flatten(words[len(words)-1])
	if len(surname) < minSurnameStem || !strings.HasSuffix(local, surname) {
		return "", false
	}
	head := strings.TrimSuffix(local, surname)
	if len(head) < minGivenStem || !strings.HasPrefix(given, head) {
		return "", false
	}
	return head, true
}

// digitStrip removes digits, so a mailbox and the same mailbox with a year on
// the end read as one.
func digitStrip(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < '0' || r > '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
