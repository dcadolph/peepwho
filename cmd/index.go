package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/llm"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/util"
)

// slackTokenEnv is the environment variable holding the Slack bot token.
const slackTokenEnv = "PEEPWHO_SLACK_TOKEN"

// githubTokenEnv is the environment variable holding the GitHub token.
const githubTokenEnv = "PEEPWHO_GITHUB_TOKEN"

// pagerdutyTokenEnv is the environment variable holding the PagerDuty token.
const pagerdutyTokenEnv = "PEEPWHO_PAGERDUTY_TOKEN"

// graphTokenEnv holds the Microsoft Graph bearer token; graphURLEnv overrides
// the Graph root for a sovereign or national cloud.
const (
	graphTokenEnv = "PEEPWHO_GRAPH_TOKEN"
	graphURLEnv   = "PEEPWHO_GRAPH_URL"
)

// Jira environment variables for the site URL, email, and API token.
const (
	jiraURLEnv   = "PEEPWHO_JIRA_URL"
	jiraEmailEnv = "PEEPWHO_JIRA_EMAIL"
	jiraTokenEnv = "PEEPWHO_JIRA_TOKEN"
)

// Confluence environment variables. They fall back to the Jira ones because
// both use the same Atlassian site and token.
const (
	confluenceURLEnv   = "PEEPWHO_CONFLUENCE_URL"
	confluenceEmailEnv = "PEEPWHO_CONFLUENCE_EMAIL"
	confluenceTokenEnv = "PEEPWHO_CONFLUENCE_TOKEN"
)

