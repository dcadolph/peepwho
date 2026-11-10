// Package report renders whotoask findings as a self-contained HTML document.
// The point of the format is that it travels: a manager can forward the file
// to somebody who has never installed whotoask, and it still explains itself.
package report

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"

	"github.com/dcadolph/whotoask/internal/resolve"
)

//go:embed risk.html
var files embed.FS

// Brief is a knowledge-risk report: the finding, plus everything a reader
// needs to judge it without whotoask in front of them.
type Brief struct {
	// Generated is when the report was written.
	Generated time.Time
	// People is how many people the index holds.
	People int
	// Scored is how many subjects were scored, which is not the same as how
	// many are listed: the table may show only the worst of them.
	Scored int
	// Sources names the connectors the index was built from, which is what
	// tells a reader how much of the company the finding actually saw.
	Sources []string
	// Risks are the subjects listed, most concentrated first.
	Risks []resolve.TopicRisk
	// Totals counts every scored subject, not only the listed ones, so the
	// headline figures do not shrink when the table is capped.
	Totals Counts
	// Exposed are the people the risks trace back to, most exposed first.
	Exposed []Exposure
	// Spans are the connections between subjects that only one person has ever
	// made. Both subjects have their own experts; the link between them does
	// not, and counting experts per subject cannot see it.
	Spans []resolve.Span
	// Regions are the connected bodies of work that rest on one person:
	// subjects changed together where the same person leads every one. They are
	// the largest findings in the report, because whoever picks one up has to
	// learn the whole of it rather than a subject at a time.
	Regions []resolve.Region
	// Merged is how many records the identity rules folded into somebody
	// already in the index.
	Merged int
	// Split are the display names left on more than one record because nothing
	// but a shared name connected them. Each reads as cover the organization
	// does not have, so the brief puts them above the findings they weaken.
	Split []resolve.SplitName
	// Aliases are the joins the rules declined to make alone, written in the
	// format the --aliases flag reads, so the caveat arrives with its fix.
	Aliases []resolve.AliasSuggestion
	// Systems are the directories the work rests on, ordered by how few people
	// cover them rather than by size. Empty when no repository was read.
	Systems []resolve.Place
	// Checks are the paste-ready git commands that count the same history the
	// systems above were counted from, one per system.
	Checks []resolve.CheckCommand
	// Ramp is what moving into an unfamiliar area has cost in this history. It
	// renders only when Measured reports enough arrivals to say anything.
	Ramp resolve.Ramp
}

// Exposure is one person and the knowledge that would leave with them.
type Exposure struct {
	// Name is the person's display name.
	Name string
	// ID is the person's canonical identifier.
	ID string
	// Sole are the topics no one else covers at all.
	Sole []string
	// Leading are the topics they lead while others still contribute.
	Leading []string
	// Succession is who stands nearest to what rests on them, nil when no
	// succession was computed for this person.
	Succession *Succession
}

// Succession is who stands nearest to what rests on one person.
//
// It is flattened out of the resolve finding so the template renders text
// rather than walking a candidate that may not exist. Nobody standing out is
// carried as its own field for the same reason: it is a finding, and a
// template reading a nil candidate would print a blank instead.
type Succession struct {
	// Person is the holder's canonical identifier, which is what ties the
	// finding to the exposure it answers.
	Person string
	// Dir is the place the succession was judged against, empty when the holder
	// leads no reported system and only subjects were compared.
	Dir string
	// Nearest is the candidate's display name, empty when NobodyClose.
	Nearest string
	// Reasons is the evidence for the candidate, strongest first.
	Reasons []string
	// NobodyClose is set when the record offers no natural second, which is a
	// sharper finding than a candidate rather than a gap in the report.
	NobodyClose bool
}

// AttachSuccession puts each succession beside the exposure it answers, so a
// reader meets who stands nearest in the same entry that says what would leave
// with the person. A succession for somebody who holds nothing alone and leads
// nothing has no exposure to attach to and is dropped.
func AttachSuccession(exposed []Exposure, found []Succession) []Exposure {
	byID := make(map[string]Succession, len(found))
	for _, s := range found {
		if s.Person == "" {
			continue
		}
		if _, taken := byID[s.Person]; !taken {
			byID[s.Person] = s
		}
	}
	out := make([]Exposure, len(exposed))
	copy(out, exposed)
	for i := range out {
		s, ok := byID[out[i].ID]
		if !ok {
			continue
		}
		attached := s
		out[i].Succession = &attached
	}
	return out
}

// ListedSystems cuts the places down to the n a brief lists and brings each
// one's check command with it. checks must be the commands built from places,
// one per place and in the same order, which is how PlaceChecks builds them.
//
// Fewest holders first rather than largest first, because ranking places by
// size sorts the least concentrated work to the top and buries every finding
// under it. The checks take the same cut and the same order: a brief listing
// fifteen systems and forty commands, ordered differently, asks the reader to
// match them up and invites the conclusion that the two disagree.
func ListedSystems(
	places []resolve.Place, checks []resolve.CheckCommand, n int,
) ([]resolve.Place, []resolve.CheckCommand) {
	// Directories are the place tally's keys, so each appears once and its
	// index is the check that was built for it. Pairing on the index rather
	// than on the directory text is what keeps a multi-repository read right,
	// where the check names the directory inside the clone and the place names
	// it with the repository prefix.
	at := make(map[string]int, len(places))
	for i, pl := range places {
		at[pl.Dir] = i
	}
	listed := resolve.MostFragile(places, n)
	paired := make([]resolve.CheckCommand, 0, len(listed))
	for _, pl := range listed {
		if i, ok := at[pl.Dir]; ok && i < len(checks) {
			paired = append(paired, checks[i])
		}
	}
	return listed, paired
}

