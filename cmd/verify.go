package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/render"
	"github.com/dcadolph/whotoask/internal/seal"
)

// newVerifyCmd builds the verify command, which checks a sealed finding or
// assessment offline.
func newVerifyCmd(opts *options) *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:   "verify DIR",
		Short: "Check a sealed finding or assessment offline",
		Long: `Check the seal in DIR. The signature in manifest.sig must verify over the exact
bytes of manifest.json, and every file the manifest lists must still have the
size and sha256 it records. Files that changed are reported as altered and files
that are gone as missing. Anything else in DIR, at any depth, is reported as
unlisted and fails the check, because the seal does not cover it: an added
file, a symbolic link, or a file left over from an earlier run. Only
manifest.json, manifest.sig, and the folder metadata an operating system writes
on its own (.DS_Store, Thumbs.db, desktop.ini) are left out. Nothing is sent
anywhere.

The signature proves the manifest is unchanged since it was signed, by the key
it names. To know whose key that is, get the producer's public key out of band,
from whotoask attest key on their install, and pass it with --key.

A failed check exits non-zero, so this can be chained in a script.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			report, err := seal.Verify(dir, key)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrSeal, err)
			}
			if err := opts.render(cmd.OutOrStdout(), report, func(w io.Writer, s style) {
				renderVerify(w, report, key != "", s)
			}); err != nil {
				return err
			}
			if !report.OK() {
				return fmt.Errorf("%w: %d altered, %d missing, %d unlisted", ErrSeal,
					len(report.Altered), len(report.Missing), len(report.Unlisted))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "",
		"Base64 public key the seal must be signed with, obtained from the producer out of band.")
	return cmd
}

// renderVerify writes the verdict on a sealed directory for a person.
func renderVerify(w io.Writer, r seal.Report, pinned bool, s style) {
	for _, p := range r.Altered {
		fmt.Fprintf(w, "%s %s\n", s.Warn("altered:"), p)
	}
	for _, p := range r.Missing {
		fmt.Fprintf(w, "%s %s\n", s.Warn("missing:"), p)
	}
	for _, p := range r.Unlisted {
		fmt.Fprintf(w, "%s %s\n", s.Warn("unlisted:"), p)
	}
	signer := "the key it names, " + r.KeyID
	if pinned {
		signer = "the key you gave, " + r.KeyID
	}
	if r.OK() {
		fmt.Fprintf(w, "%s %d file%s %s the seal, signed by %s\n", s.Accent("verified:"),
			len(r.Verified), plural(len(r.Verified)), render.Plural2(len(r.Verified), "matches", "match"), signer)
		return
	}
	fmt.Fprintf(w, "%s %d verified, %d altered, %d missing, %d unlisted, signed by %s\n",
		s.Warn("failed:"), len(r.Verified), len(r.Altered), len(r.Missing), len(r.Unlisted), signer)
}