// newIndexCmd builds the index command, which ingests a source into the index.
func newIndexCmd(opts *options) *cobra.Command {
	var (
		source           string
		file             string
		includePrivate   bool
		slackJoin        bool
		sinceDays        int
		maxMessages      int
		episodes         bool
		maxEpisodes      int
		archive          bool
		maxArchive       int
		changesFile      string
		embed            bool
		embedModel       string
		ollamaURL        string
		repos            []string
		githubOrg        string
		maxRepos         int
		githubEmails     bool
		jiraURL          string
		jiraProjects     []string
		jiraServer       bool
		jiraJQL          string
		maxIssues        int
		merge            bool
		full             bool
		allowShrink      bool
		aliasesFile      string
		halfLifeDays     int
		repoPaths        []string
		gitSinceDays     int
		gitUntilDays     int
		gitWorkers       int
		maxCommits       int
		confluenceURL    string
		confluenceSpaces []string
		confluenceServer bool
		confluenceCQL    string
		maxPages         int
	)
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build the index from a source",
		Long: `Build or extend the index from one source per run. Combine sources with --merge;
people join across sources by email, or by an alias file (--aliases) when a
source only knows a handle. Dated activity decays per --half-life-days.

Re-indexing Jira, Confluence, GitHub, or Slack with --merge is incremental: it
fetches only what changed since the last run and folds it in, keeping everyone it
did not re-read. GitHub skips its whole-repo contributor and CODEOWNERS snapshots
on an incremental run, and Slack misses edits to messages older than the window,
so pass --full periodically to re-read everything and recompact.

Sources and their credentials:
  org-csv     --file people.csv                          none
  matters     --file time-entries.csv                    none
  codeowners  --file CODEOWNERS|repo-root                none
  git         --repo-path DIR (repeatable)               none
  json        --file FILE or - for stdin                 none
  slack       [--include-private] [--slack-join]         PEEPWHO_SLACK_TOKEN
  slack-export --file export.zip|DIR [--include-private]  none
  github      --repo o/r | --github-org ORG              PEEPWHO_GITHUB_TOKEN
  jira        --jira-project KEY | --jira-jql JQL        PEEPWHO_JIRA_URL/EMAIL/TOKEN
              (--jira-server for self-hosted Server/DC)   PEEPWHO_JIRA_URL[/TOKEN]
  confluence  --confluence-space KEY | --confluence-cql  PEEPWHO_CONFLUENCE_* (or Jira's)
              (--confluence-server for self-hosted)       PEEPWHO_CONFLUENCE_URL[/TOKEN]
  pagerduty   (no scope flags)                           PEEPWHO_PAGERDUTY_TOKEN
  graph       (no scope flags)                           PEEPWHO_GRAPH_TOKEN
              (PEEPWHO_GRAPH_URL for a sovereign cloud)

Start with the org chart, then merge everything else onto it:
  peepwho index --source org-csv --file people.csv
  peepwho index --source slack --merge
  peepwho index --source git --repo-path ~/src/billing --merge`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var (
				recs []connector.Record
				eps  []episode.Episode
				err  error
			)
			if archive {
				if err := guardArchive(cmd, opts); err != nil {
					return err
				}
			}
			// Decide whether this run is incremental: a merge into an existing
			// index of a source that supports it, with a saved watermark and no
			// --full. When so, fetch only what changed since the watermark and
			// fold it in rather than re-reading and replacing the whole source.
			scope := indexScope(source, scopeInputs{
				jiraJQL: jiraJQL, jiraProjects: jiraProjects,
				confluenceCQL: confluenceCQL, confluenceSpaces: confluenceSpaces,
				githubRepos: repos, githubOrg: githubOrg, slackPrivate: includePrivate,
			})
			// An explicit --jira-jql or --confluence-cql is authoritative, so the
			// connector ignores the watermark and returns the full result. Folding
			// that would stack a copy every run, so such a source is never treated
			// as incremental: it replaces on every merge, as a full read does.
			canInc := incrementalCapable(source) && !usesExplicitQuery(source, jiraJQL, confluenceCQL)
			var since time.Time
			var incremental bool
			// Git resumes from a commit rather than a time, so it carries its
			// own marks. gitAfter is where this run stopped, saved below.
			var gitAfter map[string]string
			if merge && !full && canInc && indexExists(opts) {
				st, serr := opts.loadState()
				if serr != nil {
					return serr
				}
				if wm, ok := st.Get(source, scope); ok {
					since, incremental = wm.Cursor, true
				}
			}
			// Git keeps a position per repository rather than one time for the
			// whole source, so its incremental state is decided separately.
			// Getting this wrong is not a slow read but a wrong index: an
			// increment that is not folded REPLACES everything git contributed
			// with just the newest commits, and the shrink guard is the only
			// thing that catches it.
			var gitStops map[string]string
			if source == "git" {
				gitStops = gitMarks(opts, merge && !full, repoPaths)
				incremental = len(gitStops) > 0
			}
			switch source {
			case "org-csv":
				if file == "" {
					return fmt.Errorf("%w: --file is required for org-csv", ErrBadArgs)
				}
				oc := connector.NewOrgCSV(file)
				oc.Log = cmd.ErrOrStderr()
				recs, err = oc.Fetch(cmd.Context())
			case "slack":
				recs, eps, err = fetchSlack(cmd, opts, slackArgs{
					includePrivate: includePrivate, joinPublic: slackJoin, sinceDays: sinceDays, maxMessages: maxMessages,
					episodes: episodes || archive, maxEpisodes: maxEpisodes,
					archive: archive, maxArchive: maxArchive, since: since,
				})
			case "slack-export":
				if file == "" {
					return fmt.Errorf("%w: --file (the export zip or its unzipped folder) is required for slack-export", ErrBadArgs)
				}
				se := connector.NewSlackExport(file, connector.SlackExportOptions{
					IncludePrivate:        includePrivate,
					SinceDays:             sinceDays,
					MaxMessages:           maxMessages,
					Episodes:              episodes || archive,
					MaxEpisodesPerChannel: maxEpisodes,
					Archive:               archive,
					MaxArchiveMessages:    maxArchive,
					Since:                 since,
					Log:                   cmd.ErrOrStderr(),
				})
				recs, err = se.Fetch(cmd.Context())
				eps = se.Episodes()
			case "matters":
				if file == "" {
					return fmt.Errorf("%w: --file (a time-entry or matter CSV export) is required for matters", ErrBadArgs)
				}
				ms := connector.NewMatters(file)
				ms.Log = cmd.ErrOrStderr()
				recs, err = ms.Fetch(cmd.Context())
			case "codeowners":
				if file == "" {
					return fmt.Errorf("%w: --file (CODEOWNERS path or repo root) required for codeowners", ErrBadArgs)
				}
				recs, err = connector.NewCodeOwners(file).Fetch(cmd.Context())
			case "github":
				recs, eps, err = fetchGitHub(cmd,
					githubArgs{repos, githubOrg, maxRepos, githubEmails, episodes || archive, since})
			case "jira":
				recs, eps, err = fetchJira(cmd,
					jiraArgs{jiraURL, jiraProjects, jiraJQL, maxIssues, episodes || archive, jiraServer, since})
			case "confluence":
				recs, err = fetchConfluence(cmd, confluenceArgs{confluenceURL, confluenceSpaces, confluenceCQL, maxPages, confluenceServer, since})
			case "pagerduty":
				recs, eps, err = fetchPagerDuty(cmd, episodes || archive)
			case "git":
				if len(repoPaths) == 0 {
					return fmt.Errorf("%w: --repo-path is required for git", ErrBadArgs)
				}
				// Where the last run stopped in each repository, so a refresh
				// reads what has happened since instead of the whole window
				// again. Reading two years of a large project takes minutes,
				// and almost none of it has changed since yesterday.
				gitSrc := connector.NewGitHistory(connector.GitOptions{
					Paths:      repoPaths,
					SinceDays:  gitSinceDays,
					UntilDays:  gitUntilDays,
					MaxCommits: maxCommits,
					Workers:    gitWorkers,
					StopAt:     gitStops,
					Log:        cmd.ErrOrStderr(),
				})
				recs, err = gitSrc.Fetch(cmd.Context())
				gitAfter = gitSrc.Marks()
			case "json":
				rc, closeJSON, jerr := jsonInput(cmd, file)
				if jerr != nil {
					return jerr
				}
				recs, err = connector.NewJSON(rc, "json").Fetch(cmd.Context())
				closeJSON()
			case "graph":
				recs, err = fetchGraph(cmd)
			default:
				return fmt.Errorf("%w: %q (want org-csv, slack, slack-export, codeowners, github, jira, confluence, pagerduty, git, json, or graph)", ErrUnknownSource, source)
			}
			if err != nil {
				return err
			}

			if err := indexRecords(cmd, opts, recs, indexParams{
				merge: merge, incremental: incremental, allowShrink: allowShrink, halfLifeDays: halfLifeDays,
				aliasesFile: aliasesFile, embed: embed, embedModel: embedModel, ollamaURL: ollamaURL,
				changesFile: changesFile, episodes: eps,
			}); err != nil {
				return err
			}
			// Advance the incremental watermark only after a successful index, so a
			// failed run never moves it. This is a no-op for a source that does not
			// support incremental re-indexing.
			// Record the flags this source was indexed with, so `peepwho refresh`
			// and a scheduled refresh can replay them. Keyed by scope, so indexing
			// repo A and then repo B in separate runs keeps both; keyed by source
			// alone, the second run silently replaced the first and the weekly
			// refresh let A go stale forever. Best-effort: a save failure warns
			// but does not fail the index.
			rscope := refreshScope(source, scope, file, repoPaths)
			if err := saveInvocation(opts, cmd, source, rscope); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save refresh config: %v\n", err)
			}
			// Git's position is a commit, not a time, so it saves its own.
			if len(gitAfter) > 0 {
				if err := saveGitMarks(opts, gitAfter); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save the git position: %v\n", err)
				}
			}
			if canInc && source != "git" {
				return updateWatermark(opts, source, scope, full, recs)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.Var(newOnceValue(&source, "org-csv"), "source",
		"Source type: org-csv, matters, slack, codeowners, github, jira, confluence, pagerduty, git, json, or graph.")
	f.StringVar(&file, "file", "", "Path to the source file: the CSV for org-csv, the CODEOWNERS file or repo root for codeowners, the JSON array for json (- for stdin).")
	f.BoolVar(&includePrivate, "include-private", false, "Ingest private Slack channels if policy allows.")
	f.BoolVar(&slackJoin, "slack-join", false,
		"Have the bot self-join public channels it is not in, so a workspace indexes without manual invites (needs channels:join; posts a join notice per channel).")
	f.IntVar(&sinceDays, "since-days", 180, "Slack history window in days.")
	f.IntVar(&maxMessages, "max-messages", 5000, "Slack message cap per channel.")
	f.BoolVar(&episodes, "episodes", false,
		"Record past conversations so peepwho recall can point back at them.")
	f.IntVar(&maxEpisodes, "max-episodes-per-channel", 200, "Episode cap per channel.")
	f.BoolVar(&archive, "archive", false,
		"Slack only: keep the words of each conversation, not just a link. Needs an index key, implies --episodes.")
	f.IntVar(&maxArchive, "max-archive-messages", 50, "Retained message cap per conversation.")
	f.StringVar(&changesFile, "changes-file", "", "Write the index diff as JSON to this path.")
	f.BoolVar(&merge, "merge", false, "Merge into the existing index instead of replacing it.")
	f.BoolVar(&full, "full", false,
		"With --merge, re-read every item and recompact instead of only what changed since the last run.")
	f.BoolVar(&allowShrink, "allow-shrink", false,
		"Accept a source returning far less than last time, which is otherwise refused as a truncated read.")
	f.StringVar(&aliasesFile, "aliases", "",
		"JSON file mapping a canonical id to its aliases, joining one person across sources.")
	f.IntVar(&halfLifeDays, "half-life-days", 180,
		"Days for a dated record's weight to halve; 0 disables recency decay.")
	f.StringArrayVar(&repoPaths, "repo-path", nil, "Local repository root for the git source (repeatable).")
	f.IntVar(&gitSinceDays, "git-since-days", 365, "Git history window in days.")
	f.IntVar(&maxCommits, "max-commits", 2000, "Commit cap per repository for the git source.")
	f.IntVar(&gitUntilDays, "git-until-days", 0,
		"Stop the git window this many days before today; 0 reads up to the present.")
	f.IntVar(&gitWorkers, "git-workers", 0,
		"Commits to diff at once for the git source; 0 picks a default from the machine.")
	f.BoolVar(&embed, "embed", false, "Generate embeddings via Ollama for semantic search.")
	f.StringVar(&embedModel, "embed-model", "", "Ollama embed model (default nomic-embed-text).")
	f.StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL for --embed.")
	f.StringArrayVar(&repos, "repo", nil, "GitHub repo owner/name (repeatable).")
	f.StringVar(&githubOrg, "github-org", "", "GitHub org to index all repositories of.")
	f.IntVar(&maxRepos, "max-repos", 0, "Cap repositories taken from --github-org (0 = all).")
	f.BoolVar(&githubEmails, "github-emails", false, "Resolve GitHub user emails to join other sources.")
	f.StringVar(&jiraURL, "jira-url", "", "Jira site URL (or PEEPWHO_JIRA_URL).")
	f.StringArrayVar(&jiraProjects, "jira-project", nil, "Jira project key (repeatable).")
	f.StringVar(&jiraJQL, "jira-jql", "", "Jira JQL query (overrides --jira-project).")
	f.IntVar(&maxIssues, "max-issues", 1000, "Cap Jira issues read.")
	f.BoolVar(&jiraServer, "jira-server", false,
		"Jira is self-hosted Server or Data Center (v2 API, bearer or anonymous auth), not Cloud.")
	f.StringArrayVar(&confluenceSpaces, "confluence-space", nil, "Confluence space key (repeatable).")
	f.StringVar(&confluenceURL, "confluence-url", "", "Confluence site URL; or PEEPWHO_CONFLUENCE_URL (falls back to the Jira URL).")
	f.StringVar(&confluenceCQL, "confluence-cql", "", "Confluence CQL query (overrides --confluence-space).")
	f.BoolVar(&confluenceServer, "confluence-server", false,
		"Confluence is self-hosted Server or Data Center (REST at site root, bearer/anonymous auth), not Cloud.")
	f.IntVar(&maxPages, "max-pages", 2000, "Cap Confluence pages read.")
	return cmd
}

