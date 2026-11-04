package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/graph"
	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/prompt"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/slack"
)

// credField describes one credential or config value connect collects.
type credField struct {
	// env is the environment variable the value belongs in, e.g. PEEPWHO_SLACK_TOKEN.
	env string
	// label is the human prompt, e.g. "Slack bot token".
	label string
	// secret reads the value without echo when true.
	secret bool
	// hint is a short format reminder shown with the prompt, e.g. "starts with xoxb-".
	hint string
}

// sourceSpec is the static description of one connectable source: what it gives,
// how to create its credential, which values to collect, and how to validate
// them. The connect driver is one loop over this, not eight commands.
type sourceSpec struct {
	// id is the source identifier, matching the index --source value.
	id string
	// title is the display name, e.g. "Slack".
	title string
	// summary is the one-line "what you get".
	summary string
	// steps are the setup or credential click-path lines shown before collecting.
	steps []string
	// creds are the values to collect; empty for file and repo sources.
	creds []credField
	// authFix is the guidance shown when a credential is rejected.
	authFix string
	// validate makes a cheap read-only auth check from collected creds; nil for
	// sources that need no credentials.
	validate func(ctx context.Context, c map[string]string) error
}

// connectSpecs returns the source descriptions in setup order: the no-credential
// sources first, then everything that needs a token, matching the cookbook.
func connectSpecs() []sourceSpec {
	return append(fileSourceSpecs(),
		slackSpec(), githubSpec(), jiraSpec(), confluenceSpec(), pagerdutySpec(), graphSpec())
}

// fileSourceSpecs returns the sources read from local files and clones, which
// need no credentials.
func fileSourceSpecs() []sourceSpec {
	return []sourceSpec{
		{
			id:      "org-csv",
			title:   "Org chart (CSV)",
			summary: "The backbone of the graph: names, titles, teams, and topics every source joins onto by email.",
			steps: []string{
				"Point this at a CSV with a header row. Recognized columns: name, email,",
				"title, team, org, manager, and topics (semicolon-separated). A row needs a name or email.",
			},
		},
		{
			id:      "matters",
			title:   "Matters (billing CSV)",
			summary: "Who billed time to which matter, in which practice area, doing what. The universal export every practice-management system produces.",
			steps: []string{
				"Export time entries or matters as CSV from your billing or practice system.",
				"Recognized columns: timekeeper email, name, matter, practice area, narrative, date.",
			},
		},
		{
			id:      "codeowners",
			title:   "CODEOWNERS",
			summary: "Who owns which paths, straight from a repository's CODEOWNERS file.",
			steps:   []string{"Point this at a CODEOWNERS file or a repo root that contains one."},
		},
		{
			id:      "git",
			title:   "Git history",
			summary: "Who actually commits to what, read from local clones. Works for any repo you can clone.",
			steps:   []string{"Point this at one or more local repo roots. Bot accounts like dependabot are skipped."},
		},
	}
}

// slackSpec describes the Slack source and its bot token.
func slackSpec() sourceSpec {
	return sourceSpec{
		id:      "slack",
		title:   "Slack",
		summary: "The strongest single source: which channels exist, what they are about, and who is active on each topic.",
		steps: []string{
			"1. Go to https://api.slack.com/apps and choose Create New App, then From scratch.",
			"2. Under OAuth & Permissions, add these Bot Token Scopes:",
			"     channels:read  channels:history  users:read  users:read.email",
			"3. Install to Workspace, then copy the Bot User OAuth Token (starts with xoxb-).",
		},
		creds: []credField{
			{env: slackTokenEnv, label: "Slack bot token", secret: true, hint: "starts with xoxb-"},
		},
		authFix: "The token is wrong or missing a scope. Recreate it with the four scopes above, then re-enter.",
		validate: func(ctx context.Context, c map[string]string) error {
			return connector.NewSlack(c[slackTokenEnv], connector.SlackOptions{}).Ping(ctx)
		},
	}
}

