package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/policy"
)

// newRefreshCmd builds the refresh command, which re-indexes every source with
// the flags it was last indexed with, merging each into the existing index.
func newRefreshCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Re-index every source with its saved flags, merging into the index",
		Long: `Re-run every source that has been indexed at least once, reusing the flags it was
indexed with and merging the result. Sources indexed in separate runs, such as
two repositories, are each replayed. It is what a scheduled refresh runs, so the
graph stays current without retyping each source's scope.

  whotoask refresh`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			x := opts.indexer(cmd)
			x.PolicyFor = opts.recordedPolicy(cmd)
			_, err := x.Refresh(cmd.Context(), nil, opts.now())
			return err
		},
	}
	return cmd
}

// recordedPolicy returns the policy a refreshed scope runs under. A scope runs
// as its own index run did: under the --policy it was recorded with, or the
// flag's default and the org policy files when none was, whatever --policy
// the refresh itself was given.
func (o *options) recordedPolicy(cmd *cobra.Command) func(string) (policy.Policy, error) {
	def := cmd.Root().PersistentFlags().Lookup("policy").DefValue
	return func(recorded string) (policy.Policy, error) {
		run := *o
		run.policyName = def
		if recorded != "" {
			run.policyName = recorded
		}
		if err := run.resolvePolicy(recorded != "", cmd.ErrOrStderr()); err != nil {
			return policy.Policy{}, err
		}
		return run.pol, nil
	}
}