// indexParams holds the index-build knobs shared by the index and connect
// commands: how records fold into the existing graph and how the result decays,
// embeds, and reports.
type indexParams struct {
	// merge adds records onto the existing index instead of replacing it.
	merge bool
	// incremental folds a since-limited fetch into the source's existing
	// records instead of replacing them, so a partial re-read keeps the people
	// and topics it did not re-fetch. It always runs inside a merge.
	incremental bool
	// allowShrink accepts a source that returned far less than it did last
	// time, which is otherwise refused as a truncated read.
	allowShrink bool
	// halfLifeDays halves a dated record's weight after this many days; 0 disables decay.
	halfLifeDays int
	// aliasesFile joins one person across sources by canonical id when set.
	aliasesFile string
	// embed generates embeddings via Ollama when true.
	embed bool
	// embedModel names the Ollama embedding model; empty uses the default.
	embedModel string
	// ollamaURL is the Ollama base URL for embedding.
	ollamaURL string
	// changesFile writes the index diff as JSON to this path when set.
	changesFile string
	// episodes are the conversations a source observed, stored beside the
	// index so recall can point back at them.
	episodes []episode.Episode
}

// indexRecords folds recs into the on-disk index and reports what changed. It
// loads the previous graph for the diff, builds or merges, auto-joins handle
// identities, canonicalizes, optionally embeds, then saves atomically. The index
// and connect commands share it so ingest lives in one place.
func indexRecords(cmd *cobra.Command, opts *options, recs []connector.Record, p indexParams) error {
	// The whole load-merge-save is one read-modify-write across three files:
	// the index, its sources sidecar, and the episodes beside them. Two runs
	// interleaving here, a Sunday 3am scheduled refresh over a manual --merge,
	// would each save over the other's records while both report success, so
	// the same cross-process lock the state file already takes guards it.
	unlock, err := util.LockFile(opts.indexPath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	// Load any existing index once. A missing file is a first run; any other
	// error, including an encrypted index with no key, aborts so re-indexing
	// never overwrites an index it could not read.
	existing, err := opts.loadIndex(cmd)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	var prev *model.Graph
	if existing != nil {
		prev = existing.Graph
	}

	ix := index.New(index.WithNow(opts.clock))
	if p.merge && existing != nil {
		// A merge rebuilds from every source, so the records for the sources
		// already indexed must come back from the sidecar first; without them
		// the rebuild would keep only the source being added.
		if err := opts.loadSources(existing); err != nil {
			return fmt.Errorf(
				"cannot merge: %w; run a full index once without --merge to rebuild the sources file", err)
		}
		ix = existing
	}
	ix.SetHalfLife(time.Duration(p.halfLifeDays) * 24 * time.Hour)
	if p.aliasesFile != "" {
		if err := ix.LoadAliases(p.aliasesFile); err != nil {
			return err
		}
	}
	// An incremental read returns only what changed, so it is legitimately far
	// smaller than the last full read; the shrink guard, which protects a full
	// replace from a truncated read, does not apply to it.
	if !p.incremental {
		if err := guardShrink(existing, recs, p.allowShrink); err != nil {
			return err
		}
	}
	switch {
	case p.incremental && existing != nil:
		ix.MergeIncremental(recs)
	case p.merge:
		ix.Add(recs)
	default:
		// A replacing run that read nothing would write an empty index over a
		// good one and report success. The usual cause is a token that expired
		// or lost a scope, which each connector reports as a skip rather than
		// as a failure, so the existing index is kept instead.
		if len(recs) == 0 {
			return fmt.Errorf(
				"%w: the existing index was left alone. Check the messages above: "+
					"the usual cause is a token that expired or lost a scope. "+
					"Use --merge to add to an index without replacing it", ErrNoRecords)
		}
		ix.Build(recs)
	}
	res := ix.AutoJoin()
	if res.Joined > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "auto-joined %d handle identities\n", res.Joined)
	}
	if len(res.Ambiguous) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"%d handle(s) share a name with more than one person and were left unresolved; add them to the alias file: %s\n",
			len(res.Ambiguous), strings.Join(res.Ambiguous, ", "))
	}
	ix.Canonicalize()

	if p.embed {
		if err := engine.GuardLLMHost(opts.pol, p.ollamaURL); err != nil {
			return err
		}
		total := len(ix.Graph.People) + len(ix.Graph.Channels)
		fmt.Fprintf(cmd.ErrOrStderr(),
			"embedding %d people and %d channels via Ollama...\n",
			len(ix.Graph.People), len(ix.Graph.Channels))
		ix.SetEmbedProgress(util.ProgressWriter(cmd.ErrOrStderr(), "embedded", embedProgressEvery(total)))
		if err := ix.Embed(cmd.Context(), engine.NewDocOllama(p.embedModel, p.ollamaURL)); err != nil {
			return fmt.Errorf("embed: %w", err)
		}
	}

	changes := index.Diff(prev, ix.Graph)
	if err := opts.saveIndex(ix); err != nil {
		return err
	}
	if err := saveEpisodes(cmd, opts, ix, p); err != nil {
		return err
	}

	out := cmd.ErrOrStderr()
	// Subjects and words are counted apart on purpose. A subject is something a
	// source stated: a label, a component, a directory. A word is something
	// mined from prose so a question phrased in somebody's own vocabulary still
	// matches. On a real issue tracker the mined words outnumber the stated
	// subjects four hundred to one, and reporting the total as "topics" made the
	// index look like it understood four hundred times more than it does.
	stated := 0
	for _, t := range ix.Graph.Topics {
		if t.Curated {
			stated++
		}
	}
	fmt.Fprintf(out,
		"indexed %d people, %d channels, %d teams, %d subjects and %d words from text into %s\n",
		len(ix.Graph.People), len(ix.Graph.Channels), len(ix.Graph.Teams),
		stated, len(ix.Graph.Topics)-stated, opts.indexPath())
	if c, _ := opts.codec(); c == nil && len(ix.Graph.People) > 0 {
		fmt.Fprintln(out,
			"note: this index holds names, emails, and titles in plaintext, protected only by file "+
				"permissions. Set PEEPWHO_INDEX_KEY or PEEPWHO_INDEX_PASSPHRASE to encrypt it at rest.")
	}
	reportChanges(out, changes)
	if p.changesFile != "" {
		if err := writeChangesFile(p.changesFile, changes); err != nil {
			return err
		}
	}
	return nil
}

