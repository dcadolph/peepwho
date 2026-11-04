// The source registry: every name the index command accepts, the flags each
// one requires, and how to build its connector from plain options.

package engine

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/graph"
)

// Environment variables that hold each source's credentials. The caller
// resolves the ones a source names in SourceSpec.Creds and passes the values
// in SourceOptions.Creds, so building a source never reads the environment.
const (
	SlackTokenEnv      = "PEEPWHO_SLACK_TOKEN"
	GitHubTokenEnv     = "PEEPWHO_GITHUB_TOKEN"
	PagerDutyTokenEnv  = "PEEPWHO_PAGERDUTY_TOKEN"
	GraphTokenEnv      = "PEEPWHO_GRAPH_TOKEN"
	GraphURLEnv        = "PEEPWHO_GRAPH_URL"
	JiraURLEnv         = "PEEPWHO_JIRA_URL"
	JiraEmailEnv       = "PEEPWHO_JIRA_EMAIL"
	JiraTokenEnv       = "PEEPWHO_JIRA_TOKEN"
	ConfluenceURLEnv   = "PEEPWHO_CONFLUENCE_URL"
	ConfluenceEmailEnv = "PEEPWHO_CONFLUENCE_EMAIL"
	ConfluenceTokenEnv = "PEEPWHO_CONFLUENCE_TOKEN"
)

// SourceOptions holds everything a source constructor reads. The per-source
// connector options are filled by the caller from its flags; Log and Since are
// copied into whichever of them the source takes.
type SourceOptions struct {
	// File is the input path for a file source, or the JSON file ("-" or empty
	// reads Stdin).
	File string
	// Stdin is the reader the json source uses when File is empty or "-".
	Stdin io.Reader
	// Log receives progress and warnings from the connector.
	Log io.Writer
	// Since limits an incremental read; the zero value reads in full.
	Since time.Time
	// Creds holds the resolved value of each variable the source names in
	// SourceSpec.Creds, keyed by variable name. A missing key reads as empty.
	Creds map[string]string
	// AllowPrivateChannels is the policy answer for private Slack channels.
	AllowPrivateChannels bool
	// Slack configures the slack source.
	Slack connector.SlackOptions
	// SlackExport configures the slack-export source.
	SlackExport connector.SlackExportOptions
	// GitHub configures the github source.
	GitHub connector.GitHubOptions
	// JiraURL is the Jira site from a flag; empty falls back to the credential.
	JiraURL string
	// Jira configures the jira source.
	Jira connector.JiraOptions
	// ConfluenceURL is the Confluence site from a flag; empty falls back to the
	// Confluence and then the Jira credential.
	ConfluenceURL string
	// Confluence configures the confluence source.
	Confluence connector.ConfluenceOptions
	// PagerDuty configures the pagerduty source.
	PagerDuty connector.PagerDutyOptions
	// Git configures the git source.
	Git connector.GitOptions
}

// SourceSpec describes one source the index command reads.
type SourceSpec struct {
	// Name is the --source value.
	Name string
	// Incremental reports whether a merge can fold in only what changed since
	// the last run.
	Incremental bool
	// FileKeyed reports whether a refresh keys this source's runs by input file.
	FileKeyed bool
	// Stdin reports whether the source reads stdin when given no file, which a
	// scheduled refresh could not supply.
	Stdin bool
	// Connectable reports whether the connect wizard offers this source.
	Connectable bool
	// Creds names the environment variables the caller resolves for it.
	Creds []string
	// AuthName names the service in a rejected-credentials message; empty
	// leaves fetch errors as they are.
	AuthName string
	// AuthEnv is the variable a rejected-credentials message tells the user to
	// check.
	AuthEnv string
	// New validates the options this source needs and builds its connector.
	New func(o SourceOptions) (connector.Source, error)
}

