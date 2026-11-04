package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// summaryListed caps how many entries each summary section prints; the full
// data always sits beside it in the JSON files.
const summaryListed = 5

// assessCounts is what the summary leads with, each figure a direct count of
// rows in the files beside it.
//
// Three different things were once all called "rests on one person", and the
// summary reported whichever came to hand: the severity level, the bus factor,
// and the number of people in the record. On a real repository those are 2, 34,
// and 6. A reader who counts the files finds a number the summary does not
// have, which is the fastest way to lose an assessment.
type assessCounts struct {
	// Sole is subjects with exactly one person in the record.
	Sole int
	// BusOne and BusTwo are subjects where one or two people hold the majority
	// of the work, whoever else has touched it.
	BusOne, BusTwo int
	// Critical and Elevated are the severity levels, which weigh concentration
	// against how much work the subject carries.
	Critical, Elevated int
	// Quiet is subjects whose lead has stopped working on them.
	Quiet int
}

// countAssess tallies the summary figures over every scored subject.
func countAssess(findings []resolve.TopicRisk) assessCounts {
	var c assessCounts
	for _, f := range findings {
		switch f.Level {
		case "critical":
			c.Critical++
		case "elevated":
			c.Elevated++
		}
		switch f.BusFactor {
		case 1:
			c.BusOne++
		case 2:
			c.BusTwo++
		}
		if len(f.Experts) == 1 {
			c.Sole++
		}
		if f.LeadQuiet() {
			c.Quiet++
		}
	}
	return c
}

