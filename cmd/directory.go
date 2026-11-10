package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/render"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// newDirectoryCmd builds the directory command, a browsable inventory of
// everything indexed. The ask command points an empty result here, and the web
// UI and MCP tool expose the same view, so a terminal user has a way to see what
// terms the data actually contains.
func newDirectoryCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "directory [people|channels|teams|topics]",
		Short: "List what is indexed",
		Long: `Print the inventory of everything indexed: people, channels, teams, and
topics. With no argument it prints all four. Use it to see the terms your team's
data actually contains when a question comes back empty.

Examples:
  whotoask directory
  whotoask directory people
  whotoask directory topics`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			dir := resolve.BuildDirectory(ix)
			section := ""
			if len(args) == 1 {
				section = args[0]
			}
			var v any
			switch section {
			case "":
				v = dir
			case "people":
				v = map[string]any{"people": dir.People}
			case "channels":
				v = map[string]any{"channels": dir.Channels}
			case "teams":
				v = map[string]any{"teams": dir.Teams}
			case "topics":
				v = map[string]any{"topics": dir.Topics}
			default:
				return fmt.Errorf(
					"%w: unknown section %q; use people, channels, teams, or topics", ErrBadArgs, section)
			}
			return opts.render(cmd.OutOrStdout(), v, func(w io.Writer, s style) {
				render.RenderDirectory(w, dir, section, s)
			})
		},
	}
	return cmd
}
