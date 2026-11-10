package cmd

import (
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/render"
)

// newStatusCmd builds the status command: one view of what the index holds and
// how fresh it is, so a user need not re-run an index to answer "when did I last
// index?", "what is in here?", or "is it encrypted?".
func newStatusCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what the index holds and how fresh it is",
		Long: `Report the index: when it was last built, how many people, channels, teams,
and topics it holds, how many records each source contributed, whether it carries
embeddings, and whether a key is set to encrypt it at rest.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			codec, err := opts.codec()
			if err != nil {
				return err
			}
			st := engine.New(ix, opts.pol).Status(opts.now())
			view := map[string]any{
				"index":                     opts.indexPath(),
				"people":                    st.People,
				"channels":                  st.Channels,
				"teams":                     st.Teams,
				"subjects":                  st.Subjects,
				"words":                     st.Words,
				"sources":                   st.Sources,
				"embeddings":                st.Embeddings,
				"encryption_key_configured": codec != nil,
			}
			if !st.BuiltAt.IsZero() {
				view["built_at"] = st.BuiltAt.Format(time.RFC3339)
				view["age"] = st.Age.Round(time.Minute).String()
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				render.RenderStatus(w, view, s)
			})
		},
	}
	return cmd
}
