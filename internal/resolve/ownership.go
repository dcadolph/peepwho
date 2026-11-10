package resolve

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// OwnerDrift is a topic whose declared owner is not the person doing the work.
type OwnerDrift struct {
	// Topic is the area of ownership.
	Topic string `json:"topic"`
	// Declared are the owners of record, by name, such as from CODEOWNERS.
	Declared []string `json:"declared"`
	// Actual is the strongest actual expert by activity, by name.
	Actual string `json:"actual"`
	// Why says how the owner of record stands in relation to their own area:
	// whether they have done no recorded work at all, none in this area, or
	// some but less than whoever leads it. The three are different problems and
	// call for different things to be done about them.
	Why string `json:"why"`
	// ActualID is the actual expert's canonical id.
	ActualID string `json:"actualId"`
	// DeclaredIDs are the owners of record by canonical id, in the same order
	// as Declared, so a reader can be taken to the person rather than shown a
	// name they then have to go and look up.
	DeclaredIDs []string `json:"declaredIds,omitempty"`
	// ActualIDs is every identity the actual owner is known by, canonical
	// first. A person commits under more than one address and more than one
	// name, and anything checking a finding against the history needs the whole
	// set or it splits the person the way the raw log does.
	ActualIDs []string `json:"actualIds,omitempty"`
}

// How an owner of record stands in relation to the area they own.
const (
	standingSilent   = "owner has no recorded work"
	standingUnworked = "owner works elsewhere, not here"
	standingTrailing = "owner works here but leads less"
)

// ownerStanding says which of the three an area's owners are in. Weight a
// source of record assigned does not count as work, or every owner would look
// active in everything they own the moment a CODEOWNERS file is indexed.
func ownerStanding(ix *index.Index, owners map[model.ID]bool, topic model.ID) string {
	anyWork, workedHere := false, false
	for oid := range owners {
		p := ix.Graph.People[ix.CanonicalID(oid)]
		if p == nil {
			continue
		}
		for tid, w := range p.Topics {
			if w-p.Stated[tid] <= 0 {
				continue
			}
			anyWork = true
			if tid == topic {
				workedHere = true
			}
		}
	}
	switch {
	case !anyWork:
		return standingSilent
	case !workedHere:
		return standingUnworked
	default:
		return standingTrailing
	}
}

// automationOwner reports whether an owner id names a machine account, such as
// @grafanabot or @renovate-bot. A separator-delimited "bot" token is decisive;
// a bare trailing "bot" counts only on a long handle, because Talbot and Abbot
// are surnames and grafanabot is not.
func automationOwner(id model.ID) bool {
	h := strings.ToLower(string(id))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[i+1:]
	}
	for _, sep := range []string{"-", "_", ".", " ", "[", "]"} {
		h = strings.ReplaceAll(h, sep, " ")
	}
	for _, w := range strings.Fields(h) {
		if w == "bot" || (strings.HasSuffix(w, "bot") && len(w) >= 8) {
			return true
		}
	}
	return false
}

// teamSuffixes are the decorations a CODEOWNERS squad slug carries that an org
// chart's team name does not: alerting-squad is the team Alerting.
var teamSuffixes = []string{"squad", "team", "group", "crew", "eng", "engineering", "dev", "developers"}

// squadTeam matches a squad owner id against the org chart's teams by name:
// codeowners:grafana/alerting-squad is the team named Alerting when flattening
// and stripping a trailing decoration makes the two equal. It returns the team
// id, or empty when no team matches exactly one way.
func squadTeam(g *model.Graph, id model.ID) model.ID {
	s := string(id)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	slug := flattenName(s)
	if slug == "" {
		return ""
	}
	forms := map[string]bool{slug: true}
	for _, suf := range teamSuffixes {
		if rest := strings.TrimSuffix(slug, suf); rest != slug && len(rest) >= 3 {
			forms[rest] = true
		}
	}
	var target model.ID
	n := 0
	for tid, t := range g.Teams {
		tf := flattenName(t.Name)
		if tf == "" {
			continue
		}
		hit := forms[tf]
		if !hit {
			for _, suf := range teamSuffixes {
				if rest := strings.TrimSuffix(tf, suf); rest != tf && forms[rest] {
					hit = true
					break
				}
			}
		}
		if hit {
			target = tid
			n++
		}
	}
	if n != 1 {
		return ""
	}
	return target
}

