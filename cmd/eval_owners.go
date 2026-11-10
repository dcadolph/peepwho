package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/ownersbench"
)

// evalOwnersArgs holds the flags of the eval owners command.
type evalOwnersArgs struct {
	// Repo is the repository root holding the OWNERS ground truth.
	Repo string
	// SinceDays is the history window for both the index and the baseline.
	SinceDays int
	// MinCommits is the least directory activity worth judging.
	MinCommits int
	// TopK is how many names each side may offer.
	TopK int
	// MaxLeafShare is the most directories a leaf name may be shared by.
	MaxLeafShare int
	// UseIndex scores the saved index instead of one built from git here.
	UseIndex bool
	// ProbePath is the saved forge read from the review probe.
	ProbePath string
	// Credit says who earns place credit for a pull request.
	Credit string
	// AnswerTopics credits answerers with each episode's subjects.
	AnswerTopics bool
	// JSON emits the full result as JSON.
	JSON bool
}

// newEvalOwnersCmd builds the eval owners subcommand, which scores whotoask's
// ownership claim against a repository's own OWNERS files.
func newEvalOwnersCmd(opts *options) *cobra.Command {
	var a evalOwnersArgs
	cmd := &cobra.Command{
		Use:    "owners",
		Short:  "Score ownership recovery against a repo's own OWNERS files",
		Hidden: true,
		Long: `Score whotoask against the ownership humans wrote down: the OWNERS files
projects in the Kubernetes ecosystem maintain for real governance, aliases
expanded to individuals.

This measures ownership recovery, not expertise. The naive baseline is git's
top committers per directory, printed beside every whotoask number, and the
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
  whotoask eval owners --repo ~/src/kubernetes
  whotoask eval owners --repo ~/src/kubernetes --use-index --data-dir ./k8sidx
  whotoask eval owners --repo ~/src/kubernetes --use-index --data-dir ./k8sidx \
    --probe ./k8sidx/probe.json --credit answered`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEvalOwners(cmd, opts, &a)
		},
	}
	a.register(cmd)
	return cmd
}

// register binds the eval owners flags to the arguments.
func (a *evalOwnersArgs) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&a.Repo, "repo", "", "Repository root holding OWNERS ground truth.")
	f.IntVar(&a.SinceDays, "since-days", 730, "History window, for both index and baseline.")
	f.IntVar(&a.MinCommits, "min-commits", 30, "Least directory activity worth judging.")
	f.IntVar(&a.TopK, "top-k", 3, "Names each side may offer.")
	f.IntVar(&a.MaxLeafShare, "max-leaf-share", 3,
		"Most directories a leaf name may be shared by before it is unjudgeable.")
	f.StringVar(&a.ProbePath, "probe", "",
		"Saved forge read from the review probe: pull dirs, participants, and episodes.")
	f.StringVar(&a.Credit, "credit", string(ownersbench.CreditAll),
		"Who earns place credit for a pull request: none, all, or answered. Needs --probe.")
	f.BoolVar(&a.AnswerTopics, "answer-topics", false,
		"Credit answerers with each episode's subjects in the index. Needs --probe.")
	f.BoolVar(&a.UseIndex, "use-index", false,
		"Score the index in --data-dir instead of building one from git here.")
	f.BoolVar(&a.JSON, "json", false, "Emit the full result as JSON.")
}

// runEvalOwners scores the ownership claim against the OWNERS ground truth.
func runEvalOwners(cmd *cobra.Command, opts *options, a *evalOwnersArgs) error {
	if a.Repo == "" {
		return fmt.Errorf("%w: --repo is required", ErrBadArgs)
	}
	// A scoring flag with no probe to read would score nothing and say
	// nothing, and a silent no-op in a benchmark reads as a result.
	if a.ProbePath == "" && (cmd.Flags().Changed("credit") || a.AnswerTopics) {
		return fmt.Errorf("%w: --credit and --answer-topics need --probe", ErrBadArgs)
	}
	log := cmd.ErrOrStderr()
	// The git walk always runs: its place-scoped tally is the primary
	// ranking, whatever index supplies the identities.
	git := connector.NewGitHistory(connector.GitOptions{
		Paths: []string{a.Repo}, SinceDays: a.SinceDays, MaxCommits: 1000000, Log: log,
	})
	recs, err := git.Fetch(cmd.Context())
	if err != nil {
		return err
	}
	ix, err := evalOwnersIndex(cmd, opts, a.UseIndex, recs)
	if err != nil {
		return err
	}
	dirWork, workTotals := git.DirWork(), git.WorkTotals()
	if a.ProbePath != "" {
		dirWork, workTotals, err = applyOwnersProbe(log, ix, a, dirWork, workTotals)
		if err != nil {
			return err
		}
	}
	res, err := ownersbench.Run(ix, ownersbench.Config{
		Repo: a.Repo, SinceDays: a.SinceDays, MinCommits: a.MinCommits,
		TopK: a.TopK, MaxLeafShare: a.MaxLeafShare, Log: log,
		DirWork: dirWork, WorkTotals: workTotals,
	})
	if err != nil {
		return err
	}
	if a.JSON {
		return writeJSON(cmd.OutOrStdout(), res, opts.pretty)
	}
	printOwnersScore(cmd.OutOrStdout(), res, a.TopK)
	return nil
}

