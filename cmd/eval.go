package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/eval"
	"github.com/dcadolph/peepwho/internal/render"
)

// newEvalCmd builds the eval command, which measures the current index and
// compares it against a saved measurement.
//
// It is hidden because it answers a question about peepwho rather than about the
// organization peepwho was pointed at, so it belongs to whoever is changing the
// ranking rather than to whoever is using the tool.
func newEvalCmd(opts *options) *cobra.Command {
	var baseline, save string
	cmd := &cobra.Command{
		Use:    "eval",
		Short:  "Measure this index, and compare it against a saved measurement",
		Hidden: true,
		Long: `Measure how well the current index answers, and what could be limiting it.

The score is agreement: how often the owner an organization declared for an area
is also the person peepwho says leads it. Read it alongside the numbers under it,
because a disagreement has three different causes and only one of them is a bug.
An owner with no recorded work anywhere is usually an identity that was never
joined. An owner who works elsewhere but never in their own area is paper
ownership, and peepwho is probably right. An owner merely out-worked in their own
area is the arguable case.

Save a measurement before a change and compare after it. Two runs are only
compared when they cover the same sources, since adding one moves every number
here for reasons that have nothing to do with quality.

Examples:
  peepwho eval
  peepwho eval --save before.json
  peepwho eval --baseline before.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			now := eval.Measure(ix)

			var changes []eval.Change
			var comparable, sameQuestions bool
			var before eval.Result
			var head eval.Head
			if baseline != "" {
				if before, err = readResult(baseline); err != nil {
					return err
				}
				changes, comparable = eval.Compare(before, now)
				head, sameQuestions = eval.CompareAreas(before, now)
			}
			if save != "" {
				if err := writeResult(save, now); err != nil {
					return err
				}
			}

			view := map[string]any{"result": now}
			if baseline != "" {
				view["baseline"] = before
				view["comparable"] = comparable
				view["changes"] = changes
				view["head"] = head
				view["sameQuestions"] = sameQuestions
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				render.RenderEval(w, now, s)
				if baseline != "" {
					render.RenderEvalHead(w, head, sameQuestions, s)
					render.RenderEvalChanges(w, changes, comparable, s)
				}
				if save != "" {
					fmt.Fprintf(w, "\n%s\n", s.Dim("Saved to "+save))
				}
			})
		},
	}
	cmd.AddCommand(newEvalOwnersCmd(opts))
	cmd.AddCommand(newEvalHoldoutCmd(opts))
	cmd.Flags().StringVar(&baseline, "baseline", "", "Compare against a measurement saved earlier")
	cmd.Flags().StringVar(&save, "save", "", "Write this measurement to a file")
	return cmd
}

// readResult loads a measurement saved by an earlier run.
func readResult(path string) (eval.Result, error) {
	var r eval.Result
	b, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("%w: read baseline: %w", ErrEval, err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%w: parse baseline %s: %w", ErrEval, path, err)
	}
	return r, nil
}

// writeResult saves a measurement for a later run to compare against.
func writeResult(path string, r eval.Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode measurement: %w", ErrEval, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: write %s: %w", ErrEval, path, err)
	}
	return nil
}
