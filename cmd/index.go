package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/llm"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/util"
)

// Credential environment variables, named in the engine's source registry.
const (
	slackTokenEnv      = engine.SlackTokenEnv
	githubTokenEnv     = engine.GitHubTokenEnv
	pagerdutyTokenEnv  = engine.PagerDutyTokenEnv
	graphTokenEnv      = engine.GraphTokenEnv
	graphURLEnv        = engine.GraphURLEnv
	jiraURLEnv         = engine.JiraURLEnv
	jiraEmailEnv       = engine.JiraEmailEnv
	jiraTokenEnv       = engine.JiraTokenEnv
	confluenceURLEnv   = engine.ConfluenceURLEnv
	confluenceEmailEnv = engine.ConfluenceEmailEnv
	confluenceTokenEnv = engine.ConfluenceTokenEnv
)

// indexFlags holds the index command's flag values.
type indexFlags struct {
	// source is the --source name.
	source string
	// IndexArgs holds every other index flag.
	engine.IndexArgs
}

// newIndexCmd builds the index command, which ingests a source into the index.
func newIndexCmd(opts *options) *cobra.Command {
	var fl indexFlags
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
			return runIndex(cmd, opts, &fl)
		},
	}
	f := cmd.Flags()
	f.Var(newOnceValue(&fl.source, "org-csv"), "source",
		"Source type: org-csv, matters, slack, codeowners, github, jira, confluence, pagerduty, git, json, or graph.")
	engine.RegisterIndexFlags(f, &fl.IndexArgs)
	return cmd
}

// runIndex reads one source and folds it into the index: it decides whether the
// run is incremental, builds the source from the registry, indexes the records,
// and saves the refresh config and the watermark the next run resumes from.
func runIndex(cmd *cobra.Command, opts *options, fl *indexFlags) error {
	if fl.Archive {
		if err := guardArchive(cmd, opts); err != nil {
			return err
		}
	}
	scope := engine.IndexScope(fl.source, engine.ScopeInputs{
		JiraJQL: fl.JiraJQL, JiraProjects: fl.JiraProjects,
		ConfluenceCQL: fl.ConfluenceCQL, ConfluenceSpaces: fl.ConfluenceSpaces,
		GitHubRepos: fl.Repos, GitHubOrg: fl.GitHubOrg, SlackPrivate: fl.IncludePrivate,
	})
	// An explicit --jira-jql or --confluence-cql is authoritative, so the
	// connector ignores the watermark and returns the full result. Folding
	// that would stack a copy every run, so such a source is never treated
	// as incremental: it replaces on every merge, as a full read does.
	canInc := incrementalCapable(fl.source) && !engine.UsesExplicitQuery(fl.source, fl.JiraJQL, fl.ConfluenceCQL)
	since, incremental, err := indexWatermark(opts, fl, canInc, scope)
	if err != nil {
		return err
	}
	// Git keeps a position per repository rather than one time for the
	// whole source, so its incremental state is decided separately.
	// Getting this wrong is not a slow read but a wrong index: an
	// increment that is not folded REPLACES everything git contributed
	// with just the newest commits, and the shrink guard is the only
	// thing that catches it.
	var gitStops map[string]string
	if fl.source == "git" {
		gitStops = gitMarks(opts, fl.Merge && !fl.Full, fl.RepoPaths)
		incremental = len(gitStops) > 0
	}
	spec, ok := engine.SourceByName(fl.source)
	if !ok {
		return fmt.Errorf("%w: %q (want %s)", ErrUnknownSource, fl.source, engine.SourceNames())
	}
	o := fl.sourceOptions(cmd, opts, since, gitStops)
	o.Creds = resolveCreds(spec)
	recs, eps, src, err := fetchSource(cmd.Context(), spec, o)
	// Git resumes from a commit rather than a time, so it carries its own
	// marks: where this run stopped, saved below.
	var gitAfter map[string]string
	if g, isGit := src.(*connector.GitHistory); isGit {
		gitAfter = g.Marks()
	}
	if err != nil {
		return err
	}
	if err := indexRecords(cmd, opts, recs, indexParams{
		merge: fl.Merge, incremental: incremental, allowShrink: fl.AllowShrink, halfLifeDays: fl.HalfLifeDays,
		aliasesFile: fl.AliasesFile, embed: fl.Embed, embedModel: fl.EmbedModel, ollamaURL: fl.OllamaURL,
		changesFile: fl.ChangesFile, episodes: eps,
	}); err != nil {
		return err
	}
	return saveIndexProgress(cmd, opts, fl, scope, canInc, gitAfter, recs)
}

// indexWatermark decides whether this run is incremental: a merge into an
// existing index of a source that supports it, with a saved watermark and no
// --full. When so it returns the watermark, so the run fetches only what
// changed since and folds it in rather than re-reading the whole source.
func indexWatermark(opts *options, fl *indexFlags, canInc bool, scope string) (time.Time, bool, error) {
	if !fl.Merge || fl.Full || !canInc || !indexExists(opts) {
		return time.Time{}, false, nil
	}
	st, err := opts.loadState()
	if err != nil {
		return time.Time{}, false, err
	}
	if wm, ok := st.Get(fl.source, scope); ok {
		return wm.Cursor, true, nil
	}
	return time.Time{}, false, nil
}