// flattenName lowercases a name to its letters and digits.
func flattenName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// teamMembers returns the canonical ids of everyone on a team.
func teamMembers(ix *index.Index, tid model.ID) map[model.ID]bool {
	out := make(map[model.ID]bool)
	for id, p := range ix.Graph.People {
		if p.TeamID == tid {
			out[ix.CanonicalID(id)] = true
		}
	}
	return out
}

// allGroups reports whether every owner of record for an area is a team or a
// machine account: ownership with nobody to have held it and nobody to have
// lost it.
//
// A team cannot be displaced, because a team does not commit. Measured on
// prometheus/prometheus, whose CODEOWNERS assigns almost everything to
// @prometheus/default-maintainers: every area it owns looked as though the work
// had moved away, because the owner of record could never have done any. Both
// verifiable findings there were wrong for this one reason. WhoToAsk cannot
// enumerate a team's membership from a CODEOWNERS file, so the honest answer is
// that these areas cannot be judged, not that they have drifted.
func allGroups(owners map[model.ID]bool) bool {
	for oid := range owners {
		if !oid.IsTeam() && !automationOwner(oid) {
			return false
		}
	}
	return len(owners) > 0
}

// driftMargin is how far a challenger must be ahead of the owner of record, in
// the area itself, before the work is said to have moved.
//
// One is not a tuned number, it is what the claim means: nobody has taken an
// area off its owner while doing less of the work in it than they do. Anything
// below one is not a weak finding, it is a contradiction.
//
// Demanding more was tried and is worse. At two, five of seven findings checked
// by hand and confirmed correct were suppressed, including an area where the
// challenger had eleven changes to the owner's five. The ratios of confirmed
// findings run 0.93, 1.40, 1.61, 2.01, 2.32 against 0.11, 0.14, 0.25, 0.40 for
// confirmed bogus ones, so no threshold separates them cleanly and one is where
// the reasoning, rather than the data, puts the line.
const driftMargin = 1.0

// minDriftEvidence is how much work in the area a challenger needs before the
// claim is worth making at all. At the topic weight of three per commit, this
// asks for at least two focused commits: one change is a visit, not a claim.
//
// Verified against raw git on three repositories at once by
// eval/verify_drift.py, with people keyed by email and changes counted once per
// commit: every reported finding names the top focused committer of its area,
// at this floor and above it (home-assistant 71 of 71, prometheus 4 of 4,
// cli/cli reports nothing and nothing is wrong). The floor no longer buys
// precision; it sets how much evidence a claim rests on, and raising it to
// three commits only shrinks the report.
const minDriftEvidence = 4.5

// workIn is how much real work somebody has done in one area. Weight a source
// of record assigned does not count, or an owner would look active in an area
// the moment their name was written next to it.
func workIn(ix *index.Index, who model.ID, topic model.ID) float64 {
	p := ix.Graph.People[ix.CanonicalID(who)]
	if p == nil {
		return 0
	}
	// Work done inside the area, not a file elsewhere that carries its name.
	// Home Assistant names a platform file after the platform it implements, so
	// editing voip/assist_satellite.py is real work on assist-satellite and says
	// nothing about who owns the integration called assist_satellite. Counting
	// it handed that integration to somebody with no change in it at all, over
	// the eight-commit maintainer.
	//
	// A source that reports no paths has no Direct at all, and falling back to
	// the whole weight keeps ownership working for it rather than reporting
	// that nobody owns anything.
	if len(p.Direct) > 0 {
		return p.Direct[topic]
	}
	if w := p.Topics[topic] - p.Stated[topic]; w > 0 {
		return w
	}
	return 0
}

