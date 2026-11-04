package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/util"
)

// refreshSkipFlags are index flags that must not be replayed by refresh: they
// control the run itself, not what to fetch. The source is passed explicitly.
//
//nolint:gochecknoglobals // Read-only lookup table.
var refreshSkipFlags = map[string]bool{
	"source": true, "merge": true, "full": true, "allow-shrink": true,
	"data-dir": true, "pretty": true, "changes-file": true,
}

// captureIndexFlags returns the explicitly-set index flags worth replaying on a
// refresh, expanding a repeatable flag into one entry per value.
func captureIndexFlags(cmd *cobra.Command) []string {
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if refreshSkipFlags[f.Name] {
			return
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			for _, v := range sv.GetSlice() {
				args = append(args, "--"+f.Name, v)
			}
			return
		}
		args = append(args, "--"+f.Name+"="+f.Value.String())
	})
	return args
}

// saveInvocation records the flags a source was indexed with under its scope
// key, so refresh can replay every scope ever indexed. It is best-effort: a
// failure to save never fails the index. A source reading stdin cannot be
// replayed, so it is not recorded. The read-modify-write runs under a
// cross-process lock so two concurrent index runs keep each other's entries.
func saveInvocation(opts *options, cmd *cobra.Command, source, scope string) error {
	if readsStdin(cmd, source) {
		return nil
	}
	unlock, err := util.LockFile(opts.refreshPath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	store := engine.NewStore(opts.dir(), nil)
	inv, err := store.LoadInvocations()
	if err != nil {
		return err
	}
	if inv[source] == nil {
		inv[source] = map[string][]string{}
	}
	inv[source][scope] = captureIndexFlags(cmd)
	return store.SaveInvocations(inv)
}

// readsStdin reports whether this index run reads its input from stdin, which a
// scheduled refresh could not supply.
func readsStdin(cmd *cobra.Command, source string) bool {
	if spec, ok := engine.SourceByName(source); !ok || !spec.Stdin {
		return false
	}
	f := cmd.Flags().Lookup("file")
	return f == nil || f.Value.String() == "" || f.Value.String() == "-"
}

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

  peepwho refresh`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inv, err := engine.NewStore(opts.dir(), nil).LoadInvocations()
			if err != nil {
				return err
			}
			if len(inv) == 0 {
				return fmt.Errorf("%w: nothing to refresh; run `peepwho index --source ...` at least once first", ErrBadArgs)
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("refresh: locate binary: %w", err)
			}
			sources := make([]string, 0, len(inv))
			for s := range inv {
				sources = append(sources, s)
			}
			sort.Strings(sources)

			failed, total := 0, 0
			for _, src := range sources {
				scopes := make([]string, 0, len(inv[src]))
				for scope := range inv[src] {
					scopes = append(scopes, scope)
				}
				sort.Strings(scopes)
				for _, scope := range scopes {
					total++
					what := src
					if scope != "" {
						what += " (" + scope + ")"
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "refreshing %s...\n", what)
					args := append([]string{"index", "--source", src, "--merge", "--data-dir", opts.dataDir},
						inv[src][scope]...)
					sub := exec.CommandContext(cmd.Context(), exe, args...)
					sub.Stdout = cmd.OutOrStdout()
					sub.Stderr = cmd.ErrOrStderr()
					if err := sub.Run(); err != nil {
						failed++
						fmt.Fprintf(cmd.ErrOrStderr(), "refresh %s failed: %v\n", what, err)
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("refresh: %d of %d run(s) failed", failed, total)
			}
			return nil
		},
	}
	return cmd
}