// guardArchive refuses to retain conversation content unless the organization's
// own policy allows it and an encryption key is set. Both checks are local: the
// policy is a file the organization controls, and the key never leaves this
// machine.
func guardArchive(cmd *cobra.Command, opts *options) error {
	if !opts.pol.AllowArchive() {
		return fmt.Errorf("%w: keeping conversation content is disabled by policy", ErrBadArgs)
	}
	// An archive is the only store that holds readable conversation text, so it
	// must be encrypted at rest. Refuse to write it in plaintext rather than
	// leave full conversations on disk behind a file mode alone.
	codec, err := opts.codec()
	if err != nil {
		return err
	}
	if codec == nil {
		return fmt.Errorf(
			"%w: an archive retains the full text of conversations, so it must be encrypted at rest. "+
				"Set PEEPWHO_INDEX_KEY (base64 of 32 random bytes) or PEEPWHO_INDEX_PASSPHRASE and retry, "+
				"or drop --archive to keep only links", ErrBadArgs)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "archive: keeping the words of each conversation, encrypted at rest")
	return nil
}

// embedProgressEvery picks how often to report embedding progress: about
// twenty updates across the whole run, and at least every entity for a small
// graph, so the line moves without flooding.
func embedProgressEvery(total int) int {
	if total <= 20 {
		return 1
	}
	return total / 20
}

