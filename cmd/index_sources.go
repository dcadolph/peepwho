// Fetching per source: each connector's flags turned into records and
// episodes, with errors explained in the source's own vocabulary.

package cmd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/graph"
	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/secret"
	"github.com/dcadolph/peepwho/internal/slack"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/util"
	"github.com/spf13/cobra"
)

// fetchSlack builds Slack records, enforcing the private-channel policy guard.
// explainSourceError turns a low-level fetch failure into an actionable message
// naming the source and the credential to check. A token that expired or lost
// a scope otherwise surfaces as a bare status code next to an internal API
// path, which tells a user nothing about what to do. Other errors pass through.
func explainSourceError(source, tokenEnv string, err error) error {
	var se *httputil.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf(
			"%w: %s rejected the credentials (HTTP %d). The token in %s is missing, expired, or "+
				"lacks the read access this needs. Recreate it and set %s, then try again",
			ErrAuth, source, se.Code, tokenEnv, tokenEnv)
	case http.StatusNotFound:
		return fmt.Errorf(
			"%w: %s returned not found (HTTP 404). Check the site URL is the root with no path, and "+
				"that the names you scoped to exist", ErrAuth, source)
	}
	return err
}

func fetchSlack(
	cmd *cobra.Command, opts *options, a slackArgs,
) ([]connector.Record, []episode.Episode, error) {
	token := secret.Resolve(slackTokenEnv)
	if token == "" {
		return nil, nil, fmt.Errorf("%w: set %s", ErrBadArgs, slackTokenEnv)
	}
	if a.includePrivate && !opts.pol.AllowPrivateChannels() {
		return nil, nil, fmt.Errorf("%w: private-channel ingest is disabled by policy", ErrBadArgs)
	}
	src := connector.NewSlack(token, connector.SlackOptions{
		IncludePrivate:        a.includePrivate,
		JoinPublic:            a.joinPublic,
		SinceDays:             a.sinceDays,
		MaxMessages:           a.maxMessages,
		Episodes:              a.episodes,
		MaxEpisodesPerChannel: a.maxEpisodes,
		Archive:               a.archive,
		MaxArchiveMessages:    a.maxArchive,
		Since:                 a.since,
		Log:                   cmd.ErrOrStderr(),
	})
	recs, err := src.Fetch(cmd.Context())
	if err != nil {
		return nil, nil, explainSlackError(err)
	}
	return recs, src.Episodes(), nil
}

// explainSlackError turns a Slack auth or scope failure into an actionable
// message. Slack signals these as an ok=false code such as invalid_auth rather
// than an HTTP status, so explainSourceError, which reads status codes, never
// catches them; without this a descoped token surfaces as a bare code next to an
// internal method name.
func explainSlackError(err error) error {
	if err == nil || !errors.Is(err, slack.ErrAPI) {
		return err
	}
	code := "error"
	if _, c, ok := strings.Cut(err.Error(), "api error: "); ok {
		code = strings.TrimSpace(c)
	}
	switch {
	case strings.Contains(code, "invalid_auth"), strings.Contains(code, "not_authed"),
		strings.Contains(code, "token_revoked"), strings.Contains(code, "account_inactive"):
		return fmt.Errorf(
			"%w: Slack rejected the token in %s (%s). It is missing, expired, or revoked. Recreate "+
				"the bot token and set %s, then try again", ErrAuth, slackTokenEnv, code, slackTokenEnv)
	case strings.Contains(code, "missing_scope"), strings.Contains(code, "not_allowed_token_type"):
		return fmt.Errorf(
			"%w: the Slack token in %s is missing a required scope (%s). Add users:read, "+
				"users:read.email, channels:read, and channels:history in your Slack app and reinstall",
			ErrAuth, slackTokenEnv, code)
	}
	return err
}

// githubArgs holds the GitHub-specific index flags.
type githubArgs struct {
	// repos is the list of owner/name repositories.
	repos []string
	// org adds every repository in the org.
	org string
	// maxRepos caps repositories taken from the org.
	maxRepos int
	// emails resolves user emails to join other sources.
	emails bool
	// episodes records merged changes.
	episodes bool
	// since limits an incremental read to pull requests and issues updated at or
	// after it; the zero value reads in full.
	since time.Time
}

