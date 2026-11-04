package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/fact"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// newForgetCmd builds the forget command, which purges one person from
// everything peepwho stores.
func newForgetCmd(opts *options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "forget <email or name>",
		Short: "Purge a person from the index, remembered conversations, and facts",
		Long: `Purge a person from everything peepwho stores on this machine.

Their records are removed from the index under every identity they were known
by, references to them are stripped from channel membership and org relations,
their retained conversation notes are deleted, conversations they were alone in
are forgotten entirely, and recorded facts naming any of their identities are
removed. A conversation with other participants keeps its pointer and loses its
searchable words, since theirs cannot be separated out.

Two caveats:
  - feedback.json holds questions people asked; prune it by deleting the file.
  - Re-indexing a source that still contains the person brings them back. To
    keep them out, remove them at the source or stop indexing it.

Examples:
  peepwho forget jane@corp.com
  peepwho forget "Jane Roe" --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Taking the lock creates the data directory, so a purge with no
			// index to purge from would leave a directory and a lock file
			// behind on a machine that had neither. Nothing can be purged in
			// that case anyway, so say so before touching the disk.
			if _, err := os.Stat(opts.indexPath()); err != nil {
				return noIndexError(err)
			}
			// The same lock every index write takes, so a purge cannot interleave
			// with a scheduled refresh and leave the person half-removed.
			unlock, err := util.LockFile(opts.indexPath() + util.LockSuffix)
			if err != nil {
				return err
			}
			defer unlock()
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			if err := opts.loadSources(ix); err != nil {
				return err
			}
			people := ix.FindPeople(args[0])
			if len(people) == 0 {
				return fmt.Errorf("%w: no person matches %q", ErrBadArgs, args[0])
			}
			// A purge must never pick among namesakes: resolving "Jane Roe" to
			// an arbitrary one of two Jane Roes deletes somebody nobody named.
			if len(people) > 1 {
				lines := make([]string, 0, len(people))
				for _, p := range people {
					line := "  " + string(p.ID)
					if p.Email != "" && !strings.EqualFold(p.Email, string(p.ID)) {
						line += " <" + p.Email + ">"
					}
					lines = append(lines, line)
				}
				return fmt.Errorf(
					"%w: %q names %d people; purge one by email or id:\n%s",
					ErrBadArgs, args[0], len(people), strings.Join(lines, "\n"))
			}
			person := people[0]
			out := cmd.ErrOrStderr()
			label := person.Name
			if person.Email != "" {
				label += " <" + person.Email + ">"
			}
			if !yes {
				fmt.Fprintf(out, "This purges %s and %d linked identities from the index, "+
					"remembered conversations, and recorded facts.\nProceed? [y/N]: ",
					label, len(person.Identities))
				var answer string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil ||
					!strings.EqualFold(strings.TrimSpace(answer), "y") {
					fmt.Fprintln(out, "left everything as it was")
					return nil
				}
			}

			// Collect the identity set before the purge rewrites the graph.
			purgeIDs := append([]model.ID{person.ID}, person.Identities...)

			res := ix.Forget(person)
			if err := opts.saveIndex(ix); err != nil {
				return err
			}

			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			// One pass over the whole identity set. Passing the identities one at
			// a time let a later pass read an episode already edited by an earlier
			// one as a one-person conversation, and drop a bystander with it.
			dropped, edited := store.ForgetPerson(purgeIDs)
			if err := opts.saveEpisodes(store); err != nil {
				return err
			}

			facts, err := forgetFacts(opts, purgeIDs)
			if err != nil {
				return err
			}

			fmt.Fprintf(out,
				"forgot %s: %d records removed, %d mentions stripped, "+
					"%d conversations dropped, %d edited, %d facts removed\n",
				label, res.Records, res.Mentions, dropped, edited, facts)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Purge without asking.")
	return cmd
}

// forgetFacts removes every recorded fact naming one of the purged identities
// and reports how many went. Facts survive re-indexing by design, so a purge
// that left them would keep answering questions about the person forever.
func forgetFacts(opts *options, purgeIDs []model.ID) (int, error) {
	ids := make(map[string]bool, len(purgeIDs))
	for _, id := range purgeIDs {
		if s := strings.ToLower(strings.TrimSpace(string(id))); s != "" {
			ids[s] = true
		}
	}
	store, err := opts.loadFacts()
	if err != nil {
		return 0, err
	}
	return store.ForgetMatching(func(f fact.Fact) bool {
		return factRefNames(f.Subject, ids) || factRefNames(f.Object, ids)
	})
}

// factRefNames reports whether a fact reference names one of the purged
// identities. References are kind:name by convention, so both the whole
// reference and the part after the kind are compared.
func factRefNames(ref string, ids map[string]bool) bool {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ids[ref] {
		return true
	}
	if i := strings.IndexByte(ref, ':'); i >= 0 && ids[ref[i+1:]] {
		return true
	}
	return false
}