// assessSummary renders the executive summary a diligence reader opens first:
// the overall shape, the findings that matter, the questions the human team
// should put to management, and the actions the findings suggest. Every
// number in it is computed from the same data the JSON files carry; nothing
// is asserted that a reader cannot trace.
func assessSummary(
	ix *index.Index, findings []resolve.TopicRisk, ownership resolve.OwnershipReport,
	departures []resolve.DepartureImpact, spans []resolve.Span, regions []resolve.Region,
	places []resolve.Place, split []resolve.SplitName, successors []personSuccession,
	checks []resolve.CheckCommand, ramp resolve.Ramp,
) string {
	var b strings.Builder
	c := countAssess(findings)

	fmt.Fprintf(&b, "# Knowledge continuity summary\n\n")
	fmt.Fprintf(&b, "Scope: %d %s, %d subjects scored, sources: %s.\n\n",
		len(ix.Graph.People), people(len(ix.Graph.People)), len(findings),
		strings.Join(ix.SourceNames(), ", "))
	b.WriteString(concentrationLine(c))
	if c.Quiet > 0 {
		fmt.Fprintf(&b,
			"Sharper than any of those: %d subjects rest on somebody who has already "+
				"stopped working on them.\n", c.Quiet)
	}
	if ownership.Declared > 0 {
		fmt.Fprintf(&b,
			"Declared ownership: %d areas have an owner of record; the evidence agrees "+
				"in %d and points elsewhere in %d.\n", ownership.Declared, ownership.Held,
			len(ownership.Drift))
	}
	if len(regions) > 0 {
		fmt.Fprintf(&b,
			"Joined work: %d %s of subjects change together under a single lead, "+
				"so each would be picked up whole rather than a subject at a time.\n",
			len(regions), pick(len(regions), "body", "bodies"))
	}
	if len(spans) > 0 {
		fmt.Fprintf(&b,
			"Crossings: %d %s between subjects have only ever been made by one person.%s\n",
			len(spans), pick(len(spans), "connection", "connections"), coveredSpans(spans))
	}
	b.WriteString("\nThe language throughout is deliberate: peepwho measures observed work, " +
		"not competence. A finding says what the record shows and who it points to, " +
		"and every one traces to the evidence in the data files beside this summary.\n")
	b.WriteString(rampParagraph(ramp))
	b.WriteString(assessLimits(split))

	if len(places) > 0 {
		writeSystems(&b, places)
		if len(checks) > 0 {
			b.WriteString(
				"\nEvery system above is checkable without peepwho: checks.json carries " +
					"one paste-ready git command per system, counting the same window " +
					"of history this assessment read. When a row is doubted, run its " +
					"command rather than arguing with the report.\n")
		}
	}

	if len(departures) > 0 {
		b.WriteString("\n## Where one person is the whole record\n\n")
		for i, d := range departures {
			if i == summaryListed {
				fmt.Fprintf(&b, "- and %d more in departures.json\n", len(departures)-i)
				break
			}
			line := fmt.Sprintf("- %s: ", d.Name)
			switch {
			case len(d.Sole) > 0 && len(d.Top) > 0:
				line += fmt.Sprintf("sole holder of %s; leads %s",
					listSome(d.Sole), listSome(d.Top))
			case len(d.Sole) > 0:
				line += "sole holder of " + listSome(d.Sole)
			default:
				line += "leads " + listSome(d.Top)
			}
			b.WriteString(line + "\n")
			b.WriteString(successionLine(d.Person, successors))
		}
	}

	if len(successors) > 0 {
		b.WriteString("\nNearest is a statement about the record, not about ability: " +
			"of everyone indexed, this person starts closest to the work. The evidence " +
			"for each is in successors.json.\n")
	}

	if len(ownership.Drift) > 0 {
		b.WriteString("\n## Where the record and the declared owner disagree\n\n")
		drift := append([]resolve.OwnerDrift(nil), ownership.Drift...)
		sort.SliceStable(drift, func(i, j int) bool { return drift[i].Topic < drift[j].Topic })
		for i, d := range drift {
			if i == summaryListed {
				fmt.Fprintf(&b, "- and %d more in ownership.json\n", len(drift)-i)
				break
			}
			fmt.Fprintf(&b, "- %s: declared %s, observed work concentrates around %s (%s)\n",
				d.Topic, strings.Join(d.Declared, ", "), d.Actual, d.Why)
		}
	}

	questions := assessQuestions(ownership, departures, spans)
	if len(questions) > 0 {
		b.WriteString("\n## Questions for management\n\n")
		b.WriteString("Findings are the start of a conversation, not the end of one. " +
			"These are the questions the record raises; the answers belong to people.\n\n")
		for i, q := range questions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q)
		}
	}

	b.WriteString("\n## Suggested actions\n\n")
	if c.Sole == 0 && c.Critical == 0 && len(ownership.Drift) == 0 && len(spans) == 0 {
		b.WriteString("Nothing here demands action. No significant subject rests on a " +
			"single person, and declared ownership matches the observed work. A report " +
			"that can say this is the same report worth believing when it cannot.\n")
		return b.String()
	}
	if c.Sole > 0 {
		fmt.Fprintf(&b, "- Confirm retention and transition plans for the people "+
			"named under sole holdings; %d subjects have nobody else in the record at all.\n",
			c.Sole)
	}
	if c.Quiet > 0 {
		fmt.Fprintf(&b, "- Start with the %d subjects whose lead has already stopped "+
			"working on them; there the handover has happened without anybody arranging it.\n",
			c.Quiet)
	}
	if len(ownership.Drift) > 0 {
		fmt.Fprintf(&b, "- Reconcile the %d drifted areas: either the declaration "+
			"or the staffing is out of date, and which one it is changes the fix.\n",
			len(ownership.Drift))
	}
	if len(regions) > 0 {
		fmt.Fprintf(&b, "- Treat each body of joined work as one handover rather than "+
			"several; the largest spans %d subjects under one lead.\n", regions[0].Size())
	}
	if len(spans) > 0 {
		b.WriteString("- For each crossing, have a second person walk the joined " +
			"work end to end; the risk is not either area but knowing they belong together.\n")
	}
	return b.String()
}

