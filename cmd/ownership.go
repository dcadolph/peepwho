package cmd

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// newOwnershipCmd builds the ownership command, which compares declared
// ownership against who actually has the expertise and reports the drift.
func newOwnershipCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ownership",
		Short: "Show where the owner on paper is not the one doing the work",
		Long: `Compare declared ownership, from a source of record such as CODEOWNERS, against
who actually has the expertise. A mismatch is ownership drift: the code says one
team or person owns an area, but the work and the knowledge sit somewhere else.

Deterministic over the graph, no model needed.

Examples:
  peepwho ownership`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			report := resolve.Ownership(ix)
			// The three buckets ship with the share, because the share alone is
			// not actionable: an owner with no recorded work anywhere is usually
			// an unjoined identity, an owner who never worked in their own area
			// is paper ownership, and only the third is a judgement call about
			// ranking. The human view has said so all along; anything reading
			// the JSON could not tell them apart.
			view := map[string]any{
				"drift":    report.Drift,
				"declared": report.Declared,
				"held":     report.Held,
				"share":    report.Share(),
				"silent":   report.Silent,
				"unworked": report.Unworked,
				"trailing": report.Trailing,
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				renderOwnership(w, report, s)
			})
		},
	}
	return cmd
}