// sourceSpecs is the registry, in the order the unknown-source error lists it.
//
//nolint:gochecknoglobals // Read-only registry.
var sourceSpecs = []SourceSpec{
	{Name: "org-csv", FileKeyed: true, Connectable: true, New: newOrgCSV},
	{Name: "matters", FileKeyed: true, Connectable: true, New: newMatters},
	{
		Name: "slack", Incremental: true, Connectable: true, Creds: []string{SlackTokenEnv},
		New: newSlack,
	},
	{Name: "slack-export", FileKeyed: true, New: newSlackExport},
	{Name: "codeowners", FileKeyed: true, Connectable: true, New: newCodeOwners},
	{
		Name: "github", Incremental: true, Connectable: true, Creds: []string{GitHubTokenEnv},
		AuthName: "GitHub", AuthEnv: GitHubTokenEnv, New: newGitHub,
	},
	{
		Name: "jira", Incremental: true, Connectable: true,
		Creds:    []string{JiraURLEnv, JiraEmailEnv, JiraTokenEnv},
		AuthName: "Jira", AuthEnv: JiraTokenEnv, New: newJira,
	},
	{
		Name: "confluence", Incremental: true, Connectable: true,
		Creds: []string{
			ConfluenceURLEnv, ConfluenceEmailEnv, ConfluenceTokenEnv,
			JiraURLEnv, JiraEmailEnv, JiraTokenEnv,
		},
		AuthName: "Confluence", AuthEnv: ConfluenceTokenEnv, New: newConfluence,
	},
	{
		Name: "pagerduty", Connectable: true, Creds: []string{PagerDutyTokenEnv},
		AuthName: "PagerDuty", AuthEnv: PagerDutyTokenEnv, New: newPagerDuty,
	},
	{Name: "git", Incremental: true, Connectable: true, New: newGit},
	{Name: "json", FileKeyed: true, Stdin: true, New: newJSON},
	{
		Name: "graph", Connectable: true, Creds: []string{GraphTokenEnv, GraphURLEnv},
		AuthName: "graph", AuthEnv: GraphTokenEnv, New: newGraphSource,
	},
}

// Sources returns every registered source in registry order.
func Sources() []SourceSpec {
	return append([]SourceSpec(nil), sourceSpecs...)
}

// SourceByName returns the source registered under name.
func SourceByName(name string) (SourceSpec, bool) {
	for _, s := range sourceSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return SourceSpec{}, false
}