// incrementalCapable reports whether a source supports incremental re-indexing,
// fetching only what changed since a watermark. Other sources read in full until
// they gain the same support.
// jsonInput opens the JSON import source: stdin when file is empty or "-",
// otherwise the named file. The returned close func is always safe to call,
// so the caller closes unconditionally without tracking which branch ran.
func jsonInput(cmd *cobra.Command, file string) (io.Reader, func(), error) {
	if file == "" || file == "-" {
		return cmd.InOrStdin(), func() {}, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, fmt.Errorf("json source: open %s: %w", file, err)
	}
	return f, func() { _ = f.Close() }, nil
}

func incrementalCapable(source string) bool {
	return source == "jira" || source == "confluence" || source == "slack" ||
		source == "github" || source == "git"
}

// usesExplicitQuery reports whether a source is driven by a raw query the
// connector treats as authoritative. Such a query ignores the watermark and
// returns its full result, so the run must replace rather than fold, and no
// watermark is kept for it, keeping the raw query out of the plain state file.
func usesExplicitQuery(source, jiraJQL, confluenceCQL string) bool {
	switch source {
	case "jira":
		return strings.TrimSpace(jiraJQL) != ""
	case "confluence":
		return strings.TrimSpace(confluenceCQL) != ""
	default:
		return false
	}
}

