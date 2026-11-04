package cmd

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// newRelatedCmd builds the related command, which reports the topics held by
// the same people as a given topic.
func newRelatedCmd(opts *options) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "related <topic>",
		Short: "Show the topics that belong to the same body of knowledge",
		Long: `Report the topics whose experts overlap with a topic's own. Organizations name
one subject several ways, and a topic usually has specialties underneath it.
Overlap over the people who do the work finds both without a taxonomy anyone had
to write: a topic held by the same people is the same body of knowledge, and one
held by fewer of them is a specialty within it.

Deterministic over the graph, no model needed.

Examples:
  peepwho related billing
  peepwho related kubernetes --limit 5`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			rel := resolve.Related(ix, args[0], limit)
			return opts.render(cmd.OutOrStdout(),
				map[string]any{"topic": args[0], "related": rel},
				func(w io.Writer, s style) {
					renderRelated(w, args[0], rel, s)
				})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum related topics to show; 0 for all")
	return cmd
}
