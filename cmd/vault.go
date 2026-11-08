package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/keyring"
	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vault"
)

// newVaultCmd builds the vault command group for index encryption at rest.
func newVaultCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Encrypt peepwho's on-disk stores at rest",
		Long: `Encrypt what is on disk so a stolen laptop or a stray backup cannot read your
people graph or what peepwho kept beside it. Four stores are covered: the index,
the conversation store holding retained message text, the recorded facts, and
the feedback votes. Encryption turns on whenever a key is configured:

  PEEPWHO_INDEX_KEY         a base64 32-byte key, best for automation
  PEEPWHO_INDEX_PASSPHRASE  a passphrase, prompted if unset on a terminal

Generate a key with "peepwho vault keygen". With a key set, every write is
encrypted and every read decrypts. Reading an encrypted file without the key
fails rather than exposing anything.`,
	}
	cmd.AddCommand(
		newVaultStatusCmd(opts), newVaultKeygenCmd(), newVaultEncryptCmd(opts), newVaultDecryptCmd(opts))
	return cmd
}

// newVaultStatusCmd reports whether a key is configured and whether each store
// on disk is encrypted, without decrypting anything.
func newVaultStatusCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the key and encryption state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if src := keyring.Source(); src != "" {
				fmt.Fprintf(out, "key:           configured via %s\n", src)
			} else {
				fmt.Fprintln(out, "key:           none (every store is written as plain JSON)")
			}
			// Every store is reported, not just the index: the conversation
			// store holds retained message text, facts hold typed statements
			// about people, and votes hold the questions asked. A status that
			// spoke only for the index would call an install encrypted while
			// the sensitive files beside it sat in plain JSON.
			for _, store := range vaultStores(opts) {
				state, err := fileState(store.path, store.absent)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "%-14s %s (%s)\n", store.label+":", state, store.path)
			}
			return nil
		},
	}
}

// newVaultKeygenCmd prints a fresh key as an export line for the user to save.
func newVaultKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Print a new encryption key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, err := keyring.GenerateKey()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "export %s=%s\n", keyring.EnvKey, key)
			fmt.Fprintln(cmd.ErrOrStderr(),
				"Add this to your shell profile. Anyone with this key can read your index, "+
					"and losing it makes an encrypted index unrecoverable, so store it safely.")
			return nil
		},
	}
}

// vaultStore names one on-disk store vault status reports on.
type vaultStore struct {
	// label is the display name, e.g. "index".
	label string
	// path is the store's file path.
	path string
	// absent is what to say when the file does not exist yet.
	absent string
}

// vaultStores lists every store the vault commands cover, in report order.
func vaultStores(opts *options) []vaultStore {
	return []vaultStore{
		{label: "index", path: opts.indexPath(), absent: "no index yet"},
		{label: "conversations", path: opts.episodePath(), absent: "none kept yet"},
		{label: "facts", path: opts.factsPath(), absent: "none recorded yet"},
		{label: "feedback", path: opts.feedbackPath(), absent: "none recorded yet"},
		{label: "changes", path: opts.changesPath(), absent: "none logged yet"},
		{label: "refresh", path: opts.dir().Refresh(), absent: "none recorded yet"},
		{label: "watermarks", path: opts.dir().State(), absent: "none recorded yet"},
	}
}