// fetchGitHub builds GitHub records from the configured repositories or org.
func fetchGitHub(cmd *cobra.Command, a githubArgs) ([]connector.Record, []episode.Episode, error) {
	token := secret.Resolve(githubTokenEnv)
	if token == "" {
		return nil, nil, fmt.Errorf("%w: set %s", ErrBadArgs, githubTokenEnv)
	}
	if len(a.repos) == 0 && a.org == "" {
		return nil, nil, fmt.Errorf("%w: --repo or --github-org required for github", ErrBadArgs)
	}
	src := connector.NewGitHub(token, connector.GitHubOptions{
		Repos: a.repos, Org: a.org, MaxRepos: a.maxRepos, ResolveEmails: a.emails,
		Episodes: a.episodes, Since: a.since, Log: cmd.ErrOrStderr(),
	})
	recs, err := src.Fetch(cmd.Context())
	if err != nil {
		return nil, nil, explainSourceError("GitHub", githubTokenEnv, err)
	}
	return recs, src.Episodes(), nil
}

// jiraArgs holds the Jira-specific index flags.
type jiraArgs struct {
	// url is the Jira site URL.
	url string
	// projects scopes the search to these project keys.
	projects []string
	// jql overrides the query.
	jql string
	// maxIssues caps issues read.
	maxIssues int
	// episodes records resolved issues.
	episodes bool
	// server selects a self-hosted Server or Data Center deployment.
	server bool
	// since limits an incremental read to issues updated at or after it; the
	// zero value reads in full.
	since time.Time
}

// fetchJira builds Jira records, reading the URL and credentials from flags and
// the environment.
func fetchJira(cmd *cobra.Command, a jiraArgs) ([]connector.Record, []episode.Episode, error) {
	site := a.url
	if site == "" {
		site = secret.Resolve(jiraURLEnv)
	}
	email := secret.Resolve(jiraEmailEnv)
	token := secret.Resolve(jiraTokenEnv)
	// Cloud needs a site, an email, and a token. Server and Data Center need
	// only the site: the token is a bearer, optional for a public tracker that
	// allows anonymous read.
	if a.server {
		if site == "" {
			return nil, nil, fmt.Errorf("%w: set --jira-url (or %s) for the Server site",
				ErrBadArgs, jiraURLEnv)
		}
	} else if site == "" || email == "" || token == "" {
		return nil, nil, fmt.Errorf(
			"%w: set --jira-url (or %s), %s, and %s (or pass --jira-server for a self-hosted site)",
			ErrBadArgs, jiraURLEnv, jiraEmailEnv, jiraTokenEnv)
	}
	src := connector.NewJira(site, email, token, connector.JiraOptions{
		Projects: a.projects, JQL: a.jql, MaxIssues: a.maxIssues,
		Episodes: a.episodes, Server: a.server, Since: a.since, Log: cmd.ErrOrStderr(),
	})
	recs, err := src.Fetch(cmd.Context())
	if err != nil {
		return nil, nil, explainSourceError("Jira", jiraTokenEnv, err)
	}
	return recs, src.Episodes(), nil
}

// confluenceArgs holds the Confluence-specific index flags.
type confluenceArgs struct {
	// url is the Confluence site URL; empty falls back to the environment.
	url string
	// spaces scopes the search to these space keys.
	spaces []string
	// cql overrides the query.
	cql string
	// maxPages caps pages read.
	maxPages int
	// server selects a self-hosted Server or Data Center deployment.
	server bool
	// since limits an incremental read to pages modified at or after it; the
	// zero value reads in full.
	since time.Time
}

// fetchConfluence builds Confluence records. The URL and credentials fall back
// to the Jira environment variables, since both use the same Atlassian site.
func fetchConfluence(cmd *cobra.Command, a confluenceArgs) ([]connector.Record, error) {
	site := cmp.Or(a.url, secret.Resolve(confluenceURLEnv), secret.Resolve(jiraURLEnv))
	email := cmp.Or(secret.Resolve(confluenceEmailEnv), secret.Resolve(jiraEmailEnv))
	token := cmp.Or(secret.Resolve(confluenceTokenEnv), secret.Resolve(jiraTokenEnv))
	// Cloud needs the site, an email, and a token; Server and Data Center need
	// only the site, the token being an optional bearer for a public wiki.
	if a.server {
		if site == "" {
			return nil, fmt.Errorf("%w: set PEEPWHO_CONFLUENCE_URL (or PEEPWHO_JIRA_URL) for the Server site", ErrBadArgs)
		}
	} else if site == "" || email == "" || token == "" {
		return nil, fmt.Errorf(
			"%w: set PEEPWHO_CONFLUENCE_URL, EMAIL, and TOKEN (or the Jira ones, "+
				"or pass --confluence-server for a self-hosted site)", ErrBadArgs)
	}
	src := connector.NewConfluence(site, email, token, connector.ConfluenceOptions{
		Spaces: a.spaces, CQL: a.cql, MaxPages: a.maxPages, Server: a.server, Since: a.since,
		Log: cmd.ErrOrStderr(),
	})
	recs, err := src.Fetch(cmd.Context())
	if err != nil {
		return nil, explainSourceError("Confluence", confluenceTokenEnv, err)
	}
	return recs, nil
}

