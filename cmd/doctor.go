package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"

	"github.com/dcadolph/peepwho/internal/index"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/secret"
)

// staleAfter is how old an index may get before doctor flags it for a refresh.
const staleAfter = 30 * 24 * time.Hour

// checkLevel is a doctor finding's severity.
type checkLevel int

const (
	// levelOK marks a passing check.
	levelOK checkLevel = iota
	// levelWarn marks a check that works but should be addressed.
	levelWarn
	// levelFail marks a check that stops peepwho from answering.
	levelFail
)

// label renders the level as a fixed-width tag.
func (l checkLevel) label() string {
	switch l {
	case levelFail:
		return "FAIL"
	case levelWarn:
		return "WARN"
	default:
		return " OK "
	}
}

// finding is one doctor check result: what was examined, how it turned out, and
// the exact command that fixes it when it did not pass.
type finding struct {
	// Name is the short check name.
	Name string
	// Level is the severity.
	Level checkLevel
	// Detail is what doctor found.
	Detail string
	// Fix is the command that resolves the finding; empty when nothing to fix.
	Fix string
}

// doctorFacts is the plain snapshot diagnose reasons over, so the check logic is
// pure and testable apart from loading the index and resolving credentials.
type doctorFacts struct {
	// IndexPath is where the index is expected on disk.
	IndexPath string
	// IndexLoaded is true when the index opened successfully.
	IndexLoaded bool
	// IndexErr is why the index could not be loaded, nil when it was.
	IndexErr error
	// BuiltAt is when the index was last built; zero when unknown.
	BuiltAt time.Time
	// Now is the reference time for the freshness check.
	Now time.Time
	// People is how many people the index holds.
	People int
	// KeyConfigured is true when an at-rest encryption key is configured in the
	// environment. Key configuration is not file state: a key set after the
	// last write leaves the file plaintext until something writes it again.
	KeyConfigured bool
	// EncryptedOnDisk is true when the index file itself carries the vault
	// prefix, which is the only honest reading of "encrypted at rest".
	EncryptedOnDisk bool
	// Embeddings is true when the index carries vectors for semantic search.
	Embeddings bool
	// IndexSources names the sources already folded into the index.
	IndexSources []string
	// Configured maps a connector name to whether its credentials are present.
	Configured map[string]bool
	// DeclaredOwners is how many people a source of record names as owning
	// something.
	DeclaredOwners int
	// UnlinkedOwners is how many of those have no work recorded against them
	// anywhere. A source of record names people by handle and an activity
	// source names them by address, so the gap between the two is usually
	// identities that were never joined rather than people who left.
	UnlinkedOwners int
}

