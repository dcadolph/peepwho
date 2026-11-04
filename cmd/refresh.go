package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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

// invocations records, per source and per scope within it, the flags an index
// run used, so refresh replays every distinct scope rather than only the last
// one indexed.
type invocations map[string]map[string][]string

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

// refreshScope keys one indexing run for the refresh config. Two runs with the
// same key describe the same slice of a source and the newer flags replace the
// older; two runs with different keys, such as two repositories indexed in
// separate git runs, are both kept and both replayed.
func refreshScope(source, watermarkScope, file string, repoPaths []string) string {
	switch source {
	case "git":
		return "repo:" + sortedJoin(repoPaths)
	case "org-csv", "matters", "codeowners", "slack-export", "json":
		return "file:" + file
	default:
		return watermarkScope
	}
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
	inv, err := loadInvocations(opts.refreshPath())
	if err != nil {
		return err
	}
	if inv[source] == nil {
		inv[source] = map[string][]string{}
	}
	inv[source][scope] = captureIndexFlags(cmd)
	return saveInvocations(opts.refreshPath(), inv)
}

// readsStdin reports whether this index run reads its input from stdin, which a
// scheduled refresh could not supply.
func readsStdin(cmd *cobra.Command, source string) bool {
	if source != "json" {
		return false
	}
	f := cmd.Flags().Lookup("file")
	return f == nil || f.Value.String() == "" || f.Value.String() == "-"
}

// loadInvocations reads the refresh config. A missing file yields an empty map.
// A file in the older one-invocation-per-source shape is folded into the keyed
// shape under an empty scope, so nothing already recorded is dropped.
func loadInvocations(path string) (invocations, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return invocations{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refresh: read %s: %w", path, err)
	}
	inv := invocations{}
	if err := json.Unmarshal(raw, &inv); err == nil {
		return inv, nil
	}
	old := map[string][]string{}
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("refresh: parse %s: %w", path, err)
	}
	// The failed decode above can leave partial entries behind, so the keyed
	// map is rebuilt from nothing.
	inv = invocations{}
	for source, args := range old {
		inv[source] = map[string][]string{"": args}
	}
	return inv, nil
}

// saveInvocations writes the refresh config atomically.
func saveInvocations(path string, inv invocations) error {
	raw, err := json.Marshal(inv)
	if err != nil {
		return fmt.Errorf("refresh: encode: %w", err)
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("refresh: write %s: %w", path, err)
	}
	return nil
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
			inv, err := loadInvocations(opts.refreshPath())
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