// writeSystems lists the largest places and says plainly how well covered each
// one is.
//
// Naming three people under a heading that says what a system rests on is a
// claim about coverage, and on a wide directory it is the wrong one: measured
// on a real repository the three named under "plumbing" were the top three of a
// hundred and one, where it takes forty-eight people to cover the work. The
// names are worth printing; the count beside them is what stops a reader taking
// them for the whole of it.
func writeSystems(b *strings.Builder, places []resolve.Place) {
	fragile := resolve.MostFragile(places, summaryListed)
	b.WriteString("\n## The systems most exposed\n\n")
	b.WriteString("Ordered by how few people cover the work, not by size, " +
		"so a directory everybody has touched does not bury one that rests on a single person.\n\n")
	for _, pl := range fragile {
		names := make([]string, 0, len(pl.Holders))
		for _, h := range pl.Holders {
			names = append(names, h.Name)
		}
		if pl.People == 1 {
			fmt.Fprintf(b, "- %s: one person in the whole record, %s\n",
				pl.Dir, strings.Join(names, ", "))
			continue
		}
		fmt.Fprintf(b, "- %s: %d people in the record, %s. Most active: %s\n",
			pl.Dir, pl.People, covers(pl.Bus), strings.Join(names, ", "))
	}
	if len(places) > len(fragile) {
		fmt.Fprintf(b, "- and %d more in systems.json\n", len(places)-len(fragile))
	}
}

// people is the word for a head count of n.
func people(n int) string { return pick(n, "person", "people") }

// pick chooses the singular or plural word for n.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// concentrationLine states the three separate readings of concentration,
// naming only the ones that have anything in them.
//
// Each is a different question and they answer differently: on one repository
// the subjects with nobody else in the record, the subjects where one person
// holds most of the work, and the subjects scored critical came to 6, 34, and
// 2. Printing a zero for the ones that do not apply reads as padding, and
// printing only one of them and calling it "rests on one person" is how the
// summary came to disagree with the files beside it.
func concentrationLine(c assessCounts) string {
	var parts []string
	if c.Sole > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d %s one person in the record and nobody else",
			c.Sole, pick(c.Sole, "subject has", "subjects have")))
	}
	if more := c.BusOne - c.Sole; more > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d more rest on one person who holds most of the work", more))
	}
	if c.BusTwo > 0 {
		parts = append(parts, fmt.Sprintf("%d rest on two", c.BusTwo))
	}
	if len(parts) == 0 {
		return "Observed concentration: no subject rests on one or two people. " +
			"A report that can say this is the same report worth believing when it cannot.\n"
	}
	return fmt.Sprintf("Observed concentration: %s. Weighed against how much work "+
		"each subject carries, %d %s critical and %d elevated.\n",
		strings.Join(parts, "; "), c.Critical, pick(c.Critical, "is", "are"), c.Elevated)
}

// coveredSpans adds the clause that makes a crossing surprising, but only
// where it is true. A crossing in a code base one person wrote is not evidence
// that well-covered areas are joined by a single head, and claiming it there
// is the kind of sentence a reader checks once and then stops trusting.
func coveredSpans(spans []resolve.Span) string {
	for _, s := range spans {
		if s.Experts > 1 {
			return " The subjects themselves have other experts; the link between them does not."
		}
	}
	return ""
}

// successionLine renders who stands nearest to a departure entry's holdings,
// indented under the holding it belongs to, or the plain statement that nobody
// does. An empty line means no succession was computed at all.
func successionLine(person string, successors []personSuccession) string {
	for _, s := range successors {
		if s.Person != person {
			continue
		}
		if s.NobodyClose {
			return "  - nearest candidate: nobody stands out in the record, " +
				"which is itself the finding\n"
		}
		if s.Nearest == nil {
			return ""
		}
		line := "  - nearest candidate: " + s.Nearest.Name
		if len(s.Nearest.Reasons) > 0 {
			parts := make([]string, 0, len(s.Nearest.Reasons))
			for _, r := range s.Nearest.Reasons {
				parts = append(parts, r.Detail)
			}
			line += ": " + strings.Join(parts, "; ")
		}
		return line + "\n"
	}
	return ""
}

