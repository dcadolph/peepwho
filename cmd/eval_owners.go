package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/ownersbench"
)

// newEvalOwnersCmd builds the eval owners subcommand, which scores peepwho's
// ownership claim against a repository's own OWNERS files.
func newEvalOwnersCmd(opts *options) *cobra.Command {
	var (
		repo         string
		sinceDays    int
		minCommits   int
		topK         int
		maxLeafShare int
		useIndex     bool
		probePath    string
		credit       string
		answerTopics bool
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:    "owners",
		Short:  "Score ownership recovery against a repo's own OWNERS files",
		Hidden: true,
		Long: `Score peepwho against the ownership humans wrote down: the OWNERS files
projects in the Kubernetes ecosystem maintain for real governance, aliases
expanded to individuals.

This measures ownership recovery, not expertise. The naive baseline is git's
top committers per directory, printed beside every peepwho number, and the
directories where the baseline misses are reported as their own cohort: they
are the only ones that can tell this tool apart from counting commits.

By default the index is built here, from git history alone, so the ground
truth never leaks into the input. Pass --use-index to score an existing index
built with more sources (such as GitHub review data), and make sure that index
was never fed CODEOWNERS for this repository: judging ownership recovery on an
index that ingested declared ownership is a circle.

A saved probe adds the forge read a git walk cannot do. --credit says who earns
place credit for a pull request: everyone who took part, which is what the
product ships, or only the people who answered, which drops the author who
opened it. --answer-topics additionally credits answerers with each episode's
subjects in the index. Both read the same saved bytes, so two runs that differ
in these flags differ in scoring and in nothing else.

Examples:
  peepwho eval owners --repo ~/src/kubernetes
  peepwho eval owners --repo ~/src/kubernetes --use-index --data-dir ./k8sidx
  peepwho eval owners --repo ~/src/kubernetes --use-index --data-dir ./k8sidx \
    --probe ./k8sidx/probe.json --credit answered`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return fmt.Errorf("%w: --repo is required", ErrBadArgs)
			}
			// A scoring flag with no probe to read would score nothing and say
			// nothing, and a silent no-op in a benchmark reads as a result.
			if probePath == "" && (cmd.Flags().Changed("credit") || answerTopics) {
				return fmt.Errorf("%w: --credit and --answer-topics need --probe", ErrBadArgs)
			}
			log := cmd.ErrOrStderr()
			// The git walk always runs: its place-scoped tally is the primary
			// ranking, whatever index supplies the identities.
			git := connector.NewGitHistory(connector.GitOptions{
				Paths: []string{repo}, SinceDays: sinceDays, MaxCommits: 1000000, Log: log,
			})
			recs, err := git.Fetch(cmd.Context())
			if err != nil {
				return err
			}
			var ix *index.Index
			if useIndex {
				loaded, err := opts.loadIndex(cmd)
				if err != nil {
					return noIndexError(err)
				}
				ix = loaded
			} else {
				ix = index.New()
				ix.Build(recs)
				ix.AutoJoin()
				ix.Canonicalize()
			}

			dirWork, workTotals := git.DirWork(), git.WorkTotals()
			if probePath != "" {
				// A saved probe carries the forge read a git walk cannot do:
				// who took part in each pull request, and which of them
				// answered rather than asked. Every credit mode scores the
				// same saved bytes, so the arms differ in scoring alone.
				probe, err := ownersbench.LoadProbe(probePath)
				if err != nil {
					return err
				}
				loaded, totals, err := probe.Tally(ownersbench.CreditMode(credit), dirWork, workTotals)
				if err != nil {
					return err
				}
				dirWork, workTotals = loaded, totals
				fmt.Fprintf(log, "probe: %d pull requests, %d episodes, credit=%s\n",
					len(probe.PullPeople), len(probe.Episodes), credit)
				if answerTopics {
					got := ix.ScoreAnswers(probe.Episodes)
					fmt.Fprintf(log, "answer-hood: %d episodes credited, %d increments, %d unknown answerers\n",
						got.Episodes, got.Credits, len(got.Unknown))
					for _, id := range got.Unknown {
						fmt.Fprintf(log, "answer-hood: no person for answerer %s\n", id)
					}
				}
			}

			res, err := ownersbench.Run(ix, ownersbench.Config{
				Repo: repo, SinceDays: sinceDays, MinCommits: minCommits,
				TopK: topK, MaxLeafShare: maxLeafShare, Log: log,
				DirWork: dirWork, WorkTotals: workTotals,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), res, opts.pretty)
			}

			w, n := res.Score(func(d ownersbench.DirResult) bool { return d.PeepwhoHit })
			g, _ := res.Score(func(d ownersbench.DirResult) bool { return d.GitHit })
			cohC := res.CohortC()
			cw := 0
			for _, d := range cohC {
				if d.PeepwhoHit {
					cw++
				}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "judged %d directories (of %d with truth; dropped %d quiet, %d ambiguous, %d unmappable)\n",
				n, res.TruthDirs, res.DroppedQuiet, res.DroppedAmbiguous, res.DroppedUnmappable)
			fmt.Fprintf(out, "peepwho names an approver in its top %d:  %d/%d (%.0f%%)\n",
				topK, w, n, 100*float64(w)/float64(n))
			fmt.Fprintf(out, "git top-%d committers baseline:         %d/%d (%.0f%%)\n",
				topK, g, n, 100*float64(g)/float64(n))
			if len(cohC) > 0 {
				fmt.Fprintf(out, "cohort C, approver not a top committer: %d/%d (%.0f%%)  <- the discriminating cohort\n",
					cw, len(cohC), 100*float64(cw)/float64(len(cohC)))
			}
			for _, d := range res.Dirs {
				mark := "."
				if d.PeepwhoHit {
					mark = "Y"
				}
				base := "."
				if d.GitHit {
					base = "Y"
				}
				fmt.Fprintf(out, "  %s %s  %-46s peepwho=%v\n", mark, base, d.Dir, d.PeepwhoTop)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "Repository root holding OWNERS ground truth.")
	f.IntVar(&sinceDays, "since-days", 730, "History window, for both index and baseline.")
	f.IntVar(&minCommits, "min-commits", 30, "Least directory activity worth judging.")
	f.IntVar(&topK, "top-k", 3, "Names each side may offer.")
	f.IntVar(&maxLeafShare, "max-leaf-share", 3,
		"Most directories a leaf name may be shared by before it is unjudgeable.")
	f.StringVar(&probePath, "probe", "",
		"Saved forge read from the review probe: pull dirs, participants, and episodes.")
	f.StringVar(&credit, "credit", string(ownersbench.CreditAll),
		"Who earns place credit for a pull request: none, all, or answered. Needs --probe.")
	f.BoolVar(&answerTopics, "answer-topics", false,
		"Credit answerers with each episode's subjects in the index. Needs --probe.")
	f.BoolVar(&useIndex, "use-index", false,
		"Score the index in --data-dir instead of building one from git here.")
	f.BoolVar(&jsonOut, "json", false, "Emit the full result as JSON.")
	return cmd
}
