package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/report"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// newRiskCmd builds the risk command, a deterministic view of where knowledge is
// concentrated in too few people, and what would leave with one of them.
func newRiskCmd(opts *options) *cobra.Command {
	var (
		limit    int
		htmlPath string
	)
	cmd := &cobra.Command{
		Use:   "risk [person]",
		Short: "Show where knowledge is dangerously concentrated",
		Long: `Score knowledge concentration across the graph: the topics where one or two
people hold most of the expertise, so a single departure is visible before it
hurts. Name a person for the offboarding view: what the record shows only them in.

Deterministic arithmetic over the graph, no model needed.

Examples:
  peepwho risk
  peepwho risk "Angela Malone"`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			if htmlPath != "" {
				if len(args) > 0 {
					return fmt.Errorf("%w: --html writes the whole-company brief, so it takes no person",
						ErrBadArgs)
				}
				// A brief is read, not searched. Every subject is still
				// scored and still counted in the figures it leads with, but a
				// large organization has thousands of them and listing all of
				// them makes a document nobody opens twice. --limit 0 lists
				// every one for somebody who wants the whole table.
				capped := briefRows
				if cmd.Flags().Changed("limit") {
					capped = limit
				}
				return writeRiskHTML(cmd, ix, htmlPath, capped, opts.now())
			}
			if len(args) > 0 {
				who := strings.Join(args, " ")
				imp := resolve.Departure(ix, who)
				return opts.render(cmd.OutOrStdout(), imp, func(w io.Writer, s style) {
					renderDeparture(w, who, imp, s)
					renderNotFor(w, s)
				})
			}
			report := resolve.Risk(ix, limit)
			regions := resolve.Regions(ix, regionsShown)
			spans := resolve.SoleSpans(ix, spansShown)
			view := map[string]any{"topics": report, "regions": regions, "spans": spans}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				renderRisk(w, report, s)
				renderRegions(w, regions, s)
				renderSpans(w, spans, s)
				renderNotFor(w, s)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum topics to report.")
	cmd.Flags().StringVar(&htmlPath, "html", "",
		"Write a self-contained HTML brief to this path, for sending to somebody without peepwho.")
	return cmd
}

// regionsShown is how many joined bodies of work the report names. They are
// the largest findings it has, so a handful is enough to act on.
const regionsShown = 8

// spansShown is how many one-person connections the report names.
const spansShown = 8

// briefRows is how many subjects a brief lists before it stops, unless asked
// for a different number. The figures above the table always count every scored
// subject, so this shortens the reading without shrinking the finding.
const briefRows = 100

// briefFindings is what an assessment has already worked out and a brief would
// otherwise have to do without: the places, their check commands, what moving
// areas has cost, the identity judgements, and who stands nearest to each key
// person. They are gathered into one struct because they arrive together and
// half of them are empty whenever no repository was read.
type briefFindings struct {
	// Systems are the directories the work rests on, in the order PlaceLeads
	// produced them. The brief reorders them by exposure.
	Systems []resolve.Place
	// Checks are the git commands that count the same history, one per system
	// and in the same order, which is how each is matched back to the place it
	// checks. PlaceChecks builds them that way from this same slice.
	Checks []resolve.CheckCommand
	// Ramp is what arriving in an unfamiliar area has cost in this history.
	Ramp resolve.Ramp
	// Split are the display names left on more than one record.
	Split []resolve.SplitName
	// Aliases are the joins the rules declined to make without a human.
	Aliases []resolve.AliasSuggestion
	// Successors is who stands nearest to what rests on each key person.
	Successors []personSuccession
	// Merged is how many records the identity rules folded together.
	Merged int
}

// successions converts the assessment's succession findings into the brief's
// form, which carries text rather than a candidate that may be absent.
func successions(found []personSuccession) []report.Succession {
	out := make([]report.Succession, 0, len(found))
	for _, s := range found {
		entry := report.Succession{Person: s.Person, Dir: s.Dir, NobodyClose: s.NobodyClose}
		if s.Nearest != nil {
			entry.Nearest = s.Nearest.Name
			for _, r := range s.Nearest.Reasons {
				entry.Reasons = append(entry.Reasons, r.Detail)
			}
		}
		// Nothing to say and nobody named is not a finding, so it is dropped
		// rather than rendered as an empty answer beside an exposure.
		if entry.Nearest == "" && !entry.NobodyClose {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// riskHTML renders the knowledge-risk brief into memory. Rendering before
// writing means a template failure leaves no half-written report behind, and a
// caller that seals the file seals the exact bytes it wrote.
//
// The scored subjects, regions, and crossings are passed in because each is a
// walk of the whole graph and an assessment already has all three.
func riskHTML(
	ix *index.Index, all []resolve.TopicRisk, regions []resolve.Region,
	spans []resolve.Span, found briefFindings, limit int, now time.Time,
) ([]byte, error) {
	// Score everything, then cap only what is listed. A brief that quietly
	// counted just the rows it printed would understate the finding it is for.
	exposed := report.AttachSuccession(report.Exposures(all), successions(found.Successors))
	listed := all
	if limit > 0 && len(listed) > limit {
		listed = listed[:limit]
	}
	systems, checks := report.ListedSystems(found.Systems, found.Checks, briefSystems)
	brief := report.Brief{
		Generated: now,
		People:    len(ix.Graph.People),
		Scored:    len(all),
		Sources:   ix.SourceNames(),
		Risks:     listed,
		Regions:   resolve.Cap(regions, regionsShown),
		Spans:     resolve.Cap(spans, spansShown),
		Totals:    report.Count(all, exposed),
		Exposed:   exposed,
		Merged:    found.Merged,
		Split:     found.Split,
		Aliases:   found.Aliases,
		Systems:   systems,
		Checks:    checks,
		Ramp:      found.Ramp,
	}
	var buf bytes.Buffer
	if err := report.WriteRisk(&buf, brief); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// briefSystems is how many places a brief lists. The deliverable's systems.json
// carries every one of them, so the cut shortens the reading rather than the
// finding.
const briefSystems = 15

// writeRiskHTML renders the knowledge-risk brief, generated at now, to a file. It has no
// repository in front of it, so it carries the identity judgements, which come
// from the index alone, and none of the place findings, which do not.
func writeRiskHTML(cmd *cobra.Command, ix *index.Index, path string, limit int, now time.Time) error {
	split := resolve.SplitNames(ix)
	found := briefFindings{
		Split:   split,
		Aliases: resolve.SuggestAliases(ix, split),
		Merged:  len(ix.Joins()),
	}
	data, err := riskHTML(ix, resolve.Risk(ix, 0),
		resolve.Regions(ix, regionsShown), resolve.SoleSpans(ix, spansShown), found, limit, now)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("risk: write %s: %w", path, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "peepwho: wrote the knowledge-risk brief to %s\n", path)
	return nil
}
