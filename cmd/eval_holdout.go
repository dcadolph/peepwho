package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/ownersbench"
)

// newEvalHoldoutCmd builds the eval holdout subcommand, which measures
// prediction rather than description.
func newEvalHoldoutCmd(_ *options) *cobra.Command {
	var (
		repo       string
		sinceDays  int
		cutoffDays int
		minPast    int
		minFuture  int
		topK       int
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:    "holdout",
		Short:  "Predict from the past and score against what actually happened",
		Hidden: true,
		Long: `Build an index from history up to a cutoff, ask who each place rests on
knowing only that, then read the work that came afterwards and see who was
right.

Every other measurement here describes a history peepwho has already read.
This one hides the answer: nothing after the cutoff reaches the index, and a
departure risk is a claim about the future, so this is the shape of
measurement that claim deserves.

The naive prediction, the past window's top committers, is scored on the same
directories from the same evidence. Where the two agree the run says nothing
about either, so the disagreements are reported separately.

Examples:
  peepwho eval holdout --repo ~/src/kubernetes
  peepwho eval holdout --repo ~/src/prometheus --cutoff-days 540`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return fmt.Errorf("%w: --repo is required", ErrBadArgs)
			}
			if cutoffDays >= sinceDays {
				return fmt.Errorf("%w: --cutoff-days must be inside --since-days", ErrBadArgs)
			}
			log := cmd.ErrOrStderr()
			// UntilDays is what keeps the future out of the index: the walk
			// stops at the cutoff, so nothing peepwho answers with could have
			// been learned from the window it is being judged on.
			git := connector.NewGitHistory(connector.GitOptions{
				Paths: []string{repo}, SinceDays: sinceDays, UntilDays: cutoffDays,
				MaxCommits: 1000000, Log: log,
			})
			recs, err := git.Fetch(cmd.Context())
			if err != nil {
				return err
			}
			ix := index.New()
			ix.Build(recs)
			ix.AutoJoin()
			ix.Canonicalize()

			res, err := ownersbench.RunHoldout(ix, ownersbench.HoldoutConfig{
				Repo: repo, SinceDays: sinceDays, CutoffDays: cutoffDays,
				MinPast: minPast, MinFuture: minFuture, TopK: topK, Log: log,
				DirWork: git.DirWork(), WorkTotals: git.WorkTotals(),
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), res, true)
			}
			res.Report(cmd.OutOrStdout(), topK)

			// The holdout above asks who will be busy. The claim the product
			// makes is narrower: that a place resting on one person is
			// fragile. Score that too, from the same windows.
			sur, err := ownersbench.RunSurvival(ix, ownersbench.SurvivalConfig{
				Repo: repo, SinceDays: sinceDays, CutoffDays: cutoffDays,
				MinPast: minPast, Log: log,
				DirWork: git.DirWork(), WorkTotals: git.WorkTotals(),
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(),
				"does concentration predict fragility, which is the actual claim:")
			sur.Report(cmd.OutOrStdout())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "Repository root to predict over.")
	f.IntVar(&sinceDays, "since-days", 1095, "How far back the whole window reaches.")
	f.IntVar(&cutoffDays, "cutoff-days", 365,
		"Where the past stops and the future begins, counted back from today.")
	f.IntVar(&minPast, "min-past", 20, "Least activity before the cutoff to predict about.")
	f.IntVar(&minFuture, "min-future", 10, "Least activity after it to check against.")
	f.IntVar(&topK, "top-k", 3, "Names each predictor may offer.")
	f.BoolVar(&jsonOut, "json", false, "Emit the full result as JSON.")
	return cmd
}