// evalOwnersIndex loads the saved index, or builds one from the git records
// alone so the ground truth never leaks into the input.
func evalOwnersIndex(cmd *cobra.Command, opts *options, useIndex bool, recs []connector.Record) (*index.Index, error) {
	if useIndex {
		loaded, err := opts.loadIndex(cmd)
		if err != nil {
			return nil, noIndexError(err)
		}
		return loaded, nil
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()
	return ix, nil
}

// applyOwnersProbe replaces the git tally with the saved probe's credit and,
// when asked, credits answerers with each episode's subjects.
//
// A saved probe carries the forge read a git walk cannot do: who took part in
// each pull request, and which of them answered rather than asked. Every
// credit mode scores the same saved bytes, so the arms differ in scoring alone.
func applyOwnersProbe(
	log io.Writer, ix *index.Index, a *evalOwnersArgs,
	dirWork map[string]map[string]float64, workTotals map[string]float64,
) (map[string]map[string]float64, map[string]float64, error) {
	probe, err := ownersbench.LoadProbe(a.ProbePath)
	if err != nil {
		return nil, nil, err
	}
	loaded, totals, err := probe.Tally(ownersbench.CreditMode(a.Credit), dirWork, workTotals)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(log, "probe: %d pull requests, %d episodes, credit=%s\n",
		len(probe.PullPeople), len(probe.Episodes), a.Credit)
	if a.AnswerTopics {
		got := ix.ScoreAnswers(probe.Episodes)
		fmt.Fprintf(log, "answer-hood: %d episodes credited, %d increments, %d unknown answerers\n",
			got.Episodes, got.Credits, len(got.Unknown))
		for _, id := range got.Unknown {
			fmt.Fprintf(log, "answer-hood: no person for answerer %s\n", id)
		}
	}
	return loaded, totals, nil
}

// printOwnersScore writes the human score: whotoask beside the git baseline,
// the discriminating cohort, and one line per judged directory.
func printOwnersScore(out io.Writer, res *ownersbench.Result, topK int) {
	w, n := res.Score(func(d ownersbench.DirResult) bool { return d.WhoToAskHit })
	g, _ := res.Score(func(d ownersbench.DirResult) bool { return d.GitHit })
	cohC := res.CohortC()
	cw := 0
	for _, d := range cohC {
		if d.WhoToAskHit {
			cw++
		}
	}
	fmt.Fprintf(out, "judged %d directories (of %d with truth; dropped %d quiet, %d ambiguous, %d unmappable)\n",
		n, res.TruthDirs, res.DroppedQuiet, res.DroppedAmbiguous, res.DroppedUnmappable)
	fmt.Fprintf(out, "whotoask names an approver in its top %d:  %d/%d (%.0f%%)\n",
		topK, w, n, 100*float64(w)/float64(n))
	fmt.Fprintf(out, "git top-%d committers baseline:         %d/%d (%.0f%%)\n",
		topK, g, n, 100*float64(g)/float64(n))
	if len(cohC) > 0 {
		fmt.Fprintf(out, "cohort C, approver not a top committer: %d/%d (%.0f%%)  <- the discriminating cohort\n",
			cw, len(cohC), 100*float64(cw)/float64(len(cohC)))
	}
	for _, d := range res.Dirs {
		mark := "."
		if d.WhoToAskHit {
			mark = "Y"
		}
		base := "."
		if d.GitHit {
			base = "Y"
		}
		fmt.Fprintf(out, "  %s %s  %-46s whotoask=%v\n", mark, base, d.Dir, d.WhoToAskTop)
	}
}