// SourceNames lists the registered names as an English list ending in "or",
// for the unknown-source error.
func SourceNames() string {
	names := make([]string, len(sourceSpecs))
	for i, s := range sourceSpecs {
		names[i] = s.Name
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

// newOrgCSV builds the org chart CSV source.
func newOrgCSV(o SourceOptions) (connector.Source, error) {
	if o.File == "" {
		return nil, fmt.Errorf("%w: --file is required for org-csv", ErrBadArgs)
	}
	oc := connector.NewOrgCSV(o.File)
	oc.Log = o.Log
	return oc, nil
}

// newMatters builds the billing export source.
func newMatters(o SourceOptions) (connector.Source, error) {
	if o.File == "" {
		return nil, fmt.Errorf(
			"%w: --file (a time-entry or matter CSV export) is required for matters", ErrBadArgs)
	}
	ms := connector.NewMatters(o.File)
	ms.Log = o.Log
	return ms, nil
}

// newCodeOwners builds the CODEOWNERS source.
func newCodeOwners(o SourceOptions) (connector.Source, error) {
	if o.File == "" {
		return nil, fmt.Errorf(
			"%w: --file (CODEOWNERS path or repo root) required for codeowners", ErrBadArgs)
	}
	return connector.NewCodeOwners(o.File), nil
}

// newSlack builds the live Slack source, enforcing the private-channel policy.
func newSlack(o SourceOptions) (connector.Source, error) {
	token := o.Creds[SlackTokenEnv]
	if token == "" {
		return nil, fmt.Errorf("%w: set %s", ErrBadArgs, SlackTokenEnv)
	}
	if o.Slack.IncludePrivate && !o.AllowPrivateChannels {
		return nil, fmt.Errorf("%w: private-channel ingest is disabled by policy", ErrBadArgs)
	}
	so := o.Slack
	so.Since, so.Log = o.Since, o.Log
	return connector.NewSlack(token, so), nil
}

// newSlackExport builds the Slack export source.
func newSlackExport(o SourceOptions) (connector.Source, error) {
	if o.File == "" {
		return nil, fmt.Errorf(
			"%w: --file (the export zip or its unzipped folder) is required for slack-export",
			ErrBadArgs)
	}
	so := o.SlackExport
	so.Since, so.Log = o.Since, o.Log
	return connector.NewSlackExport(o.File, so), nil
}

// newGitHub builds the GitHub source from the configured repositories or org.
func newGitHub(o SourceOptions) (connector.Source, error) {
	token := o.Creds[GitHubTokenEnv]
	if token == "" {
		return nil, fmt.Errorf("%w: set %s", ErrBadArgs, GitHubTokenEnv)
	}
	if len(o.GitHub.Repos) == 0 && o.GitHub.Org == "" {
		return nil, fmt.Errorf("%w: --repo or --github-org required for github", ErrBadArgs)
	}
	gho := o.GitHub
	gho.Since, gho.Log = o.Since, o.Log
	return connector.NewGitHub(token, gho), nil
}

// newJira builds the Jira source. Cloud needs a site, an email, and a token.
// Server and Data Center need only the site: the token is a bearer, optional
// for a public tracker that allows anonymous read.
func newJira(o SourceOptions) (connector.Source, error) {
	site := cmp.Or(o.JiraURL, o.Creds[JiraURLEnv])
	email, token := o.Creds[JiraEmailEnv], o.Creds[JiraTokenEnv]
	if o.Jira.Server {
		if site == "" {
			return nil, fmt.Errorf("%w: set --jira-url (or %s) for the Server site",
				ErrBadArgs, JiraURLEnv)
		}
	} else if site == "" || email == "" || token == "" {
		return nil, fmt.Errorf(
			"%w: set --jira-url (or %s), %s, and %s (or pass --jira-server for a self-hosted site)",
			ErrBadArgs, JiraURLEnv, JiraEmailEnv, JiraTokenEnv)
	}
	jo := o.Jira
	jo.Since, jo.Log = o.Since, o.Log
	return connector.NewJira(site, email, token, jo), nil
}

// newConfluence builds the Confluence source. The site and credentials fall
// back to the Jira ones, since both use the same Atlassian site. Cloud needs
// the site, an email, and a token; Server and Data Center need only the site,
// the token being an optional bearer for a public wiki.
func newConfluence(o SourceOptions) (connector.Source, error) {
	site := cmp.Or(o.ConfluenceURL, o.Creds[ConfluenceURLEnv], o.Creds[JiraURLEnv])
	email := cmp.Or(o.Creds[ConfluenceEmailEnv], o.Creds[JiraEmailEnv])
	token := cmp.Or(o.Creds[ConfluenceTokenEnv], o.Creds[JiraTokenEnv])
	if o.Confluence.Server {
		if site == "" {
			return nil, fmt.Errorf(
				"%w: set PEEPWHO_CONFLUENCE_URL (or PEEPWHO_JIRA_URL) for the Server site",
				ErrBadArgs)
		}
	} else if site == "" || email == "" || token == "" {
		return nil, fmt.Errorf(
			"%w: set PEEPWHO_CONFLUENCE_URL, EMAIL, and TOKEN (or the Jira ones, "+
				"or pass --confluence-server for a self-hosted site)", ErrBadArgs)
	}
	co := o.Confluence
	co.Since, co.Log = o.Since, o.Log
	return connector.NewConfluence(site, email, token, co), nil
}

// newPagerDuty builds the PagerDuty source from services and on-call data.
func newPagerDuty(o SourceOptions) (connector.Source, error) {
	token := o.Creds[PagerDutyTokenEnv]
	if token == "" {
		return nil, fmt.Errorf("%w: set %s", ErrBadArgs, PagerDutyTokenEnv)
	}
	po := o.PagerDuty
	po.Log = o.Log
	return connector.NewPagerDuty(token, po), nil
}

// newGit builds the local git history source.
func newGit(o SourceOptions) (connector.Source, error) {
	if len(o.Git.Paths) == 0 {
		return nil, fmt.Errorf("%w: --repo-path is required for git", ErrBadArgs)
	}
	gito := o.Git
	gito.Log = o.Log
	return connector.NewGitHistory(gito), nil
}

// newJSON builds the JSON import source over Stdin or the named file.
func newJSON(o SourceOptions) (connector.Source, error) {
	if o.File == "" || o.File == "-" {
		return connector.NewJSON(o.Stdin, "json"), nil
	}
	return jsonFile(o.File), nil
}

// jsonFile is a JSON import source that opens its file for the read and
// closes it after.
type jsonFile string

// Fetch opens the file and reads its records.
func (p jsonFile) Fetch(ctx context.Context) ([]connector.Record, error) {
	f, err := os.Open(string(p))
	if err != nil {
		return nil, fmt.Errorf("json source: open %s: %w", string(p), err)
	}
	defer func() { _ = f.Close() }()
	return connector.NewJSON(f, "json").Fetch(ctx)
}

// newGraphSource builds the Microsoft Graph org chart source. The token is
// required; the base URL is optional and points at a sovereign cloud when set.
func newGraphSource(o SourceOptions) (connector.Source, error) {
	token := o.Creds[GraphTokenEnv]
	if token == "" {
		return nil, fmt.Errorf("%w: set %s for the graph source", ErrBadArgs, GraphTokenEnv)
	}
	client := graph.New(token, graph.WithBaseURL(o.Creds[GraphURLEnv]))
	return connector.NewGraph(client), nil
}