// diagnose runs every check against the facts and returns findings in report
// order. It never reads the environment or disk, so a test can drive any state.
// rootCause unwraps an error to its innermost message, so a reader sees what
// actually went wrong rather than the chain of packages it travelled through.
func rootCause(err error) string {
	if err == nil {
		return ""
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}

func diagnose(f doctorFacts) []finding {
	var out []finding

	if !f.IndexLoaded {
		// A first run has no index yet, which is not a malfunction and should
		// not read like one. Repeating the path and the layers of wrapping the
		// error picked up on its way here says nothing the reader can act on.
		detail := fmt.Sprintf("no index at %s yet", f.IndexPath)
		if !errors.Is(f.IndexErr, fs.ErrNotExist) {
			detail = fmt.Sprintf("cannot read the index at %s: %s", f.IndexPath, rootCause(f.IndexErr))
		}
		out = append(out, finding{
			Name:   "index",
			Level:  levelFail,
			Detail: detail,
			Fix:    "peepwho index --source org-csv --file people.csv",
		})
		// Nothing else is knowable without an index; the credential survey below
		// still helps the user get to a first index.
	} else {
		out = append(out, finding{
			Name:   "index",
			Level:  levelOK,
			Detail: fmt.Sprintf("%d people at %s", f.People, f.IndexPath),
		})
		if f.People == 0 {
			out = append(out, finding{
				Name:   "content",
				Level:  levelWarn,
				Detail: "index holds no people",
				Fix:    "peepwho index --source org-csv --file people.csv",
			})
		}
		out = append(out, freshnessFinding(f))
		out = append(out, staleSourceFindings(f)...)
		if !f.Embeddings {
			out = append(out, finding{
				Name:   "embeddings",
				Level:  levelWarn,
				Detail: "no vectors; semantic and llm ask modes are unavailable",
				Fix:    "peepwho index --merge --embed",
			})
		}
		out = append(out, encryptionFinding(f))
	}

	if fnd, ok := ownershipFinding(f); ok {
		out = append(out, fnd)
	}
	out = append(out, credentialFindings(f)...)
	return out
}

// ownerLinkage counts the people a source of record names as owners, and how
// many of them have no recorded work at all. Weight that a source of record
// assigned is not work, or every owner would look busy the moment the file is
// indexed.
func ownerLinkage(ix *index.Index) (declared, unlinked int) {
	for id, p := range ix.Graph.People {
		// A group named as an owner cannot be tied to an address, so it is not
		// a person whose work has gone missing.
		if len(p.Owns) == 0 || namesATeam(string(id)) {
			continue
		}
		declared++
		worked := false
		for tid, w := range p.Topics {
			if w-p.Stated[tid] > 0 {
				worked = true
				break
			}
		}
		if !worked {
			unlinked++
		}
	}
	return declared, unlinked
}

// unlinkedOwnerShare is the fraction of declared owners with no recorded work
// above which the gap is worth reporting rather than treating as turnover.
const unlinkedOwnerShare = 0.25

// ownershipFinding reports declared owners who have no work recorded against
// them. Some will have left, but a source of record names people by handle and
// an activity source names them by address, so most of a large gap is usually
// identities that were never joined. It matters because those owners look
// inactive everywhere they appear: the ownership report calls their areas
// drifted, and nobody is told the cause is fixable.
func ownershipFinding(f doctorFacts) (finding, bool) {
	if f.DeclaredOwners == 0 || f.UnlinkedOwners == 0 {
		return finding{}, false
	}
	share := float64(f.UnlinkedOwners) / float64(f.DeclaredOwners)
	detail := fmt.Sprintf("%d of %d declared owners have no work recorded against them",
		f.UnlinkedOwners, f.DeclaredOwners)
	if share < unlinkedOwnerShare {
		return finding{Name: "ownership", Level: levelOK, Detail: detail}, true
	}
	return finding{
		Name:   "ownership",
		Level:  levelWarn,
		Detail: detail + ", so their areas all read as drifted",
		Fix:    "peepwho identity --unlinked, then map those handles to addresses in an alias file",
	}, true
}

// encryptionFinding reports the index file's at-rest state from the file
// itself, never from key configuration. A key set after the last write leaves
// the file plaintext until the next save, and a doctor asserting "encrypted at
// rest" over a plaintext file is exactly the false assurance the check exists
// to prevent.
func encryptionFinding(f doctorFacts) finding {
	if f.EncryptedOnDisk {
		return finding{Name: "encryption", Level: levelOK, Detail: "index file is encrypted at rest"}
	}
	if f.KeyConfigured {
		return finding{
			Name:   "encryption",
			Level:  levelWarn,
			Detail: "an encryption key is configured but the index file on disk is still plaintext",
			Fix:    "peepwho vault encrypt",
		}
	}
	return finding{Name: "encryption", Level: levelOK, Detail: "index file is not encrypted at rest"}
}

// freshnessFinding checks how old the index is against staleAfter.
func freshnessFinding(f doctorFacts) finding {
	if f.BuiltAt.IsZero() {
		return finding{Name: "freshness", Level: levelWarn, Detail: "build time unknown", Fix: "peepwho index --full"}
	}
	age := f.Now.Sub(f.BuiltAt).Round(time.Hour)
	if age > staleAfter {
		return finding{
			Name:   "freshness",
			Level:  levelWarn,
			Detail: fmt.Sprintf("last built %s ago", age),
			Fix:    "peepwho index --merge",
		}
	}
	return finding{Name: "freshness", Level: levelOK, Detail: fmt.Sprintf("last built %s ago", age)}
}

// staleSourceFindings warns when a source already in the index has since lost
// the credentials needed to refresh it. A source with no credential need (org
// chart CSV, git, codeowners, json) is skipped.
func staleSourceFindings(f doctorFacts) []finding {
	var out []finding
	for _, name := range f.IndexSources {
		need, ok := f.Configured[name]
		if ok && !need {
			out = append(out, finding{
				Name:   "source:" + name,
				Level:  levelWarn,
				Detail: fmt.Sprintf("%s is indexed but its credentials are missing, so it cannot refresh", name),
				Fix:    "peepwho connect",
			})
		}
	}
	return out
}

// credentialFindings reports which live connectors are ready to run. It lists
// only connectors that need credentials; the file and repo sources never do.
func credentialFindings(f doctorFacts) []finding {
	names := make([]string, 0, len(f.Configured))
	for name := range f.Configured {
		names = append(names, name)
	}
	sort.Strings(names)
	ready := 0
	for _, name := range names {
		if f.Configured[name] {
			ready++
		}
	}
	if ready == 0 {
		return []finding{{
			Name:   "connectors",
			Level:  levelWarn,
			Detail: "no live connector credentials are set",
			Fix:    "peepwho connect",
		}}
	}
	var out []finding
	for _, name := range names {
		if f.Configured[name] {
			out = append(out, finding{Name: "connector:" + name, Level: levelOK, Detail: "credentials present"})
		}
	}
	return out
}

// writeFindings prints the report and returns how many findings failed.
func writeFindings(w io.Writer, findings []finding) int {
	fails := 0
	for _, fnd := range findings {
		if fnd.Level == levelFail {
			fails++
		}
		fmt.Fprintf(w, "[%s] %s: %s\n", fnd.Level.label(), fnd.Name, fnd.Detail)
		if fnd.Fix != "" {
			fmt.Fprintf(w, "       fix: %s\n", fnd.Fix)
		}
	}
	return fails
}

// configuredConnectors resolves which live connectors have their credentials
// set, reading only presence and never the secret value.
func configuredConnectors() map[string]bool {
	set := func(name string) bool { return secret.Resolve(name) != "" }
	jiraCreds := set(jiraURLEnv) && set(jiraTokenEnv)
	confluenceURL := set(confluenceURLEnv) || set(jiraURLEnv)
	confluenceToken := set(confluenceTokenEnv) || set(jiraTokenEnv)
	return map[string]bool{
		"slack":      set(slackTokenEnv),
		"github":     set(githubTokenEnv),
		"pagerduty":  set(pagerdutyTokenEnv),
		"graph":      set(graphTokenEnv),
		"jira":       jiraCreds,
		"confluence": confluenceURL && confluenceToken,
	}
}

// newDoctorCmd builds the doctor command: it diagnoses why peepwho cannot answer
// or is misconfigured, prints the exact fix for each problem, and exits nonzero
// when something is broken so it can gate a script.
func newDoctorCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration and index problems and print the fix for each",
		Long: `Check the index and the connector credentials, report what is wrong, and print
the exact command that fixes each problem. Exits nonzero when a problem stops
peepwho from answering, so it works as a gate in a script.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			facts := doctorFacts{
				IndexPath:  opts.indexPath(),
				Now:        time.Now(),
				Configured: configuredConnectors(),
			}
			if ix, err := opts.loadIndex(cmd); err != nil {
				facts.IndexErr = err
			} else {
				facts.IndexLoaded = true
				facts.DeclaredOwners, facts.UnlinkedOwners = ownerLinkage(ix)
				facts.People = len(ix.Graph.People)
				facts.BuiltAt = ix.BuiltAt()
				facts.Embeddings = ix.HasEmbeddings()
				facts.IndexSources = ix.SourceNames()
				if codec, cerr := opts.codec(); cerr == nil {
					facts.KeyConfigured = codec != nil
				}
				if enc, eerr := isEncryptedOnDisk(opts.indexPath()); eerr == nil {
					facts.EncryptedOnDisk = enc
				}
			}
			findings := diagnose(facts)
			fails := writeFindings(cmd.OutOrStdout(), findings)
			if fails > 0 {
				return fmt.Errorf("doctor: %d problem(s) need attention", fails)
			}
			return nil
		},
	}
	return cmd
}