// githubSpec describes the GitHub source and its token.
func githubSpec() sourceSpec {
	return sourceSpec{
		id:      "github",
		title:   "GitHub",
		summary: "Contributors, PR authors, reviewers, assignees, labels, issues, repo topics, and CODEOWNERS.",
		steps: []string{
			"1. Go to https://github.com/settings/tokens and create a token.",
			"2. Grant read access: a classic token needs the repo scope; a fine-grained token",
			"   needs read-only Contents, Metadata, Pull requests, and Issues on the target repos.",
			"3. Copy the token (ghp_... or github_pat_...).",
		},
		creds: []credField{
			{env: githubTokenEnv, label: "GitHub token", secret: true, hint: "ghp_... or github_pat_..."},
		},
		authFix: "The token is wrong, expired, or lacks read access to the repos. Recreate it and re-enter.",
		validate: func(ctx context.Context, c map[string]string) error {
			return connector.NewGitHub(c[githubTokenEnv], connector.GitHubOptions{}).Ping(ctx)
		},
	}
}

// jiraSpec describes the Jira source and its site, email, and API token.
func jiraSpec() sourceSpec {
	return sourceSpec{
		id:      "jira",
		title:   "Jira",
		summary: "Issue assignees and reporters, weighted by components, labels, summary words, and project.",
		steps: []string{
			"1. Go to https://id.atlassian.com/manage-profile/security/api-tokens.",
			"2. Click Create API token and copy it.",
			"3. You will also need your site URL and the account email that owns the token.",
		},
		creds: []credField{
			{env: jiraURLEnv, label: "Jira site URL", hint: "https://your-site.atlassian.net"},
			{env: jiraEmailEnv, label: "Atlassian account email", hint: "you@example.com"},
			{env: jiraTokenEnv, label: "Jira API token", secret: true},
		},
		authFix: "Check the email matches the account that owns the token, and the URL is the site root with no trailing path.",
		validate: func(ctx context.Context, c map[string]string) error {
			return connector.NewJira(c[jiraURLEnv], c[jiraEmailEnv], c[jiraTokenEnv], connector.JiraOptions{}).Ping(ctx)
		},
	}
}

// confluenceSpec describes the Confluence source, which shares the Atlassian token.
func confluenceSpec() sourceSpec {
	return sourceSpec{
		id:      "confluence",
		title:   "Confluence",
		summary: "Page creators and last editors, weighted by labels, title words, and space.",
		steps: []string{
			"Confluence uses the same Atlassian site and token as Jira. If you set up Jira,",
			"reuse those values below. Otherwise create a token at",
			"https://id.atlassian.com/manage-profile/security/api-tokens.",
		},
		creds: []credField{
			{env: confluenceURLEnv, label: "Confluence site URL", hint: "https://your-site.atlassian.net"},
			{env: confluenceEmailEnv, label: "Atlassian account email", hint: "you@example.com"},
			{env: confluenceTokenEnv, label: "Confluence API token", secret: true},
		},
		authFix: "Check the email matches the token owner, and the URL is the site root with no trailing path.",
		validate: func(ctx context.Context, c map[string]string) error {
			return connector.NewConfluence(
				c[confluenceURLEnv], c[confluenceEmailEnv], c[confluenceTokenEnv], connector.ConfluenceOptions{}).Ping(ctx)
		},
	}
}

// pagerdutySpec describes the PagerDuty source and its read-only key.
func pagerdutySpec() sourceSpec {
	return sourceSpec{
		id:      "pagerduty",
		title:   "PagerDuty",
		summary: "Every service and who is on call, so each on-call person gets the topics they answer for.",
		steps: []string{
			"1. In PagerDuty, go to Integrations > API Access Keys.",
			"2. Create a read-only API key and copy it.",
		},
		creds: []credField{
			{env: pagerdutyTokenEnv, label: "PagerDuty API token", secret: true, hint: "read-only key"},
		},
		authFix: "The token is wrong. Create a read-only API key and re-enter.",
		validate: func(ctx context.Context, c map[string]string) error {
			return connector.NewPagerDuty(c[pagerdutyTokenEnv], connector.PagerDutyOptions{}).Ping(ctx)
		},
	}
}