// newVaultEncryptCmd rewrites the existing plain stores in their encrypted
// form. It covers everything beside the index too: the conversation store is
// where retained message text lives, and facts and votes name people and
// questions.
func newVaultEncryptCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt",
		Short: "Encrypt the existing plain stores in place",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := opts.codec()
			if err != nil {
				return err
			}
			if c == nil {
				return fmt.Errorf(
					"%w: no key configured; run `peepwho vault keygen` and set %s", ErrBadArgs, keyring.EnvKey)
			}
			enc, err := isEncryptedOnDisk(opts.indexPath())
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%w: run `peepwho index` first", ErrNoIndex)
			}
			if err != nil {
				return err
			}
			w := cmd.ErrOrStderr()
			if enc {
				fmt.Fprintln(w, "index is already encrypted")
			} else {
				ix, err := opts.loadIndex(cmd)
				if err != nil {
					return err
				}
				// Bring the sources sidecar in too so the save rewrites it under
				// the key; leaving it would encrypt the index but not its records.
				if err := opts.loadSources(ix); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if err := opts.saveIndex(ix); err != nil {
					return err
				}
				fmt.Fprintf(w, "encrypted %s\n", opts.indexPath())
			}
			if err := sealEpisodes(cmd, opts, w); err != nil {
				return err
			}
			if err := rewriteFacts(opts, w, c, "facts are already encrypted"); err != nil {
				return err
			}
			if err := rewriteFeedback(opts, w, c, "feedback is already encrypted"); err != nil {
				return err
			}
			if err := rewriteChanges(opts, w, c, "change log is already encrypted"); err != nil {
				return err
			}
			return rewriteRecords(opts, w, c, "already encrypted")
		},
	}
}

// newVaultDecryptCmd rewrites the encrypted stores back to plain JSON, needing
// the key that encrypted them.
func newVaultDecryptCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "decrypt",
		Short: "Decrypt the stores back to plain JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			enc, err := isEncryptedOnDisk(opts.indexPath())
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%w: run `peepwho index` first", ErrNoIndex)
			}
			if err != nil {
				return err
			}
			w := cmd.ErrOrStderr()
			if !enc {
				fmt.Fprintln(w, "index is already plain JSON")
			} else {
				ix, err := opts.loadIndex(cmd)
				if err != nil {
					return err
				}
				// Bring the sources sidecar in too so the plain save rewrites it;
				// leaving it would decrypt the index but not its records.
				if err := opts.loadSources(ix); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if err := ix.Save(opts.indexPath()); err != nil {
					return err
				}
				fmt.Fprintf(w, "decrypted %s\n", opts.indexPath())
			}
			if err := openEpisodes(cmd, opts, w); err != nil {
				return err
			}
			if err := rewriteFacts(opts, w, nil, "facts are already plain JSON"); err != nil {
				return err
			}
			if err := rewriteFeedback(opts, w, nil, "feedback is already plain JSON"); err != nil {
				return err
			}
			if err := rewriteChanges(opts, w, nil, "change log is already plain JSON"); err != nil {
				return err
			}
			return rewriteRecords(opts, w, nil, "already plain JSON")
		},
	}
}

