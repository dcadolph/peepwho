package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/render"
)

// newNearCmd builds the near command, which ranks the people who work nearest a
// given person by shared groups and shared topics.
func newNearCmd(opts *options) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "near PERSON",
		Short: "Show who works near a person, by shared groups and topics",
		Long: `Rank the people nearest PERSON by shared team and channel membership and shared
topics. Co-membership is normalized by group size, so a tight team counts for far
more than a broad channel, org-wide groups are ignored, and permission tiers of
one group (store-admin, store-write) are folded together. PERSON is an email, an
id, or an exact name.

  peepwho near alice@corp.com
  peepwho near "Kim Ray" --limit 5`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			who := strings.Join(args, " ")
			p := ix.FindPerson(who)
			if p == nil {
				return fmt.Errorf("%w: no person matches %q; try an email, or run `peepwho directory`", ErrBadArgs, who)
			}
			nearList := ix.Near(p.ID, limit)
			view := map[string]any{
				"person": map[string]string{"id": string(p.ID), "name": p.Name, "email": p.Email},
				"near":   nearList,
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				render.RenderNear(w, p.Name, nearList, s)
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of people to return.")
	return cmd
}
