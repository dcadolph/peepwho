package cmd

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/fact"
	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/keyring"
	"github.com/dcadolph/peepwho/internal/prompt"
	"github.com/dcadolph/peepwho/internal/vault"
)

// store returns the engine store over the data directory, sealed with the
// codec resolved from the environment or entered at a prompt.
func (o *options) store() (*engine.Store, error) {
	c, err := o.codec()
	if err != nil {
		return nil, err
	}
	return engine.NewStore(o.dir(), c).WithNow(o.clock), nil
}

// codec resolves the at-rest codec from the environment once and caches it on
// the options. A nil codec means the index is stored as plain JSON.
func (o *options) codec() (vault.Codec, error) {
	if o.codecResolved {
		return o.codecCache, nil
	}
	c, err := keyring.FromEnv()
	if err != nil {
		return nil, err
	}
	o.codecCache = c
	o.codecResolved = true
	return c, nil
}

// setCodec overrides the cached codec, used after an interactive passphrase so
// the matching save reuses it.
func (o *options) setCodec(c vault.Codec) {
	o.codecCache = c
	o.codecResolved = true
}

// loadIndex reads the index, decrypting when a key is configured. When the file
// is encrypted and no key is set, it prompts for a passphrase on a terminal, or
// returns a clear error pointing at the key variables when input is not a tty.
func (o *options) loadIndex(cmd *cobra.Command) (*index.Index, error) {
	st, err := o.store()
	if err != nil {
		return nil, err
	}
	ix, err := st.LoadIndex()
	if !errors.Is(err, vault.ErrEncrypted) {
		return ix, err
	}
	if kerr := refuseKeyModePrompt(o.indexPath()); kerr != nil {
		return nil, kerr
	}
	return unlockAndLoad(cmd, o, "Index passphrase", func(pc vault.Codec) (*index.Index, error) {
		return st.WithCodec(pc).LoadIndex()
	})
}

// refuseKeyModePrompt returns the error to show instead of a passphrase prompt
// when the file at path is sealed under a raw key. No passphrase can open such
// a file, so prompting for one wastes three attempts and ends in a message
// blaming the passphrase; pointing straight at the key variable is the only
// answer that works.
func refuseKeyModePrompt(path string) error {
	if !engine.NeedsKey(path) {
		return nil
	}
	return fmt.Errorf(
		"%w: %s is sealed under a raw key, which no passphrase can open; set %s",
		vault.ErrEncrypted, path, keyring.EnvKey)
}

// unlockAndLoad prompts for a passphrase and retries a few times on a wrong
// one, since a typo is the everyday cause of a failed decrypt and re-running
// the whole command to try again is a poor way to spend it. On success it
// records the working codec so a later save reuses it. It is shared by the
// index and episode loaders so both behave alike. When input is not a
// terminal it cannot prompt, so it points at the key variables instead.
func unlockAndLoad[T any](
	cmd *cobra.Command, o *options, label string, load func(vault.Codec) (T, error),
) (T, error) {
	var zero T
	ui := prompt.New(cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.ErrOrStderr())
	if !ui.Interactive() {
		return zero, fmt.Errorf(
			"%w: set %s or %s (see `peepwho vault keygen`)",
			vault.ErrEncrypted, keyring.EnvKey, keyring.EnvPassphrase)
	}
	const attempts = 3
	for i := range attempts {
		pass, perr := ui.Secret(label)
		if perr != nil {
			return zero, perr
		}
		pc := vault.NewPassphraseCipher([]byte(pass))
		v, err := load(pc)
		if err == nil {
			o.setCodec(pc)
			return v, nil
		}
		if !errors.Is(err, vault.ErrCorrupt) {
			return zero, err
		}
		if i < attempts-1 {
			fmt.Fprintln(cmd.ErrOrStderr(), "That passphrase did not work. Try again.")
		}
	}
	return zero, fmt.Errorf("%w: gave up after %d attempts", vault.ErrCorrupt, attempts)
}

// loadSources reads the sources sidecar into ix with the same key as the index,
// so a merge can rebuild from every source and not just the one being added.
func (o *options) loadSources(ix *index.Index) error {
	st, err := o.store()
	if err != nil {
		return err
	}
	return st.LoadSources(ix)
}

// saveIndex writes the index, encrypting it when a key is configured. It reuses
// any passphrase entered during a preceding loadIndex.
func (o *options) saveIndex(ix *index.Index) error {
	st, err := o.store()
	if err != nil {
		return err
	}
	return st.SaveIndex(ix)
}

// loadEpisodes reads the episode store, decrypting with the same key as the
// index. A missing file is an empty store, since a run that has never indexed
// episodes has no history rather than a problem. When the file is encrypted
// and no key is set, it prompts on a terminal, so `archive status` reaches an
// encrypted store the same way `ask` reaches an encrypted index.
func (o *options) loadEpisodes(cmd *cobra.Command) (*episode.Store, error) {
	st, err := o.store()
	if err != nil {
		return nil, err
	}
	store, err := st.LoadEpisodes()
	if !errors.Is(err, vault.ErrEncrypted) {
		return store, err
	}
	if kerr := refuseKeyModePrompt(o.episodePath()); kerr != nil {
		return nil, kerr
	}
	return unlockAndLoad(cmd, o, "Conversations passphrase", func(pc vault.Codec) (*episode.Store, error) {
		return st.WithCodec(pc).LoadEpisodes()
	})
}

// saveEpisodes writes the episode store, encrypting it when a key is
// configured.
func (o *options) saveEpisodes(store *episode.Store) error {
	st, err := o.store()
	if err != nil {
		return err
	}
	return st.SaveEpisodes(store)
}

// noIndexError wraps a load error as ErrNoIndex only when the index file is
// missing. Other errors, such as an encrypted index with no key, pass through so
// their own guidance survives.
func noIndexError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		// The first thing somebody runs is usually a question, and the answer
		// to a missing index is not the raw open error and a path. Point at the
		// two commands that get them somewhere: the demo needs nothing at all,
		// and connect is how a real index starts. Indexing is named last
		// because it is the step that needs credentials to already exist.
		return fmt.Errorf(
			"%w. Nothing is indexed yet.\n"+
				"  Try it on sample data with no setup:   peepwho demo\n"+
				"  Point it at your own tools:            peepwho connect\n"+
				"  Then build the index:                  peepwho index",
			ErrNoIndex)
	}
	if errors.Is(err, index.ErrDamaged) {
		// A file that exists but will not parse is the one failure where the
		// user cannot tell whether their data is gone. Say what to do: the
		// index is derived from the sources, so rebuilding it loses nothing
		// that was not already somewhere else.
		return fmt.Errorf(
			"%w.\n"+
				"  The index is rebuilt from the sources, so nothing is lost by making it again:\n"+
				"    peepwho index --source SOURCE\n"+
				"  If the file was meant to be encrypted, the key is missing rather than the data:\n"+
				"    set PEEPWHO_INDEX_KEY or PEEPWHO_INDEX_PASSPHRASE and try again",
			err)
	}
	return err
}

// loadFeedback opens the feedback store under the data directory, sealed with
// the same codec as the index so votes are never plaintext when the index is
// encrypted.
func (o *options) loadFeedback() (*feedback.Store, error) {
	st, err := o.store()
	if err != nil {
		return nil, err
	}
	return st.LoadFeedback()
}

// loadFacts opens the fact store under the data directory, sealed with the
// same codec as the index.
func (o *options) loadFacts() (*fact.Store, error) {
	st, err := o.store()
	if err != nil {
		return nil, err
	}
	return st.LoadFacts()
}