// displaced reports whether challenger has taken an area off its owners of
// record, and is the whole of what drift means.
//
// It is a comparison between the incumbent and the challenger, not a contest
// the whole company enters. Ranking everybody and calling the winner the owner
// reported drift wherever somebody happened to edge ahead: on
// home-assistant/core it fired on areas where the owner and the challenger had
// one change each, which is a coin toss rather than a finding. An earlier
// attempt at a fix applied a floor to every candidate, which threw out the
// incumbent along with the noise and guaranteed drift whenever an owner's own
// work was modest. It won 16 areas and lost 35.
//
// An owner who has never worked in the area at all is not handled here. That is
// paper ownership, which ownerStanding reports separately and which needs no
// margin: there is nothing to displace.
func displaced(ix *index.Index, owners map[model.ID]bool, challenger model.ID, topic model.ID) bool {
	var most float64
	for oid := range owners {
		if w := workIn(ix, oid, topic); w > most {
			most = w
		}
	}
	if most <= 0 {
		return true
	}
	mine := workIn(ix, challenger, topic)
	// Enough to be worth saying. Below this the finding is a coin toss between
	// two people with a change each, and reporting it as ownership having moved
	// is a claim the evidence cannot carry.
	if mine < minDriftEvidence {
		return false
	}
	// Strictly more. An owner matched change for change has not lost the area,
	// and reporting a dead heat as a transfer of ownership named the tied
	// challenger over the tied owner on the strength of nothing.
	return mine > most*driftMargin
}

// actualOwnerOf picks whoever has done the most direct work in the area, over
// everyone in the graph. The risk view's expert list cannot be used for this:
// it is capped at five for display and ranked by affinity share, and affinity
// on a common word is diluted by everyone who has ever typed it, so the person
// with the most focused work in the area is routinely not on it. On a real
// project the top committer of an area, eleven focused changes, was invisible
// to ownership for exactly that reason. The capped list keeps its job, which is
// being read; deciding ownership is not it.
//
// It returns false when nobody has direct work in the area, and the caller
// falls back to the discounted ranking over the displayed experts, which is
// the best available where no source can tell focused work from sweeping.
func actualOwnerOf(ix *index.Index, topic model.ID) (RiskExpert, bool) {
	var best model.ID
	var bestScore, bestPull float64
	for id, p := range ix.Graph.People {
		if len(p.Direct) == 0 {
			continue
		}
		canon := ix.CanonicalID(id)
		score := p.Direct[topic]
		if score <= 0 {
			continue
		}
		// Ties are real: two people with the same number of focused commits.
		// They break on overall engagement with the subject, work only, which
		// reaches past commits into everything else the person has done around
		// it. Breaking them by identifier handed an area to whoever sorted
		// first, and on a real project that named the co-leader whose edge the
		// history does not support.
		pull := p.Topics[topic] - p.Stated[topic]
		if score > bestScore ||
			(score == bestScore && pull > bestPull) ||
			(score == bestScore && pull == bestPull && canon < best) {
			best, bestScore, bestPull = canon, score, pull
		}
	}
	if bestScore <= 0 {
		return RiskExpert{}, false
	}
	return RiskExpert{ID: string(best), Name: personName(ix, best)}, true
}

// actualOwner is the fallback for areas where no source could tell focused
// work from sweeping: raw weight would hand every area to the busiest person in
// the organization, so each candidate is discounted by the square root of their
// whole career. A proxy for a distinction the data cannot draw, kept only where
// it is the best available.
func actualOwner(ix *index.Index, tr TopicRisk) RiskExpert {
	topic := model.ID(tr.Topic)
	best, bestScore := tr.Experts[0], -1.0
	for _, e := range tr.Experts {
		p := ix.Graph.People[ix.CanonicalID(model.ID(e.ID))]
		if p == nil {
			continue
		}
		here := p.Topics[topic] - p.Stated[topic]
		if here <= 0 {
			continue
		}
		// Summed in identifier order. Float addition is not associative and Go
		// randomizes map iteration, so accumulating a career total as the map
		// yields it gives a different last bit each run, and this total is a
		// divisor for every score below it.
		var total float64
		for _, tid := range slices.Sorted(maps.Keys(p.Topics)) {
			if work := p.Topics[tid] - p.Stated[tid]; work > 0 {
				total += work
			}
		}
		if total <= 0 {
			continue
		}
		if score := here / math.Sqrt(total); score > bestScore {
			best, bestScore = e, score
		}
	}
	return best
}

