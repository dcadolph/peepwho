package resolve

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// concentrationCut is the share of expertise the bus factor must cover: the
// fewest people who together hold this much of a topic.
const concentrationCut = 0.8

// RiskExpert is one person's share of the expertise for a topic.
type RiskExpert struct {
	// ID is the person's canonical identifier.
	ID string `json:"id"`
	// Name is the person's display name.
	Name string `json:"name"`
	// Share is the fraction of the topic's expertise this person holds, 0 to 1.
	Share float64 `json:"share"`
	// Quiet marks an expert who has stopped working on the topic. A subject
	// resting on one person is a risk; a subject resting on one person who has
	// already moved on is a different and sharper one, and the two look
	// identical without this.
	Quiet bool `json:"quiet,omitempty"`
}

// TopicRisk is the knowledge-concentration risk for one topic: how few people
// hold most of the expertise, so a single departure is visible before it hurts.
type TopicRisk struct {
	// Topic is the area of expertise.
	Topic string `json:"topic"`
	// Level is critical, elevated, or ok.
	Level string `json:"level"`
	// Concentration is the top expert's share of the topic, 0 to 1.
	Concentration float64 `json:"concentration"`
	// BusFactor is how few people together hold most of the topic.
	BusFactor int `json:"busFactor"`
	// Weight is how much work the topic rests on, which is what separates a
	// subject the organization depends on from one that came up twice. Every
	// subject held by a single person is equally concentrated, so without this
	// the report leads with whichever of them happens to sort first by name.
	Weight float64 `json:"weight"`
	// Lead is the person the subject rests on, and LeadID their identifier.
	// It is deliberately not Experts[0]: share measures concentration, and by
	// share the few people who touch a whole code base out-hold the owner of
	// nearly every subject at once. Measured on kubernetes, reading Experts[0]
	// as the owner named the same five people for half of all subjects; the
	// breadth-discounted lead cut that in half and doubled agreement with the
	// project's own OWNERS files. Empty when nobody has done work on the
	// subject beyond being assigned it.
	Lead string `json:"lead,omitempty"`
	// LeadID is the lead's canonical identifier.
	LeadID string `json:"leadId,omitempty"`
	// Experts are the people holding the topic, by share of it, largest first.
	//
	// Share of a subject is the right measure for concentration and the wrong
	// one for who leads it; Lead is who the subject rests on.
	Experts []RiskExpert `json:"experts"`
	// Includes are the other names this same body of knowledge goes by, folded
	// in so one subject is not reported as several risks.
	Includes []string `json:"includes,omitempty"`
}

// LeadQuiet reports whether the person the subject rests on has stopped working
// on it.
//
// It reads the lead rather than the widest-ranging expert. Share ranks who has
// touched a subject, and counting a passer-by's silence as the subject's
// silence reports a different finding from the one the table beside it shows.
func (t TopicRisk) LeadQuiet() bool {
	for _, e := range t.Experts {
		if e.ID == t.LeadID {
			return e.Quiet
		}
	}
	return false
}