// scopeInputs carries the query-scope flags that key a source's watermark.
type scopeInputs struct {
	// jiraJQL and jiraProjects scope a Jira read.
	jiraJQL      string
	jiraProjects []string
	// confluenceCQL and confluenceSpaces scope a Confluence read.
	confluenceCQL    string
	confluenceSpaces []string
	// githubRepos and githubOrg scope a GitHub read.
	githubRepos []string
	githubOrg   string
	// slackPrivate reports whether private channels are in scope.
	slackPrivate bool
}

// indexScope returns the watermark key for a source's current query, so changing
// the scope, such as the set of projects, spaces, or repositories indexed,
// starts a fresh watermark rather than reusing one taken over different items.
func indexScope(source string, in scopeInputs) string {
	switch source {
	case "jira":
		if strings.TrimSpace(in.jiraJQL) != "" {
			return "jql:" + in.jiraJQL
		}
		if len(in.jiraProjects) > 0 {
			return "project:" + sortedJoin(in.jiraProjects)
		}
		return "all"
	case "confluence":
		if strings.TrimSpace(in.confluenceCQL) != "" {
			return "cql:" + in.confluenceCQL
		}
		if len(in.confluenceSpaces) > 0 {
			return "space:" + sortedJoin(in.confluenceSpaces)
		}
		return "all"
	case "github":
		if in.githubOrg != "" {
			return "org:" + in.githubOrg
		}
		if len(in.githubRepos) > 0 {
			return "repos:" + sortedJoin(in.githubRepos)
		}
		return "all"
	case "slack":
		if in.slackPrivate {
			return "all"
		}
		return "public"
	default:
		return ""
	}
}