// OwnershipReport is what a source of record says about ownership set against
// what the work says: the areas that moved, and the count they moved out of.
// The count is the finding. A list of drifted areas invites the reading that
// these are the exceptions, and in every organization measured so far they are
// not: most declared ownership does not match who does the work.
type OwnershipReport struct {
	// Declared is how many areas have an owner of record at all.
	Declared int `json:"declared"`
	// Held is how many of those the owner of record still leads.
	Held int `json:"held"`
	// Drift are the areas whose strongest expert is somebody else, by topic.
	Drift []OwnerDrift `json:"drift"`
	// Silent counts drifted areas whose owner of record has no recorded work
	// anywhere. Read it carefully: they may have left, but a source of record
	// names people by handle and an activity source names them by address, so
	// an owner whose handle was never linked to their commits looks exactly the
	// same. It is the bucket to check an alias file against before believing.
	Silent int `json:"silent"`
	// Unworked counts drifted areas whose owner is active elsewhere but has
	// never worked in the area they own. Ownership there is paper only.
	Unworked int `json:"unworked"`
	// GroupOwned counts areas owned only by teams or automation, which drift
	// cannot judge and does not list.
	GroupOwned int `json:"groupOwned"`
	// Trailing counts drifted areas whose owner does work in them, but less
	// than whoever now leads.
	Trailing int `json:"trailing"`
}

// Drifted is how many declared areas have moved to somebody else.
func (r OwnershipReport) Drifted() int { return len(r.Drift) }

// Share is the fraction of declared ownership that no longer matches who does
// the work, zero to one. It is zero when nothing was declared.
func (r OwnershipReport) Share() float64 {
	judged := r.Declared - r.GroupOwned
	if judged <= 0 {
		return 0
	}
	return float64(len(r.Drift)) / float64(judged)
}

// Ownership compares declared ownership, from a source of record such as
// CODEOWNERS, against who actually has the expertise, so a reader can see where
// ownership on paper has drifted from where the work happens. It is
// deterministic over the graph, no model required.
//
// It can only speak for what was indexed: an owner whose work lives somewhere
// whotoask was not pointed at looks like an owner who does nothing, so the share
// it reports is an upper bound on drift rather than a measurement of it.
func Ownership(ix *index.Index) OwnershipReport {
	report := OwnershipReport{}
	for _, a := range OwnedAreas(ix) {
		report.Declared++
		switch a.Standing {
		case StandingHeld:
			report.Held++
			continue // the declared owner is the one doing the work: no drift
		case StandingGroup:
			report.GroupOwned++
			continue // no person to judge; counted so the summary is honest
		case standingSilent:
			report.Silent++
		case standingUnworked:
			report.Unworked++
		default:
			report.Trailing++
		}
		drift := OwnerDrift{
			Topic: a.Topic, Declared: a.Declared, DeclaredIDs: a.DeclaredIDs,
			Actual: a.Actual, ActualID: a.ActualID, Why: a.Standing,
		}
		if p := ix.Graph.People[model.ID(a.ActualID)]; p != nil {
			drift.ActualIDs = append(drift.ActualIDs, a.ActualID)
			for _, alt := range p.Identities {
				drift.ActualIDs = append(drift.ActualIDs, string(alt))
			}
		}
		report.Drift = append(report.Drift, drift)
	}
	sort.Slice(report.Drift, func(i, j int) bool { return report.Drift[i].Topic < report.Drift[j].Topic })
	return report
}

// StandingHeld marks an area whose owner of record is also the person doing the
// work. The other three standings say why an area is not held.
const StandingHeld = "owner leads their own area"

// StandingGroup marks an area owned only by teams or automation, which drift
// can say nothing about: there is no person to have held it or lost it.
const StandingGroup = "owned by a team or automation"