// rampParagraph says what this history shows about moving into an unfamiliar
// area, or says nothing at all.
//
// It is phrased as a record of what happened, never as a forecast of what a
// successor would take. The published holdout showed this model does not
// predict future activity, and a report that answered "how long would a
// handover take" would be selling exactly the claim that measurement refused.
// What it can say is how long it took the people who already did it, and how
// many started and did not carry on, which is the number a reader planning a
// handover has no other way to get.
func rampParagraph(r resolve.Ramp) string {
	if !r.Measured() {
		return ""
	}
	return fmt.Sprintf(
		"\nWhat moving into an unfamiliar area has cost here, taken from this "+
			"history rather than from any forecast. Across the %d systems named "+
			"below, somebody already working in the repository first touched an "+
			"area they had not worked in before %d times. %d of those went on to "+
			"land %d changes there, taking a median of %d days, with the middle "+
			"half between %d and %d. The remaining %d had not, by the end of the "+
			"window read. Read that last figure first: stalling is the commoner "+
			"outcome, and unlike the median it is not measured only on the people "+
			"who got through.\n",
		r.Places, r.Arrivals, r.Established, r.Changes, r.MedianDays,
		r.QuickestDays, r.SlowestDays, r.Arrivals-r.Established)
}

// covers phrases how many people it takes to cover most of a place's work.
func covers(bus int) string {
	if bus == 1 {
		return "one of them covers most of the work"
	}
	return fmt.Sprintf("%d of them cover most of the work", bus)
}

// assessLimits states what would make these numbers wrong, in the summary
// rather than a footnote. An assessment that only reports what it found invites
// the reading that what it did not find is not there.
func assessLimits(split []resolve.SplitName) string {
	var b strings.Builder
	b.WriteString("\nWhat would change these numbers: peepwho sees only what it was " +
		"pointed at, so knowledge held somewhere unindexed reads as knowledge nobody " +
		"has. Every figure here is therefore a floor on concentration rather than a " +
		"measurement of it.\n")
	if len(split) == 0 {
		return b.String()
	}
	names := make([]string, 0, summaryListed)
	for i, s := range split {
		if i == summaryListed {
			break
		}
		names = append(names, fmt.Sprintf("%s (%d records)", s.Name, len(s.IDs)))
	}
	more := ""
	if len(split) > len(names) {
		more = fmt.Sprintf(", and %d more", len(split)-len(names))
	}
	fmt.Fprintf(&b,
		"\nOne caveat runs the other way and is worth stating plainly: %d names are "+
			"held by more than one record that could not be joined into a single "+
			"person: %s%s. Each of those reads as extra cover it does not have, so "+
			"where they appear the coverage above is better than the truth. The "+
			"suggested joins are written out in aliases.json, in the format the "+
			"--aliases flag reads: review them, drop any pair that really is two "+
			"people, and run the assessment again with that file. Correcting them "+
			"can only sharpen the findings, never soften them.\n",
		len(split), strings.Join(names, ", "), more)
	return b.String()
}

// assessQuestions derives the questions a diligence team should ask, each
// grounded in a specific finding rather than a template.
func assessQuestions(
	ownership resolve.OwnershipReport, departures []resolve.DepartureImpact,
	spans []resolve.Span,
) []string {
	var out []string
	for i, d := range ownership.Drift {
		if i == summaryListed {
			break
		}
		out = append(out, fmt.Sprintf(
			"The record names %s as owner of %s, but observed work concentrates around %s. "+
				"What is the working arrangement, and which of the two should a buyer rely on?",
			strings.Join(d.Declared, ", "), d.Topic, d.Actual))
	}
	for i, d := range departures {
		if i == 3 || len(d.Sole) == 0 {
			break
		}
		out = append(out, fmt.Sprintf(
			"%s is the only person with recorded work in %s. Is that reflected in "+
				"retention planning, and where would a successor start?",
			d.Name, listSome(d.Sole)))
	}
	for i, s := range spans {
		if i == 3 {
			break
		}
		out = append(out, fmt.Sprintf(
			"%s is the only person who has worked across %s together. Who else "+
				"understands how they fit?", s.Person, strings.Join(s.Topics, " and ")))
	}
	return out
}

// listSome renders a topic list capped for prose.
func listSome(topics []string) string {
	if len(topics) <= 4 {
		return strings.Join(topics, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(topics[:4], ", "), len(topics)-4)
}