// Risk scores knowledge concentration for every topic across the graph: it is
// deterministic arithmetic over per-person topic affinity, no model required.
// Results are risk-first; limit caps them, and a limit of zero or less returns
// all of them.
func Risk(ix *index.Index, limit int) []TopicRisk {
	// Fragments of one compound subject are folded together first, so a body of
	// knowledge is weighed once rather than once per name it goes by.
	groups := topicGroups(ix)
	byTopic := make(map[string]map[model.ID]float64)
	aliases := make(map[string]map[string]bool)
	// People and their subjects are walked in sorted order, and the shares are
	// summed in sorted order below, because float addition is not associative
	// and Go randomizes map iteration per process. Accumulated as the map
	// happened to yield them, two runs over one unchanged repository produced
	// findings.json files differing in the last bit of about a thousand values.
	// Nothing visible moved, but a share feeds a sort comparison, so a tie
	// could break differently between runs, and an assessment a client cannot
	// reproduce byte for byte is not the thing this product claims to be.
	for _, id := range slices.Sorted(maps.Keys(ix.Graph.People)) {
		p := ix.Graph.People[id]
		for _, tid := range slices.Sorted(maps.Keys(p.Topics)) {
			w := p.Topics[tid]
			if w <= 0 {
				continue
			}
			// Knowledge risk is only meaningful for a subject the organization
			// actually has. A word mined once out of a title is not one, and
			// reporting it as critical would bury the topics that matter.
			if !ix.Graph.Topics[tid].Salient() {
				continue
			}
			key := groups[string(tid)]
			if key == "" {
				key = string(tid)
			}
			m := byTopic[key]
			if m == nil {
				m = make(map[model.ID]float64)
				byTopic[key] = m
				aliases[key] = make(map[string]bool)
			}
			m[id] += w
			if string(tid) != key {
				aliases[key][string(tid)] = true
			}
		}
	}
	lead := leads(ix)
	var out []TopicRisk
	for topic, people := range byTopic {
		var total float64
		experts := make([]RiskExpert, 0, len(people))
		for _, id := range slices.Sorted(maps.Keys(people)) {
			w := people[id]
			total += w
			experts = append(experts, RiskExpert{
				ID: string(id), Name: personName(ix, id), Share: w,
				Quiet: hasMovedOn(ix, id, model.ID(topic)),
			})
		}
		if total <= 0 {
			continue
		}
		sort.Slice(experts, func(i, j int) bool {
			if experts[i].Share != experts[j].Share {
				return experts[i].Share > experts[j].Share
			}
			return experts[i].Name < experts[j].Name
		})
		var cum float64
		bus := 0
		for i := range experts {
			// mapsum: experts and total belong to this topic's iteration alone.
			experts[i].Share /= total
			if cum < concentrationCut {
				cum += experts[i].Share
				bus++
			}
		}
		level := "ok"
		switch {
		case len(experts) == 1 || bus <= 1:
			level = "critical"
		case bus == 2:
			level = "elevated"
		}
		concentrated := level
		if len(experts) > 5 {
			experts = experts[:5]
		}
		includes := slices.Sorted(maps.Keys(aliases[topic]))
		leadName, leadID := "", ""
		if who := lead[model.ID(topic)]; who != "" {
			leadName, leadID = personName(ix, who), string(who)
		}
		out = append(out, TopicRisk{
			Topic: topic, Level: concentrated, Concentration: experts[0].Share, BusFactor: bus,
			Weight: total, Experts: experts, Includes: includes,
			Lead: leadName, LeadID: leadID,
		})
	}
	demoteSlightSubjects(out)
	sort.Slice(out, func(i, j int) bool {
		if li, lj := levelRank(out[i].Level), levelRank(out[j].Level); li != lj {
			return li < lj
		}
		// Among equally concentrated subjects, the one resting on the most work
		// is the one worth reading first: a single person holding years of a
		// subject is a different finding from a single person holding a file
		// they touched once.
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].Concentration != out[j].Concentration {
			return out[i].Concentration > out[j].Concentration
		}
		return out[i].Topic < out[j].Topic
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DepartureImpact is what the record shows resting on one person: the topics
// where they are the only expert, and the topics where they rank first but
// others remain.
type DepartureImpact struct {
	// Person is the canonical id the query resolved to, empty when unmatched.
	Person string `json:"person"`
	// Name is the person's display name.
	Name string `json:"name"`
	// Sole are topics nobody else has any expertise in.
	Sole []string `json:"sole"`
	// Top are topics where the person ranks first but others remain.
	Top []string `json:"top"`
	// Regions are the joined bodies of work they lead: subjects changed
	// together where they lead every one. Losing one of these is not the same
	// as losing the subjects in it separately, because whoever takes it on has
	// to learn the whole of it.
	Regions []Region `json:"regions,omitempty"`
}

// demoteSlightSubjects downgrades a concentrated subject that rests on less
// work than the median subject the organization has. Concentration alone makes
// every one-file corner of a code base as critical as its core: measured on
// prometheus, all thirty-four critical findings together carried one percent
// of the work, and the report led with the sole author of a CSS accordion
// while promql and tsdb, healthy at a bus factor above twenty, sat below them.
//
// A finding is not deleted by this, only levelled honestly: the subject keeps
// its weight, its bus factor, and its place in the list. What changes is the
// word an acquirer reads first.
func demoteSlightSubjects(risks []TopicRisk) {
	if len(risks) < minRisksToDemote {
		return
	}
	weights := make([]float64, len(risks))
	for i, r := range risks {
		weights[i] = r.Weight
	}
	sort.Float64s(weights)
	floor := weights[len(weights)/2]
	for i := range risks {
		if risks[i].Weight >= floor {
			continue
		}
		switch risks[i].Level {
		case "critical":
			risks[i].Level = "elevated"
		case "elevated":
			risks[i].Level = "ok"
		}
	}
}

// minRisksToDemote is the fewest subjects a median is worth computing over. A
// handful of subjects has no meaningful middle, and a young organization whose
// every subject rests on one person deserves to hear that plainly.
const minRisksToDemote = 12

// Departure reports what leaves with the person matching query: the offboarding
// view of Risk. It resolves the query by canonical id, then email, then a name
// or id substring.
func Departure(ix *index.Index, query string) DepartureImpact {
	return DepartureFrom(ix, Risk(ix, 0), Regions(ix, 0), query)
}

// DepartureFrom is Departure over findings and regions already computed. Both
// are walks of the whole graph, so a caller asking for many people at once,
// such as an assessment, scores them once and cuts every person's view from
// the same two rather than rebuilding them per person.
func DepartureFrom(
	ix *index.Index, findings []TopicRisk, regions []Region, query string,
) DepartureImpact {
	pid := findPerson(ix, query)
	imp := DepartureImpact{Person: string(pid)}
	if pid == "" {
		return imp
	}
	if p := ix.Graph.People[pid]; p != nil {
		imp.Name = p.Name
	}
	// Who leads a subject is not who has the most raw weight in it: the people
	// who touch everything out-weigh every owner at once, and reading departure
	// off that showed a maintainer losing one subject where they really lead
	// nine. See leadOf.
	//
	// Heaviest first, because prose only has room for the first few and every
	// surface that prints this truncates. Ordered by name instead, a maintainer
	// who solely holds fifty subjects was introduced as the sole holder of
	// at-daily, at-hourly, at-midnight and at-monthly, four scheduling words out
	// of an example directory, while the systems the company runs on sat in the
	// "and 47 more".
	var sole, top []TopicRisk
	for _, tr := range findings {
		if len(tr.Experts) == 0 || model.ID(tr.LeadID) != pid {
			continue
		}
		if len(tr.Experts) == 1 {
			sole = append(sole, tr)
		} else {
			top = append(top, tr)
		}
	}
	imp.Sole = topicsByWeight(sole)
	imp.Top = topicsByWeight(top)
	for _, region := range regions {
		if model.ID(region.LeadID) == pid {
			imp.Regions = append(imp.Regions, region)
		}
	}
	return imp
}

// topicsByWeight names the subjects heaviest first, ties broken alphabetically
// so a rebuild produces the same list.
func topicsByWeight(risks []TopicRisk) []string {
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Weight != risks[j].Weight {
			return risks[i].Weight > risks[j].Weight
		}
		return risks[i].Topic < risks[j].Topic
	})
	if len(risks) == 0 {
		return nil
	}
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, r.Topic)
	}
	return out
}