// saveIndexProgress runs after a successful index. It records the flags this
// source was indexed with, so `peepwho refresh` and a scheduled refresh can
// replay them, then saves the position the next incremental run resumes from.
// The refresh config is keyed by scope, so indexing repo A and then repo B in
// separate runs keeps both; keyed by source alone, the second run silently
// replaced the first and the weekly refresh let A go stale forever. Saving it
// is best-effort: a failure warns but does not fail the index.
func saveIndexProgress(
	cmd *cobra.Command, opts *options, fl *indexFlags, scope string, canInc bool,
	gitAfter map[string]string, recs []connector.Record,
) error {
	rscope := engine.RefreshScope(fl.source, scope, fl.File, fl.RepoPaths)
	if err := saveInvocation(opts, cmd, fl.source, rscope); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save refresh config: %v\n", err)
	}
	// Git's position is a commit, not a time, so it saves its own.
	if len(gitAfter) > 0 {
		if err := saveGitMarks(opts, gitAfter); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save the git position: %v\n", err)
		}
	}
	// Advance the incremental watermark only after a successful index, so a
	// failed run never moves it.
	if canInc && fl.source != "git" {
		return updateWatermark(opts, fl.source, scope, fl.Full, recs)
	}
	return nil
}

// sourceOptions turns the flags into the registry's plain source options. The
// credentials are resolved separately, for the chosen source only.
func (fl *indexFlags) sourceOptions(
	cmd *cobra.Command, opts *options, since time.Time, gitStops map[string]string,
) engine.SourceOptions {
	episodes := fl.Episodes || fl.Archive
	return engine.SourceOptions{
		File:                 fl.File,
		Stdin:                cmd.InOrStdin(),
		Log:                  cmd.ErrOrStderr(),
		Since:                since,
		AllowPrivateChannels: opts.pol.AllowPrivateChannels(),
		Slack: connector.SlackOptions{
			IncludePrivate: fl.IncludePrivate, JoinPublic: fl.SlackJoin,
			SinceDays: fl.SinceDays, MaxMessages: fl.MaxMessages,
			Episodes: episodes, MaxEpisodesPerChannel: fl.MaxEpisodes,
			Archive: fl.Archive, MaxArchiveMessages: fl.MaxArchive,
		},
		SlackExport: connector.SlackExportOptions{
			IncludePrivate: fl.IncludePrivate, SinceDays: fl.SinceDays, MaxMessages: fl.MaxMessages,
			Episodes: episodes, MaxEpisodesPerChannel: fl.MaxEpisodes,
			Archive: fl.Archive, MaxArchiveMessages: fl.MaxArchive,
		},
		GitHub: connector.GitHubOptions{
			Repos: fl.Repos, Org: fl.GitHubOrg, MaxRepos: fl.MaxRepos,
			ResolveEmails: fl.GitHubEmails, Episodes: episodes,
		},
		JiraURL: fl.JiraURL,
		Jira: connector.JiraOptions{
			Projects: fl.JiraProjects, JQL: fl.JiraJQL, MaxIssues: fl.MaxIssues,
			Episodes: episodes, Server: fl.JiraServer,
		},
		ConfluenceURL: fl.ConfluenceURL,
		Confluence: connector.ConfluenceOptions{
			Spaces: fl.ConfluenceSpaces, CQL: fl.ConfluenceCQL, MaxPages: fl.MaxPages,
			Server: fl.ConfluenceServer,
		},
		PagerDuty: connector.PagerDutyOptions{Episodes: episodes},
		Git: connector.GitOptions{
			Paths: fl.RepoPaths, SinceDays: fl.GitSinceDays, UntilDays: fl.GitUntilDays,
			MaxCommits: fl.MaxCommits, Workers: fl.GitWorkers, StopAt: gitStops,
		},
	}
}

// resolveCreds reads the credentials the source names, and only those, so
// indexing one source never touches another's keychain entry.
func resolveCreds(spec engine.SourceSpec) map[string]string {
	creds := make(map[string]string, len(spec.Creds))
	for _, env := range spec.Creds {
		creds[env] = secret.Resolve(env)
	}
	return creds
}

// fetchSource builds the source from the registry and reads it. It is the
// engine's FetchSource, named here for the commands that share it.
func fetchSource(
	ctx context.Context, spec engine.SourceSpec, o engine.SourceOptions,
) ([]connector.Record, []episode.Episode, connector.Source, error) {
	return engine.FetchSource(ctx, spec, o)
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
		if err := engine.GuardShrink(existing, recs, p.allowShrink); err != nil {
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

// incrementalCapable reports whether a source supports incremental re-indexing,
// fetching only what changed since a watermark. Other sources read in full.
func incrementalCapable(source string) bool {
	spec, ok := engine.SourceByName(source)
	return ok && spec.Incremental
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