// rewriteFacts re-persists an existing facts file under codec: sealed when a
// codec is given, plain JSON when nil. A decrypt that skipped it would strand
// the facts sealed after the user unsets the key, and an encrypt that skipped
// it would leave typed statements about people in plaintext beside the
// ciphertext index.
func rewriteFacts(opts *options, w io.Writer, codec vault.Codec, already string) error {
	enc, err := isEncryptedOnDisk(opts.factsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if enc == (codec != nil) {
		fmt.Fprintln(w, already)
		return nil
	}
	store, err := opts.loadFacts()
	if err != nil {
		return err
	}
	if err := store.Rewrite(codec); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s\n", rewriteVerb(codec), opts.factsPath())
	return nil
}

// rewriteFeedback re-persists an existing feedback file under codec, exactly
// as rewriteFacts does for facts. Votes are sealed on normal writes whenever a
// key is set, so leaving the file out of encrypt and decrypt is how they end
// up unreadable: sealed under a key the user has since unset, failing with a
// parse error instead of pointing at the key.
func rewriteFeedback(opts *options, w io.Writer, codec vault.Codec, already string) error {
	enc, err := isEncryptedOnDisk(opts.feedbackPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if enc == (codec != nil) {
		fmt.Fprintln(w, already)
		return nil
	}
	store, err := opts.loadFeedback()
	if err != nil {
		return err
	}
	if err := store.Rewrite(codec); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s\n", rewriteVerb(codec), opts.feedbackPath())
	return nil
}

// rewriteChanges re-persists an existing change log under codec, exactly as
// rewriteFeedback does for votes. The log names everyone who joined or left,
// so an encrypt that skipped it would leave those names in plaintext, and a
// decrypt that skipped it would strand it sealed and fail the next index.
func rewriteChanges(opts *options, w io.Writer, codec vault.Codec, already string) error {
	enc, err := isEncryptedOnDisk(opts.changesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if enc == (codec != nil) {
		fmt.Fprintln(w, already)
		return nil
	}
	// The same lock every index write takes, so a scheduled refresh cannot
	// append to the log between this read and write.
	unlock, err := util.LockFile(opts.indexPath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := opts.store()
	if err != nil {
		return err
	}
	if err := st.RewriteChanges(codec); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s\n", rewriteVerb(codec), opts.changesPath())
	return nil
}

// rewriteRecords re-persists the refresh config and the watermarks under codec, each
// under the lock its own writes take. They name the repositories, projects, and sites
// a person reads, so an encrypt that skipped them would leave those names in
// plaintext, and a decrypt that skipped them would strand them sealed and fail the
// next index.
func rewriteRecords(opts *options, w io.Writer, codec vault.Codec, already string) error {
	st, err := opts.store()
	if err != nil {
		return err
	}
	for _, rec := range []struct {
		path    string
		rewrite func(vault.Codec) error
	}{{opts.dir().Refresh(), st.RewriteInvocations}, {opts.dir().State(), st.RewriteState}} {
		enc, err := isEncryptedOnDisk(rec.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if enc == (codec != nil) {
			fmt.Fprintf(w, "%s is %s\n", filepath.Base(rec.path), already)
			continue
		}
		if err := rewriteLocked(rec.path, codec, rec.rewrite); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s %s\n", rewriteVerb(codec), rec.path)
	}
	return nil
}

// rewriteLocked runs rewrite under the lock every write of path takes, so a scheduled
// refresh cannot change the file between the read and the write.
func rewriteLocked(path string, codec vault.Codec, rewrite func(vault.Codec) error) error {
	unlock, err := util.LockFile(path + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	return rewrite(codec)
}

// rewriteVerb names what a rewrite under codec did to a file.
func rewriteVerb(codec vault.Codec) string {
	if codec != nil {
		return "encrypted"
	}
	return "decrypted"
}

// sealEpisodes encrypts the conversation store when one exists and is still
// plain. It holds retained message text, so a key covering only the index
// would leave the more sensitive of the two files readable on a stolen disk.
func sealEpisodes(cmd *cobra.Command, opts *options, w io.Writer) error {
	enc, err := isEncryptedOnDisk(opts.episodePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if enc {
		fmt.Fprintln(w, "conversations are already encrypted")
		return nil
	}
	store, err := opts.loadEpisodes(cmd)
	if err != nil {
		return err
	}
	if err := opts.saveEpisodes(store); err != nil {
		return err
	}
	fmt.Fprintf(w, "encrypted %s\n", opts.episodePath())
	return nil
}

// openEpisodes rewrites the conversation store as plain JSON, so decrypt leaves
// nothing sealed behind.
func openEpisodes(cmd *cobra.Command, opts *options, w io.Writer) error {
	enc, err := isEncryptedOnDisk(opts.episodePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !enc {
		fmt.Fprintln(w, "conversations are already plain JSON")
		return nil
	}
	store, err := opts.loadEpisodes(cmd)
	if err != nil {
		return err
	}
	if err := store.Save(opts.episodePath()); err != nil {
		return err
	}
	fmt.Fprintf(w, "decrypted %s\n", opts.episodePath())
	return nil
}

// fileState describes a store on disk for status: absent, encrypted, or plain.
// absent is what to say when the file is not there yet, which differs between
// the index and the conversations beside it.
func fileState(path, absent string) (string, error) {
	enc, err := isEncryptedOnDisk(path)
	if errors.Is(err, fs.ErrNotExist) {
		return absent, nil
	}
	if err != nil {
		return "", err
	}
	if enc {
		return "encrypted", nil
	}
	return "plain JSON", nil
}

// isEncryptedOnDisk reports whether the file at path carries the vault prefix,
// reading only the prefix rather than the whole index.
func isEncryptedOnDisk(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, vault.MagicLen)
	n, err := io.ReadFull(f, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return vault.IsEncrypted(buf[:n]), nil
}