// fetchGraph reads the org chart from Microsoft Graph. The token is required;
// the base URL is optional and points at a sovereign cloud when set.
func fetchGraph(cmd *cobra.Command) ([]connector.Record, error) {
	token := secret.Resolve(graphTokenEnv)
	if token == "" {
		return nil, fmt.Errorf("%w: set %s for the graph source", ErrBadArgs, graphTokenEnv)
	}
	client := graph.New(token, graph.WithBaseURL(secret.Resolve(graphURLEnv)))
	recs, err := connector.NewGraph(client).Fetch(cmd.Context())
	if err != nil {
		return nil, explainSourceError("graph", graphTokenEnv, err)
	}
	return recs, nil
}

// fetchPagerDuty builds PagerDuty records from services and on-call data.
func fetchPagerDuty(cmd *cobra.Command, episodes bool) ([]connector.Record, []episode.Episode, error) {
	token := secret.Resolve(pagerdutyTokenEnv)
	if token == "" {
		return nil, nil, fmt.Errorf("%w: set %s", ErrBadArgs, pagerdutyTokenEnv)
	}
	src := connector.NewPagerDuty(token, connector.PagerDutyOptions{
		Episodes: episodes, Log: cmd.ErrOrStderr(),
	})
	recs, err := src.Fetch(cmd.Context())
	if err != nil {
		return nil, nil, explainSourceError("PagerDuty", pagerdutyTokenEnv, err)
	}
	return recs, src.Episodes(), nil
}

// reportChanges prints a one-line summary and capped lists of who and what
// joined or left since the last index.
func reportChanges(w io.Writer, c index.Changes) {
	if c.Empty() {
		return
	}
	fmt.Fprintf(w, "changes since last index: %s\n", c.Summary())
	printChangeList(w, "joined", c.PeopleJoined)
	printChangeList(w, "left", c.PeopleLeft)
	printChangeList(w, "new channels", c.ChannelsAdded)
	printChangeList(w, "gone channels", c.ChannelsRemoved)
}

// printChangeList prints up to a fixed number of items under a label, noting
// any remainder.
func printChangeList(w io.Writer, label string, items []string) {
	const limit = 15
	if len(items) == 0 {
		return
	}
	shown := items
	if len(shown) > limit {
		shown = shown[:limit]
	}
	fmt.Fprintf(w, "  %s: %s", label, strings.Join(shown, ", "))
	if len(items) > limit {
		fmt.Fprintf(w, ", and %d more", len(items)-limit)
	}
	fmt.Fprintln(w)
}

// writeChangesFile writes the changes as indented JSON to path atomically.
func writeChangesFile(path string, c index.Changes) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("changes file: %w", err)
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("changes file: %w", err)
	}
	return nil
}

// gitMarks returns where a previous run stopped in each repository, so reading
// resumes there. It is empty unless this is a merge, since a full read is meant
// to start over, and empty on the first run, when there is nothing to resume
// from.
func gitMarks(opts *options, merge bool, paths []string) map[string]string {
	if !merge || !indexExists(opts) {
		return nil
	}
	st, err := opts.loadState()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if wm, ok := st.Get("git", "repo:"+p); ok && wm.Mark != "" {
			out[p] = wm.Mark
		}
	}
	return out
}

// saveGitMarks records where reading stopped in each repository. Each is its own
// watermark, since one run may cover several repositories and they advance
// independently.
func saveGitMarks(opts *options, marks map[string]string) error {
	if len(marks) == 0 {
		return nil
	}
	unlock, err := util.LockFile(opts.statePath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := opts.loadState()
	if err != nil {
		return err
	}
	for path, mark := range marks {
		st.Set(state.Watermark{
			Source: "git", Scope: "repo:" + path, Mark: mark,
			Complete: true, RanAt: opts.now(),
		})
	}
	return opts.saveState(st)
}
