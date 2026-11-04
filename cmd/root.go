package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/vault"
)

// policyEnvVar names the environment variable pointing to an org policy file.
const policyEnvVar = "PEEPWHO_POLICY_FILE"

// defaultPolicyFile is the path an organization can pin a policy at.
const defaultPolicyFile = "/etc/peepwho/policy.json"

// nowEnvVar names the environment variable that pins the clock to one RFC 3339
// instant, so tests and benchmarks get reproducible output.
const nowEnvVar = "PEEPWHO_NOW"

// options holds the shared flags resolved on the root command.
type options struct {
	// dataDir is the directory holding the on-disk index.
	dataDir string
	// policyName is the requested egress policy mode.
	policyName string
	// pretty indents JSON output when true.
	pretty bool
	// jsonOut forces JSON output even at a terminal.
	jsonOut bool
	// humanOut forces human-readable output even when piped.
	humanOut bool
	// pol is the resolved egress policy, set before any subcommand runs.
	pol policy.Policy
	// systemPolicyFile is the org-pinned policy path, defaultPolicyFile in production.
	systemPolicyFile string
	// envPolicyFile is the PEEPWHO_POLICY_FILE override, read once at startup.
	envPolicyFile string
	// codecCache is the resolved at-rest codec, nil for plain JSON. It is set
	// once, lazily, so a passphrase entered during a load is reused for the save.
	codecCache vault.Codec
	// codecResolved reports whether codecCache has been resolved.
	codecResolved bool
	// keychain is the credential store, nil for the OS keychain. Tests set a
	// fake so nothing touches the real one.
	keychain *secret.Store
	// clock is the time source every command reads, nil for the system clock.
	// PEEPWHO_NOW pins it at startup.
	clock func() time.Time
}

// now returns the current time from the configured clock, or the system clock.
func (o *options) now() time.Time {
	if o.clock != nil {
		return o.clock()
	}
	return time.Now()
}

// resolveClock pins the clock to the instant in raw, an RFC 3339 time, when raw
// is set. An unparseable value is an error rather than a silent fallback to the
// system clock, since a caller who sets it is relying on the output it fixes.
func (o *options) resolveClock(raw string) error {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("%w: %s must be an RFC 3339 time such as 2026-11-01T12:00:00Z: %w",
			ErrBadArgs, nowEnvVar, err)
	}
	t = t.UTC()
	o.clock = func() time.Time { return t }
	return nil
}

// secrets returns the credential store: the configured one, or the OS keychain.
func (o *options) secrets() *secret.Store {
	if o.keychain != nil {
		return o.keychain
	}
	return secret.New()
}

// dir returns the data directory as the engine names its files.
func (o *options) dir() engine.Dir { return engine.Dir(o.dataDir) }

// indexPath returns the index file path under the data directory.
func (o *options) indexPath() string { return o.dir().Index() }

// feedbackPath returns the feedback file path under the data directory.
func (o *options) feedbackPath() string { return o.dir().Feedback() }

// changesPath returns the change log path under the data directory.
func (o *options) changesPath() string { return o.dir().Changes() }

// factsPath returns the facts file path under the data directory.
func (o *options) factsPath() string { return o.dir().Facts() }

// refreshPath returns the refresh-config path under the data directory.
func (o *options) refreshPath() string { return o.dir().Refresh() }

// episodePath returns the episode file path under the data directory.
func (o *options) episodePath() string { return o.dir().Episodes() }