// OwnedArea is one declared area set against who actually does the work there.
type OwnedArea struct {
	// Topic is the area a source of record assigned an owner.
	Topic string `json:"topic"`
	// Standing is StandingHeld, or which of the three ways the owner of record
	// is not the person leading it.
	Standing string `json:"standing"`
	// Declared names the owners of record, alphabetically, and DeclaredIDs
	// their canonical ids in the same order.
	Declared    []string `json:"declared,omitempty"`
	DeclaredIDs []string `json:"declaredIds,omitempty"`
	// Actual is the person the work says leads it, and ActualID their id.
	Actual   string `json:"actual,omitempty"`
	ActualID string `json:"actualId,omitempty"`
}

// Answerable reports whether this area can fairly be used to judge ranking. An
// owner with no recorded work in the area could not be named first by any
// ranking, so scoring those measures how much was indexed rather than how well
// it ranks.
func (a OwnedArea) Answerable() bool {
	return a.Standing == StandingHeld || a.Standing == standingTrailing
}

// OwnedAreas returns every area with an owner of record, sorted by area, set
// against who the work says leads it.
//
// Ownership summarizes this into counts. It is exported separately because a
// count cannot answer whether two indexes did better or worse on the SAME
// areas, and comparing overall scores across indexes that answer different
// questions has been wrong every time it was tried.
func OwnedAreas(ix *index.Index) []OwnedArea {
	declared := make(map[model.ID]map[model.ID]bool)
	for id, p := range ix.Graph.People {
		canon := ix.CanonicalID(id)
		for _, t := range p.Owns {
			m := declared[t]
			if m == nil {
				m = make(map[model.ID]bool)
				declared[t] = m
			}
			m[canon] = true
		}
	}
	var out []OwnedArea
	for _, tr := range Risk(ix, 0) {
		owners := declared[model.ID(tr.Topic)]
		if len(owners) == 0 || len(tr.Experts) == 0 {
			continue
		}
		// An area owned only by teams or automation cannot usually be scored:
		// there is no person to have held it and none to have lost it. The
		// exception is a squad the org chart also knows as a team: then the
		// members are the owners of record, and the area is judged by whether
		// one of them leads it.
		if allGroups(owners) {
			members := make(map[model.ID]bool)
			for oid := range owners {
				if oid.IsTeam() {
					if tid := squadTeam(ix.Graph, oid); tid != "" {
						for m := range teamMembers(ix, tid) {
							members[m] = true
						}
					}
				}
			}
			if len(members) == 0 {
				out = append(out, OwnedArea{Topic: tr.Topic, Standing: StandingGroup})
				continue
			}
			owners = members
		}
		actual, ok := actualOwnerOf(ix, model.ID(tr.Topic))
		if !ok {
			actual = actualOwner(ix, tr)
		}
		area := OwnedArea{Topic: tr.Topic, Actual: actual.Name, ActualID: actual.ID}
		switch {
		case owners[ix.CanonicalID(model.ID(actual.ID))]:
			area.Standing = StandingHeld
		case !displaced(ix, owners, model.ID(actual.ID), model.ID(tr.Topic)):
			// Somebody else ranks higher, but not by enough to say the job has
			// changed hands. The owner of record still holds the area.
			area.Standing = StandingHeld
		default:
			area.Standing = ownerStanding(ix, owners, model.ID(tr.Topic))
		}
		type owner struct{ Name, ID string }
		list := make([]owner, 0, len(owners))
		for oid := range owners {
			list = append(list, owner{Name: personName(ix, oid), ID: string(oid)})
		}
		// Sorted by name, and the ids carried along, so the two slices stay in
		// step. Sorting the names alone and the ids separately would pair each
		// person with somebody else's identifier.
		sort.Slice(list, func(i, j int) bool {
			if list[i].Name != list[j].Name {
				return list[i].Name < list[j].Name
			}
			return list[i].ID < list[j].ID
		})
		for _, o := range list {
			area.Declared = append(area.Declared, o.Name)
			area.DeclaredIDs = append(area.DeclaredIDs, o.ID)
		}
		out = append(out, area)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}
