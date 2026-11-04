package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/keyring"
)

// wipeWord is what a person types to confirm a wipe. A word rather than a y/n
// answer, since the wipe cannot be undone and a reflexive "y" is too easy.
const wipeWord = "wipe"

// wipePlan is what a wipe would remove.
type wipePlan struct {
	// Files are the paths peepwho wrote under the data directory, present now.
	Files []string
	// Credentials are the keychain entries peepwho stored, present now.
	Credentials []string
}

// empty reports whether there is nothing to remove.
func (p wipePlan) empty() bool { return len(p.Files) == 0 && len(p.Credentials) == 0 }

// newWipeCmd builds the wipe command, which removes every file peepwho writes
// under the data directory and every credential it stored in the keychain.
func newWipeCmd(opts *options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "Delete everything peepwho stored on this machine",
		Long: `Delete every file peepwho writes under the data directory and every
credential peepwho connect saved in the OS keychain. The index, its sources,
remembered conversations and their archive, feedback, facts, the refresh config,
and the attestation signing key all go. It lists what it will remove and asks
you to type "wipe" first; --yes skips the question for scripts.

Files peepwho did not write are left in place, so pointing --data-dir at a
shared directory never deletes anything else. Environment variables and a
scheduled refresh are outside its reach, and it names any that remain.`,
		Example: `  peepwho wipe
  peepwho wipe --data-dir /var/lib/peepwho --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.ErrOrStderr()
			plan, err := planWipe(opts)
			if err != nil {
				return err
			}
			if plan.empty() {
				fmt.Fprintf(out, "nothing to wipe: no peepwho files in %s and no stored credentials\n",
					opts.dataDir)
				warnWipeLeftovers(out)
				return nil
			}
			printWipePlan(out, plan)
			if !yes && !confirmWipe(cmd.InOrStdin(), out) {
				fmt.Fprintln(out, "left everything as it was")
				return nil
			}
			if err := runWipe(opts, plan); err != nil {
				return err
			}
			fmt.Fprintf(out, "wiped %d files and %d stored credentials\n",
				len(plan.Files), len(plan.Credentials))
			warnWipeLeftovers(out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Wipe without asking.")
	return cmd
}

// planWipe lists the files and credentials present now that a wipe removes.
// The file half is the engine's, so an app wipes the same files.
func planWipe(opts *options) (wipePlan, error) {
	files, err := engine.PlanWipe(opts.dir())
	if err != nil {
		return wipePlan{}, err
	}
	plan := wipePlan{Files: files}
	keychain := opts.secrets()
	for _, name := range credentialNames() {
		if keychain.Stored(name) {
			plan.Credentials = append(plan.Credentials, name)
		}
	}
	return plan, nil
}

// credentialNames returns every credential name peepwho connect can store,
// sorted and without duplicates.
func credentialNames() []string {
	var names []string
	for _, spec := range connectSpecs() {
		for _, cf := range spec.creds {
			names = append(names, cf.env)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// printWipePlan writes what a wipe would remove.
func printWipePlan(w io.Writer, plan wipePlan) {
	fmt.Fprintln(w, "This deletes, and it cannot be undone:")
	for _, f := range plan.Files {
		fmt.Fprintf(w, "  %s\n", f)
	}
	for _, c := range plan.Credentials {
		fmt.Fprintf(w, "  keychain: %s\n", c)
	}
}

// confirmWipe asks the person to type the wipe word and reports whether they
// did. Anything else, including closed input, declines.
func confirmWipe(in io.Reader, w io.Writer) bool {
	fmt.Fprintf(w, "Type %q to delete these: ", wipeWord)
	var answer string
	if _, err := fmt.Fscanln(in, &answer); err != nil {
		return false
	}
	return strings.TrimSpace(answer) == wipeWord
}

// runWipe removes every planned file, then the data directory itself when
// nothing else is left in it, then every planned credential. It removes
// everything it can before reporting, so one stuck file does not leave the
// rest behind.
func runWipe(opts *options, plan wipePlan) error {
	errs := []error{engine.Wipe(opts.dir(), plan.Files)}
	keychain := opts.secrets()
	for _, name := range plan.Credentials {
		if err := keychain.Delete(name); err != nil {
			errs = append(errs, fmt.Errorf("wipe: remove keychain entry %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// warnWipeLeftovers names what a wipe cannot reach: credentials still set in
// the environment, and a scheduled refresh that would rebuild the index.
func warnWipeLeftovers(w io.Writer) {
	for _, name := range credentialNames() {
		if os.Getenv(name) != "" {
			fmt.Fprintf(w, "%s is still set in your environment; unset it yourself\n", name)
		}
	}
	for _, name := range []string{keyring.EnvKey, keyring.EnvPassphrase} {
		if os.Getenv(name) != "" {
			fmt.Fprintf(w, "%s is still set in your environment; unset it yourself\n", name)
		}
	}
	if plist, err := schedulePlistPath(); err == nil {
		if _, err := os.Stat(plist); err == nil {
			fmt.Fprintln(w, "a weekly refresh is still scheduled; remove it with: peepwho schedule --remove")
		}
	}
}