// sortedJoin returns the values sorted and comma-joined, so a scope key does not
// depend on the order the flags were given.
func sortedJoin(values []string) string {
	v := append([]string(nil), values...)
	sort.Strings(v)
	return strings.Join(v, ",")
}

// updateWatermark advances a source's incremental watermark to the newest
// activity just indexed, after a successful run. It never moves the cursor
// backward, and does nothing for a source without incremental support or a read
// that saw nothing dated.
func updateWatermark(opts *options, source, scope string, full bool, recs []connector.Record) error {
	if !incrementalCapable(source) {
		return nil
	}
	var cursor time.Time
	for _, r := range recs {
		if r.Time.After(cursor) {
			cursor = r.Time
		}
	}
	if cursor.IsZero() {
		return nil
	}
	// Serialize the read-modify-write on the state file, so two concurrent
	// index runs against different sources cannot drop each other's cursor.
	unlock, err := util.LockFile(opts.statePath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := opts.loadState()
	if err != nil {
		return err
	}
	// A partial read only ever adds newer activity, so never let its cursor fall
	// behind the stored one; a full read is authoritative and replaces it.
	if prev, ok := st.Get(source, scope); ok && !full && prev.Cursor.After(cursor) {
		cursor = prev.Cursor
	}
	st.Set(state.Watermark{Source: source, Scope: scope, Cursor: cursor, Complete: true, RanAt: opts.now()})
	return opts.saveState(st)
}

// guardShrink refuses to replace a source's contribution with a far smaller
// one. A rate limit or a scope a token quietly lost makes a connector keep
// what it managed to read and return no error, so without this a run that saw
// a fraction of a source would shrink the index while reporting success.
func guardShrink(existing *index.Index, recs []connector.Record, allow bool) error {
	if existing == nil || allow {
		return nil
	}
	incoming := make(map[string]int)
	for _, rec := range recs {
		incoming[rec.Source]++
	}
	for name, got := range incoming {
		if name == "" {
			continue
		}
		had := existing.SourceSize(name)
		if had > 0 && got*2 < had {
			return fmt.Errorf(
				"%w: %s returned %d records where it last returned %d, so the existing index "+
					"was left alone. Check the messages above for a rate limit or a token that "+
					"lost a scope. Pass --allow-shrink if the source really did get smaller",
				ErrShrunkSource, name, got, had)
		}
	}
	return nil
}

// saveEpisodes merges newly observed conversations into the episode store.
// Episodes always merge, even when the index itself is rebuilt: a source stops
// serving old messages long before they stop being worth remembering, so
// dropping them would throw away history peepwho may be the last to hold.
func saveEpisodes(cmd *cobra.Command, opts *options, ix *index.Index, p indexParams) error {
	eps := p.episodes
	if len(eps) == 0 {
		return nil
	}
	// Participants arrive as each source names them. Resolving them against
	// the graph is what makes one person's work findable across every tool.
	ix.CanonicalizeEpisodes(eps)
	// Note authors arrive source-native too, and only participants are
	// canonicalized above. Left as they came, a purge matching authors against
	// canonical identifiers misses them, so the two are resolved by the same
	// rule.
	for i := range eps {
		for j, n := range eps[i].Archive {
			if n.Author != "" {
				eps[i].Archive[j].Author = ix.CanonicalID(n.Author)
			}
		}
	}
	store, err := opts.loadEpisodes(cmd)
	if err != nil {
		return err
	}
	// A conversation can only be embedded while its text is in hand: the store
	// keeps terms, not messages, so an episode indexed without embeddings can
	// never gain them later.
	var embedder *llm.Ollama
	if p.embed {
		embedder = engine.NewDocOllama(p.embedModel, p.ollamaURL)
		fmt.Fprintf(cmd.ErrOrStderr(), "embedding %d conversations via Ollama...\n", len(eps))
	}
	before := store.Len()
	embedFailed := false
	for _, ep := range eps {
		// Include any retained archive text so semantic recall can match the
		// solution's meaning, not just the problem statement in the body. Keyword
		// postings already index the archive; the vector must too or an
		// archive is searchable by meaning only for the title and opener.
		body := strings.TrimSpace(ep.Body + " " + ep.Text() + " " + ep.ArchiveText())
		store.Add(ep)
		if embedder == nil || body == "" || embedFailed {
			continue
		}
		vec, err := embedder.Embed(cmd.Context(), body)
		if err != nil {
			// A failed embedding must not discard the conversations already
			// fetched: the index is on disk by now, so throwing these away
			// would leave the two out of step and waste the whole read. The
			// conversations are kept without vectors, so keyword recall works
			// and a later re-index can add the vectors.
			fmt.Fprintf(cmd.ErrOrStderr(),
				"embedding stopped (%v); keeping conversations without semantic vectors, "+
					"re-index with a working model to add them\n", err)
			embedFailed = true
			continue
		}
		store.SetVector(ep.ID, vec)
	}
	if err := opts.saveEpisodes(store); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"recorded %d conversations (%d new) into %s\n",
		len(eps), store.Len()-before, opts.episodePath())
	return nil
}

// slackArgs holds the Slack-specific index flags.
type slackArgs struct {
	// includePrivate requests private-channel ingest.
	includePrivate bool
	// joinPublic self-joins public channels before reading them.
	joinPublic bool
	// sinceDays is the history window in days.
	sinceDays int
	// maxMessages caps messages per channel.
	maxMessages int
	// episodes records the conversations behind the messages.
	episodes bool
	// maxEpisodes caps episodes kept per channel.
	maxEpisodes int
	// archive retains the content of each conversation.
	archive bool
	// maxArchive caps retained messages per conversation.
	maxArchive int
	// since limits an incremental read to messages posted at or after it; the
	// zero value reads the full window.
	since time.Time
}