// hasMovedOn reports whether somebody holds a subject but has stopped working
// on it. A source that cannot say what was recent claims nothing either way,
// which is why an absent record is not read as absence.
func hasMovedOn(ix *index.Index, id model.ID, topic model.ID) bool {
	p := ix.Graph.People[ix.CanonicalID(id)]
	if p == nil || len(p.Recent) == 0 {
		return false
	}
	return p.Topics[topic] > 0 && p.Recent[topic] == 0
}

// levelRank orders the risk levels for sorting, most severe first.
func levelRank(level string) int {
	switch level {
	case "critical":
		return 0
	case "elevated":
		return 1
	default:
		return 2
	}
}

// personName returns a person's display name, or their id when unnamed.
func personName(ix *index.Index, id model.ID) string {
	if p := ix.Graph.People[id]; p != nil && p.Name != "" {
		return p.Name
	}
	return string(id)
}

// findPerson resolves a query to a canonical person id: an exact id or email
// first, then the first person whose id, email, or name contains it.
func findPerson(ix *index.Index, query string) model.ID {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return ""
	}
	if _, ok := ix.Graph.People[model.ID(q)]; ok {
		return model.ID(q)
	}
	var best model.ID
	for id, p := range ix.Graph.People {
		if strings.Contains(strings.ToLower(string(id)), q) ||
			strings.Contains(strings.ToLower(p.Email), q) ||
			strings.Contains(strings.ToLower(p.Name), q) {
			if best == "" || id < best {
				best = id
			}
		}
	}
	return best
}