// graphSpec describes the Microsoft Graph source and its bearer token.
func graphSpec() sourceSpec {
	return sourceSpec{
		id:      "graph",
		title:   "Microsoft Graph",
		summary: "The org chart from Entra ID: names, titles, and reporting lines, joined onto everything else by email.",
		steps: []string{
			"1. In Entra admin center, register an application and grant it User.Read.All (application).",
			"2. Have an admin consent to the permission.",
			"3. Acquire a bearer token for https://graph.microsoft.com and export it.",
			"Tokens expire quickly, so mint one right before indexing, or wire your own refresh.",
		},
		creds: []credField{
			{env: graphTokenEnv, label: "Microsoft Graph bearer token", secret: true},
			{env: graphURLEnv, label: "Graph base URL (blank for public cloud)", hint: "only for sovereign clouds"},
		},
		authFix: "The token is expired or missing User.Read.All. Mint a fresh one and re-enter.",
		validate: func(ctx context.Context, c map[string]string) error {
			return graph.New(c[graphTokenEnv], graph.WithBaseURL(c[graphURLEnv])).Ping(ctx)
		},
	}
}

// newConnectCmd builds the connect command, the interactive setup wizard.
func newConnectCmd(opts *options) *cobra.Command {
	var status, forget bool
	cmd := &cobra.Command{
		Use:   "connect [source]",
		Short: "Set up a source interactively",
		Long: `Set up a source with a guided flow: it explains what the source gives you, shows
how to create the credential, collects and validates it, runs the first index,
and shows how to ask.

Run with no argument for a menu of every source and whether it is configured.
Run with a source to set up just that one. Use --status for a non-interactive
report. Credentials are validated in memory and never logged or written to any
peepwho file. Connect then offers to save them to your OS keychain so future
runs stay connected; decline and it prints export lines to save yourself.
--forget removes a source's saved credentials from the keychain again.

Sources: org-csv, matters, codeowners, git, slack, github, jira, confluence, pagerduty, graph.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if status {
				return runStatus(cmd, opts)
			}
			if forget {
				if len(args) != 1 {
					return fmt.Errorf("%w: name the source to forget, e.g. peepwho connect slack --forget", ErrBadArgs)
				}
				spec, ok := specByID(args[0])
				if !ok {
					return fmt.Errorf("%w: %q (want %s)", ErrUnknownSource, args[0], strings.Join(specIDs(), ", "))
				}
				return runForget(cmd, spec)
			}
			var chosen *sourceSpec
			if len(args) == 1 {
				spec, ok := specByID(args[0])
				if !ok {
					return fmt.Errorf("%w: %q (want %s)", ErrUnknownSource, args[0], strings.Join(specIDs(), ", "))
				}
				chosen = &spec
			}
			ui := prompt.New(cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.ErrOrStderr())
			if !ui.Interactive() {
				return fmt.Errorf(
					"%w: connect needs an interactive terminal; use 'peepwho index' for scripts (see docs/CONNECT.md)",
					ErrBadArgs)
			}
			if chosen != nil {
				return quietAbort(runConnect(cmd, opts, ui, *chosen))
			}
			return quietAbort(runMenu(cmd, opts, ui))
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "Report which sources are configured, without prompting.")
	cmd.Flags().BoolVar(&forget, "forget", false, "Remove a source's saved credentials from the OS keychain.")
	return cmd
}

// runMenu shows every source with its configured state and sets up the chosen one.
func runMenu(cmd *cobra.Command, opts *options, ui *prompt.IO) error {
	specs := connectSpecs()
	ui.Blank()
	ui.Step("Connect a source")
	ui.Detail("Pick a source to set up or refresh. Its current state is shown.")
	ui.Blank()
	labels := make([]string, len(specs))
	for i, s := range specs {
		labels[i] = fmt.Sprintf("%-11s %s", s.id, statusNote(s))
	}
	idx, err := ui.Choose("Source", labels)
	if err != nil {
		return err
	}
	return runConnect(cmd, opts, ui, specs[idx])
}

// runConnect walks one source: explain, create the credential, collect and
// validate it, print the export line, run the first index, and show what is next.
func runConnect(cmd *cobra.Command, opts *options, ui *prompt.IO, spec sourceSpec) error {
	ui.Blank()
	ui.Step("Connect %s", spec.title)
	ui.Detail("%s", spec.summary)
	ui.Blank()
	printPrivacy(ui, opts)

	if len(spec.steps) > 0 {
		ui.Blank()
		if len(spec.creds) > 0 {
			ui.Step("Create the credential")
		} else {
			ui.Step("Before you start")
		}
		for _, s := range spec.steps {
			ui.Detail("%s", s)
		}
	}

	creds := map[string]string{}
	if len(spec.creds) > 0 {
		ui.Blank()
		ui.Step("Enter your credentials")
		var err error
		if creds, err = collectAndValidate(cmd, ui, spec); err != nil {
			return err
		}
		if err := persistCreds(ui, spec, creds); err != nil {
			return err
		}
	}

	ui.Blank()
	run, err := ui.Confirm(fmt.Sprintf("Index %s now?", spec.title), true)
	if err != nil {
		return err
	}
	if !run {
		ui.Hint("Index later with:  peepwho index --source %s%s", spec.id, mergeSuffix(opts))
	} else {
		ui.Blank()
		ui.Step("Indexing %s", spec.title)
		if ierr := runFirstIndex(cmd, opts, ui, spec, creds); ierr != nil {
			ui.Blank()
			ui.Fail("Indexing failed: %v", ierr)
			return ierr
		}
		ui.Success("%s indexed.", spec.title)
	}

	ui.Blank()
	ui.Step("Next")
	ui.Detail("Ask a question:")
	ui.Command(`peepwho ask "who owns billing retries"`)
	ui.Detail("Open the web UI:")
	ui.Command("peepwho serve")
	ui.Blank()
	return nil
}

// collectAndValidate collects each credential and validates it against the
// source, re-prompting until it is accepted or the user backs out.
func collectAndValidate(cmd *cobra.Command, ui *prompt.IO, spec sourceSpec) (map[string]string, error) {
	for {
		creds, err := collect(ui, spec)
		if err != nil {
			return nil, err
		}
		ui.Blank()
		ui.Step("Validating")
		verr := spec.validate(cmd.Context(), creds)
		if verr == nil {
			ui.Success("Credentials accepted.")
			return creds, nil
		}
		ui.Fail("%s", explainAuthError(spec, verr))
		again, cerr := ui.Confirm("Try again?", true)
		if cerr != nil {
			return nil, cerr
		}
		if !again {
			return nil, prompt.ErrAborted
		}
		ui.Blank()
	}
}

// collect prompts for each credential, reusing a value already in the
// environment when the user allows it, and reading secrets without echo.
func collect(ui *prompt.IO, spec sourceSpec) (map[string]string, error) {
	creds := make(map[string]string, len(spec.creds))
	for _, cf := range spec.creds {
		if existing := secret.Resolve(cf.env); existing != "" {
			use, err := ui.Confirm(fmt.Sprintf("%s is already set. Use it?", cf.env), true)
			if err != nil {
				return nil, err
			}
			if use {
				creds[cf.env] = existing
				continue
			}
		}
		label := cf.label
		if cf.hint != "" {
			label = fmt.Sprintf("%s (%s)", cf.label, cf.hint)
		}
		var (
			v   string
			err error
		)
		if cf.secret {
			v, err = ui.Secret(label)
		} else {
			v, err = ui.Line(label)
		}
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, fmt.Errorf("%w: %s is required", ErrBadArgs, cf.env)
		}
		creds[cf.env] = v
	}
	return creds, nil
}

// persistCreds offers to store the collected credentials in the OS keychain so
// future runs read them without environment variables, falling back to printing
// export lines when the keychain is declined or unavailable. connect never edits
// dotfiles.
func persistCreds(ui *prompt.IO, spec sourceSpec, creds map[string]string) error {
	ui.Blank()
	ui.Step("Make it permanent")
	save, err := ui.Confirm("Save these to your OS keychain?", true)
	if err != nil {
		return err
	}
	if save {
		if kerr := saveToKeychain(ui, spec, creds); kerr == nil {
			return nil
		}
		// A headless or locked keychain must never leave the user stuck, so fall
		// through to export lines they can persist by hand.
		ui.Detail("Could not use the keychain; add these to your shell profile instead.")
	} else {
		ui.Detail("Add these to your shell profile so future runs stay connected.")
	}
	ui.Detail("They hold your credentials, so treat that file as a secret.")
	ui.Blank()
	printExports(ui, spec, creds)
	return nil
}

// saveToKeychain writes each collected credential to the OS keychain. An error
// from the store aborts so the caller can fall back to export lines.
func saveToKeychain(ui *prompt.IO, spec sourceSpec, creds map[string]string) error {
	for _, cf := range spec.creds {
		v := creds[cf.env]
		if v == "" {
			continue
		}
		if err := secret.Save(cf.env, v); err != nil {
			return err
		}
	}
	ui.Success("Saved to your OS keychain. Future runs read them from there, no environment variables needed.")
	return nil
}

// printExports prints the export lines the user adds to their shell profile,
// naming a secret variable without echoing its value.
func printExports(ui *prompt.IO, spec sourceSpec, creds map[string]string) {
	for _, cf := range spec.creds {
		v := creds[cf.env]
		if v == "" {
			continue
		}
		if cf.secret {
			// Do not echo the secret to the terminal, where it would land in
			// scrollback and shell history. The user just entered it, so name the
			// variable and let them paste the value in themselves.
			ui.Command("export %s='...'   # paste the value you just entered", cf.env)
			continue
		}
		ui.Command("export %s=%s", cf.env, v)
	}
}

// runFirstIndex loads the freshly collected credentials into the environment for
// this run, prompts for any source-specific scope, then reuses the same fetch and
// index path as the index command. Credentials stay in memory; nothing is written
// to disk beyond the local index the index command already writes.
func runFirstIndex(cmd *cobra.Command, opts *options, ui *prompt.IO, spec sourceSpec, creds map[string]string) error {
	for k, v := range creds {
		if v == "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}

	merge := indexExists(opts)
	o, err := firstIndexOptions(opts, ui, spec.id)
	if err != nil {
		return err
	}
	src, ok := engine.SourceByName(spec.id)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSource, spec.id)
	}
	o.Log = cmd.ErrOrStderr()
	o.AllowPrivateChannels = opts.pol.AllowPrivateChannels()
	o.Creds = resolveCreds(src)
	recs, eps, _, err := fetchSource(cmd.Context(), src, o)
	if err != nil {
		return err
	}
	return indexRecords(cmd, opts, recs, indexParams{merge: merge, halfLifeDays: 180, episodes: eps})
}

// firstIndexOptions prompts for the scope a first index of the source needs and
// returns it as registry options, with the defaults the index command uses.
// Merged changes, resolved tickets, and resolved incidents are recorded as a
// matter of course: they are the same metadata already being indexed, and they
// are what recall points back at.
func firstIndexOptions(opts *options, ui *prompt.IO, id string) (engine.SourceOptions, error) {
	var o engine.SourceOptions
	switch id {
	case "slack":
		includePrivate := false
		if opts.pol.AllowPrivateChannels() {
			var err error
			if includePrivate, err = ui.Confirm("Include private channels the bot can read?", false); err != nil {
				return o, err
			}
		}
		remember, err := ui.Confirm(
			"Remember conversations, so peepwho recall can find who helped you and when?", true)
		if err != nil {
			return o, err
		}
		o.Slack = connector.SlackOptions{
			IncludePrivate: includePrivate, SinceDays: 180, MaxMessages: 5000,
			Episodes: remember, MaxEpisodesPerChannel: 200,
		}
	case "github":
		gho, err := promptGitHubScope(ui)
		if err != nil {
			return o, err
		}
		gho.Episodes = true
		o.GitHub = gho
	case "jira":
		projects, err := promptList(ui, "Jira project keys to index (space-separated, blank for all)")
		if err != nil {
			return o, err
		}
		o.Jira = connector.JiraOptions{Projects: projects, MaxIssues: 1000, Episodes: true}
	case "confluence":
		spaces, err := promptList(ui, "Confluence space keys to index (space-separated, blank for all)")
		if err != nil {
			return o, err
		}
		o.Confluence = connector.ConfluenceOptions{Spaces: spaces, MaxPages: 2000}
	case "pagerduty":
		o.PagerDuty = connector.PagerDutyOptions{Episodes: true}
	case "org-csv", "codeowners":
		return localFileOptions(ui, id)
	case "git":
		paths, err := promptList(ui, "Local repo paths (space-separated)")
		if err != nil {
			return o, err
		}
		if len(paths) == 0 {
			return o, fmt.Errorf("%w: at least one repo path is required for git", ErrBadArgs)
		}
		o.Git = connector.GitOptions{Paths: paths, SinceDays: 365, MaxCommits: 2000}
	default:
		return o, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}
	return o, nil
}

// localFileOptions prompts for the required path of a file source.
func localFileOptions(ui *prompt.IO, id string) (engine.SourceOptions, error) {
	label := "Path to the org chart CSV"
	if id == "codeowners" {
		label = "Path to a CODEOWNERS file or a repo root"
	}
	path, err := ui.Line(label)
	if err != nil {
		return engine.SourceOptions{}, err
	}
	if path == "" {
		return engine.SourceOptions{}, fmt.Errorf("%w: a path is required", ErrBadArgs)
	}
	return engine.SourceOptions{File: path}, nil
}

// promptGitHubScope asks whether to index a single repo or a whole org and
// returns the matching fetch arguments, resolving emails so people join sources.
func promptGitHubScope(ui *prompt.IO) (connector.GitHubOptions, error) {
	ui.Detail("Index a single repo (owner/name) or a whole org.")
	repo, err := ui.Line("GitHub repo owner/name, or blank to index an org")
	if err != nil {
		return connector.GitHubOptions{}, err
	}
	gho := connector.GitHubOptions{ResolveEmails: true}
	if repo != "" {
		gho.Repos = []string{repo}
		return gho, nil
	}
	org, err := ui.Line("GitHub org to index")
	if err != nil {
		return connector.GitHubOptions{}, err
	}
	if org == "" {
		return connector.GitHubOptions{}, fmt.Errorf("%w: a repo or an org is required for github", ErrBadArgs)
	}
	gho.Org = org
	return gho, nil
}

// promptList reads a whitespace-separated list, returning nil when left blank.
func promptList(ui *prompt.IO, label string) ([]string, error) {
	line, err := ui.Line(label)
	if err != nil {
		return nil, err
	}
	return strings.Fields(line), nil
}

// runForget removes a source's stored credentials from the OS keychain, so a
// rotated or revoked token does not outlive its welcome. Environment variables
// are out of reach here, so any still set are named for the user to unset.
func runForget(cmd *cobra.Command, spec sourceSpec) error {
	ui := prompt.New(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	if len(spec.creds) == 0 {
		ui.Detail("%s uses no credentials; nothing to forget.", spec.title)
		return nil
	}
	removed := 0
	for _, cf := range spec.creds {
		if err := secret.Delete(cf.env); err == nil {
			removed++
		}
		if os.Getenv(cf.env) != "" {
			ui.Warn("%s is still set in your environment; unset it yourself.", cf.env)
		}
	}
	if removed == 0 {
		ui.Detail("No stored credentials for %s in the keychain.", spec.title)
		return nil
	}
	ui.Success("Removed %d stored credential(s) for %s from the keychain.", removed, spec.title)
	return nil
}

// connectStatusView is the machine-readable form of `connect --status`.
type connectStatusView struct {
	// Sources reports each source's readiness, in setup order.
	Sources []connectSourceStatus `json:"sources"`
	// Index is the on-disk index path.
	Index string `json:"index"`
	// IndexExists reports whether an index has been built yet.
	IndexExists bool `json:"index_exists"`
}

// connectSourceStatus is one source's readiness.
type connectSourceStatus struct {
	// ID is the source identifier, matching the index --source value.
	ID string `json:"id"`
	// Status is the human note, e.g. "configured (from keychain)".
	Status string `json:"status"`
	// Configured reports whether the source can run as things stand.
	Configured bool `json:"configured"`
}

// runStatus prints which sources are configured, without prompting, so it works
// over a pipe or in a script. Nothing is collected. It honors the global
// output contract: human lines at a terminal or under --human, JSON when piped
// or under --json or --pretty.
func runStatus(cmd *cobra.Command, opts *options) error {
	view := connectStatusView{Index: opts.indexPath(), IndexExists: indexExists(opts)}
	for _, s := range connectSpecs() {
		view.Sources = append(view.Sources, connectSourceStatus{
			ID:         s.id,
			Status:     statusNote(s),
			Configured: len(s.creds) == 0 || credsSet(s),
		})
	}
	return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, _ style) {
		ui := prompt.New(cmd.InOrStdin(), w, cmd.ErrOrStderr())
		ui.Step("peepwho sources")
		for _, s := range view.Sources {
			ui.Detail("%-11s %s", s.ID, s.Status)
		}
		ui.Blank()
		if view.IndexExists {
			ui.Detail("index: %s", view.Index)
		} else {
			ui.Detail("index: none yet (%s)", view.Index)
		}
	})
}

// statusNote describes a source's readiness: sources with no credentials are
// always ready, credentialed ones report whether their credentials resolve and
// where from, since an environment variable silently outranks the keychain and
// that is the first thing to know when a fresh credential seems ignored.
func statusNote(spec sourceSpec) string {
	if len(spec.creds) == 0 {
		return "ready, no credentials needed"
	}
	if !credsSet(spec) {
		return "not configured"
	}
	sources := make(map[string]bool)
	for _, c := range spec.creds {
		if src := secret.Source(c.env); src != "" {
			sources[src] = true
		}
	}
	switch {
	case sources["env"] && sources["keychain"]:
		return "configured (env and keychain; env wins)"
	case sources["env"]:
		return "configured (from env)"
	case sources["keychain"]:
		return "configured (from keychain)"
	}
	return "configured"
}

// credsSet reports whether a source's credentials resolve, from either the
// keychain or the environment. Confluence also counts as configured when the
// Jira variables are set, because it falls back to them.
func credsSet(spec sourceSpec) bool {
	if allCredsSet(spec.creds) {
		return true
	}
	if spec.id == "confluence" {
		return secret.Resolve(jiraURLEnv) != "" && secret.Resolve(jiraEmailEnv) != "" &&
			secret.Resolve(jiraTokenEnv) != ""
	}
	return false
}

// allCredsSet reports whether every field resolves to a non-empty value.
func allCredsSet(fields []credField) bool {
	for _, cf := range fields {
		if secret.Resolve(cf.env) == "" {
			return false
		}
	}
	return len(fields) > 0
}

// explainAuthError turns a validation error into a specific fix. A 401 or 403,
// or Slack's logical auth error, points at the source's credential guidance; a
// transport error points at the URL and network.
func explainAuthError(spec sourceSpec, err error) string {
	var se *httputil.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return spec.authFix
		case http.StatusNotFound:
			return "Not found (HTTP 404). Check the site URL is the root, with no trailing path."
		default:
			return fmt.Sprintf("The server returned HTTP %d. %s", se.Code, spec.authFix)
		}
	}
	if errors.Is(err, slack.ErrAPI) {
		return spec.authFix
	}
	return fmt.Sprintf("Could not reach the service (%v). Check the URL and your network, then try again.", err)
}

// printPrivacy states, up front, where credentials go and that the index stays
// local, so a work user can wire in peepwho and keep people data private. It
// must match what the command does: it offers the OS keychain later, so
// promising "never written to disk" here would be a lie the next prompt
// exposes.
func printPrivacy(ui *prompt.IO, opts *options) {
	ui.Hint("Privacy: credentials you enter are held in memory and never logged or")
	ui.Hint("written to any peepwho file. You will be offered your OS keychain to keep")
	ui.Hint("them; decline it and nothing is stored. Your index stays local at")
	ui.Hint("%s, readable only by you, and is never uploaded.", opts.indexPath())
}

// mergeSuffix returns " --merge" when an index already exists, matching the
// flag a later manual index run would need to add onto it.
func mergeSuffix(opts *options) string {
	if indexExists(opts) {
		return " --merge"
	}
	return ""
}

// indexExists reports whether an on-disk index is already present.
func indexExists(opts *options) bool {
	_, err := os.Stat(opts.indexPath())
	return err == nil
}

// specByID returns the source with the given id.
func specByID(id string) (sourceSpec, bool) {
	for _, s := range connectSpecs() {
		if s.id == id {
			return s, true
		}
	}
	return sourceSpec{}, false
}

// specIDs returns every source id, for the unknown-source error message.
func specIDs() []string {
	specs := connectSpecs()
	ids := make([]string, len(specs))
	for i, s := range specs {
		ids[i] = s.id
	}
	return ids
}

// quietAbort maps a user backing out, by menu quit or end of input, to a clean
// exit rather than an error.
func quietAbort(err error) error {
	// Only a prompt-level abort or end-of-input is a clean exit. A network
	// error that happens to unwrap to io.EOF, such as a server closing a
	// connection mid-fetch, must not be swallowed as if the user backed out,
	// which would report a failed first index as success.
	if errors.Is(err, prompt.ErrAborted) || errors.Is(err, prompt.ErrInputClosed) {
		return nil
	}
	return err
}