// CheckNotes is the distinct reading instructions across the checks, in the
// order they first appear.
//
// Every check against one clone carries the same sentence, and repeating it
// under each of forty commands buries it. It cannot be dropped: raw git output
// lists one person's several addresses as separate lines, and a reader who does
// not know that reads the counts as contradicting the finding.
func CheckNotes(checks []resolve.CheckCommand) []string {
	seen := make(map[string]bool, len(checks))
	var out []string
	for _, c := range checks {
		if c.Reads == "" || seen[c.Reads] {
			continue
		}
		seen[c.Reads] = true
		out = append(out, c.Reads)
	}
	return out
}

// Exposures turns per-topic risk into per-person risk, which is the form the
// question actually gets asked in: not "which topics are concentrated" but
// "who can we not afford to lose". A topic with a single expert is theirs
// alone; a topic they merely lead still has cover behind them.
func Exposures(risks []resolve.TopicRisk) []Exposure {
	byID := make(map[string]*Exposure)
	at := func(e resolve.RiskExpert) *Exposure {
		if ex := byID[e.ID]; ex != nil {
			return ex
		}
		ex := &Exposure{Name: e.Name, ID: e.ID}
		byID[e.ID] = ex
		return ex
	}
	for _, r := range risks {
		if len(r.Experts) == 0 {
			continue
		}
		// The lead is who the subject rests on; the share ranking is who has
		// touched it. Attributing exposure by share handed half a real
		// project's subjects to its five widest-ranging committers.
		top := r.Experts[0]
		if r.Lead != "" {
			top = resolve.RiskExpert{ID: r.LeadID, Name: r.Lead}
			for _, e := range r.Experts {
				if e.ID == r.LeadID {
					top = e
					break
				}
			}
		}
		if len(r.Experts) == 1 {
			ex := at(top)
			ex.Sole = append(ex.Sole, r.Topic)
			continue
		}
		if r.Level != "ok" {
			ex := at(top)
			ex.Leading = append(ex.Leading, r.Topic)
		}
	}
	out := make([]Exposure, 0, len(byID))
	for _, ex := range byID {
		sort.Strings(ex.Sole)
		sort.Strings(ex.Leading)
		out = append(out, *ex)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Sole) != len(out[j].Sole) {
			return len(out[i].Sole) > len(out[j].Sole)
		}
		if len(out[i].Leading) != len(out[j].Leading) {
			return len(out[i].Leading) > len(out[j].Leading)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Counts summarizes a brief for its headline figures.
type Counts struct {
	// Critical is how many topics scored critical.
	Critical int
	// Elevated is how many topics scored elevated.
	Elevated int
	// SinglePoint is how many topics rest on exactly one person.
	SinglePoint int
	// Exposed is how many people solely hold at least one topic.
	Exposed int
	// MovedOn is how many subjects rest on somebody who has stopped working on
	// them. It is the sharpest count in the report: the expert it would send
	// you to has already gone.
	MovedOn int
}

// Count tallies the headline figures over every scored subject. It is taken
// before any cap on the table, so capping what is listed never quietly shrinks
// the numbers the report leads with.
func Count(risks []resolve.TopicRisk, exposed []Exposure) Counts {
	var c Counts
	for _, r := range risks {
		switch r.Level {
		case "critical":
			c.Critical++
		case "elevated":
			c.Elevated++
		}
		if len(r.Experts) == 1 {
			c.SinglePoint++
		}
		if r.LeadQuiet() {
			c.MovedOn++
		}
	}
	for _, ex := range exposed {
		if len(ex.Sole) > 0 {
			c.Exposed++
		}
	}
	return c
}

// funcs are the small formatting helpers the template needs. Everything else
// is done in Go, so the template stays a document rather than a program.
var funcs = template.FuncMap{
	"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"join": func(parts []string) string {
		out := ""
		for i, p := range parts {
			if i > 0 {
				out += ", "
			}
			out += p
		}
		return out
	},
	"date": func(t time.Time) string { return t.Format("2 January 2006, 15:04 MST") },
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"sub":   func(a, b int) int { return a - b },
	"notes": CheckNotes,
	// evidence joins reason fragments with semicolons, because a fragment such
	// as "holds 4 of the 6 subjects they hold" carries its own commas.
	"evidence": func(parts []string) string {
		out := ""
		for i, p := range parts {
			if i > 0 {
				out += "; "
			}
			out += p
		}
		return out
	},
}

// WriteRisk renders the brief as one self-contained HTML file. Nothing is
// fetched at open time, so the report reads the same offline, from an email
// attachment, or years later.
func WriteRisk(w io.Writer, b Brief) error {
	tmpl, err := template.New("risk.html").Funcs(funcs).ParseFS(files, "risk.html")
	if err != nil {
		return fmt.Errorf("report: parse template: %w", err)
	}
	if err := tmpl.Execute(w, b); err != nil {
		return fmt.Errorf("report: render: %w", err)
	}
	return nil
}
