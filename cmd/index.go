package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/vault"
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

// newIndexCmd builds the index command, which ingests a source into the index.
func newIndexCmd(opts *options) *cobra.Command {
	var req engine.IndexRequest
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
			return runIndex(cmd, opts, &req)
		},
	}
	f := cmd.Flags()
	f.Var(newOnceValue(&req.Source, "org-csv"), "source",
		"Source type: org-csv, matters, slack, codeowners, github, jira, confluence, pagerduty, git, json, or graph.")
	engine.RegisterIndexFlags(f, &req.IndexArgs)
	return cmd
}

// runIndex reads one source and folds it into the index through the engine,
// recording the flags it ran with so a refresh can replay them.
func runIndex(cmd *cobra.Command, opts *options, req *engine.IndexRequest) error {
	x := opts.indexer(cmd)
	x.Record = func(source, scope string) error {
		return saveInvocation(opts, cmd, source, scope)
	}
	return x.Index(cmd.Context(), *req)
}

// indexer returns the engine indexer over the data directory, wired to the
// environment's codec and credentials and to a terminal passphrase prompt.
func (o *options) indexer(cmd *cobra.Command) *engine.Indexer {
	return &engine.Indexer{
		Dir:    o.dir(),
		Codec:  o.codec,
		Unlock: o.unlocker(cmd),
		Policy: o.pol,
		Creds:  secret.Resolve,
		Log:    cmd.ErrOrStderr(),
		Stdin:  cmd.InOrStdin(),
		Now:    o.clock,
	}
}

// unlocker returns the indexer's Unlock: the passphrase prompt the loaders
// share, refused outright for a file sealed under a raw key. A failure is
// remembered and returned at once on any later call, so one run never asks
// again after the operator could not open the index.
func (o *options) unlocker(cmd *cobra.Command) engine.UnlockFunc {
	var failed error
	return func(label string, try func(vault.Codec) error) (vault.Codec, error) {
		if failed != nil {
			return nil, failed
		}
		path := o.indexPath()
		if label == engine.EpisodesPassphrase {
			path = o.episodePath()
		}
		if err := refuseKeyModePrompt(path); err != nil {
			failed = err
			return nil, err
		}
		c, err := unlockAndLoad(cmd, o, label, func(c vault.Codec) (vault.Codec, error) {
			return c, try(c)
		})
		if err != nil {
			failed = err
			return nil, err
		}
		return c, nil
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

// indexRecords folds recs into the on-disk index through the engine's Fold.
func indexRecords(cmd *cobra.Command, opts *options, recs []connector.Record, p indexParams) error {
	return opts.indexer(cmd).Fold(cmd.Context(), recs, engine.FoldParams{
		Merge: p.merge, Incremental: p.incremental, AllowShrink: p.allowShrink, HalfLifeDays: p.halfLifeDays,
		AliasesFile: p.aliasesFile, Embed: p.embed, EmbedModel: p.embedModel, OllamaURL: p.ollamaURL,
		ChangesFile: p.changesFile, Episodes: p.episodes,
	})
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