// newRootCmd builds the root command, wires shared flags, and adds subcommands.
func newRootCmd() *cobra.Command {
	opts := &options{
		dataDir: defaultDataDir(), policyName: "strict", systemPolicyFile: defaultPolicyFile,
	}

	root := &cobra.Command{
		Use:           "peepwho",
		Short:         "Know who knows",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if (opts.jsonOut || opts.pretty) && opts.humanOut {
				return fmt.Errorf("%w: --human contradicts --json and --pretty; pass one side or the other",
					ErrBadArgs)
			}
			if err := opts.resolveClock(os.Getenv(nowEnvVar)); err != nil {
				return err
			}
			opts.envPolicyFile = os.Getenv(policyEnvVar)
			return opts.resolvePolicy(cmd.Flags().Changed("policy"), cmd.ErrOrStderr())
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&opts.dataDir, "data-dir", opts.dataDir, "Directory for the on-disk index.")
	pf.StringVar(&opts.policyName, "policy", opts.policyName, "Egress policy: strict, redacted, or open.")
	pf.BoolVar(&opts.pretty, "pretty", false, "Indent JSON output.")
	pf.BoolVar(&opts.jsonOut, "json", false, "Force JSON output, even at a terminal.")
	pf.BoolVar(&opts.humanOut, "human", false, "Force human-readable output, even when piped.")

	root.AddCommand(
		newIndexCmd(opts), newConnectCmd(opts), newAskCmd(opts), newRecallCmd(opts), newNearCmd(opts),
		newDirectoryCmd(opts), newIdentityCmd(opts), newSearchCmd(opts), newRiskCmd(opts), newOwnershipCmd(opts), newAssessCmd(opts), newRelatedCmd(opts), newAttestCmd(opts), newVerifyCmd(opts), newStatusCmd(opts), newDoctorCmd(opts), newServeCmd(opts), newBotCmd(opts), newEvalCmd(opts),
		newFeedbackCmd(opts), newFactCmd(opts), newForgetCmd(opts), newDemoCmd(opts), newMCPCmd(opts), newVaultCmd(opts),
		newRefreshCmd(opts), newScheduleCmd(opts),
		newArchiveCmd(opts), newWipeCmd(opts), newVersionCmd())
	return root
}

// resolvePolicy sets o.pol from the org policy files and the user's flag. A
// locked system policy always wins, even when PEEPWHO_POLICY_FILE names another
// file, so the env var cannot unlock a pinned machine. Otherwise the env file
// replaces the system file entirely and must exist, a locked file pins its
// mode over the flag, and an unlocked file supplies the default the flag may
// override.
func (o *options) resolvePolicy(policyChanged bool, errOut io.Writer) error {
	cfg, found, err := policy.Load(o.systemPolicyFile)
	if err != nil {
		return err
	}
	systemLocked := found && cfg.Locked
	if systemLocked && o.envPolicyFile != "" {
		fmt.Fprintln(errOut, "peepwho: "+policyEnvVar+" ignored; system policy is locked")
	}
	if !systemLocked && o.envPolicyFile != "" {
		envCfg, envFound, envErr := policy.Load(o.envPolicyFile)
		if envErr != nil {
			return envErr
		}
		// A named file that is not there is a typo or a missing mount, not a
		// request to run without policy. Falling through here used to discard
		// the system file entirely, including a private_channels pin, on a
		// path nobody chose.
		if !envFound {
			return fmt.Errorf("org policy: %s names %s, which does not exist", policyEnvVar, o.envPolicyFile)
		}
		cfg, found = envCfg, true
	}
	if found && cfg.Locked {
		pol, err := cfg.Policy()
		if err != nil {
			return fmt.Errorf("org policy: %w", err)
		}
		o.pol = pol
		if policyChanged {
			fmt.Fprintln(errOut, "peepwho: --policy ignored; pinned by org policy")
		}
		return nil
	}

	mode := o.policyName
	if found && !policyChanged && cfg.Mode != "" {
		mode = cfg.Mode
	}
	parsed, err := policy.ParseMode(mode)
	if err != nil {
		return err
	}
	// Start from the config so an unlocked file's private_channels pin survives,
	// then swap in the resolved mode. The base is unlocked here, so WithMode
	// only changes the mode.
	base := policy.Default()
	if found {
		if base, err = cfg.Policy(); err != nil {
			return fmt.Errorf("org policy: %w", err)
		}
	}
	o.pol, err = base.WithMode(parsed)
	return err
}

// defaultDataDir returns the default data directory under the user's home.
func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".peepwho")
	}
	return ".peepwho"
}
