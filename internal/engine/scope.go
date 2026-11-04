package engine

import (
	"sort"
	"strings"
)

// ScopeInputs carries the query-scope flags that key a source's watermark.
type ScopeInputs struct {
	// JiraJQL and JiraProjects scope a Jira read.
	JiraJQL      string
	JiraProjects []string
	// ConfluenceCQL and ConfluenceSpaces scope a Confluence read.
	ConfluenceCQL    string
	ConfluenceSpaces []string
	// GitHubRepos and GitHubOrg scope a GitHub read.
	GitHubRepos []string
	GitHubOrg   string
	// SlackPrivate reports whether private channels are in scope.
	SlackPrivate bool
}

// IndexScope returns the watermark key for a source's current query, so changing
// the scope, such as the set of projects, spaces, or repositories indexed,
// starts a fresh watermark rather than reusing one taken over different items.
func IndexScope(source string, in ScopeInputs) string {
	switch source {
	case "jira":
		if strings.TrimSpace(in.JiraJQL) != "" {
			return "jql:" + in.JiraJQL
		}
		if len(in.JiraProjects) > 0 {
			return "project:" + sortedJoin(in.JiraProjects)
		}
		return "all"
	case "confluence":
		if strings.TrimSpace(in.ConfluenceCQL) != "" {
			return "cql:" + in.ConfluenceCQL
		}
		if len(in.ConfluenceSpaces) > 0 {
			return "space:" + sortedJoin(in.ConfluenceSpaces)
		}
		return "all"
	case "github":
		if in.GitHubOrg != "" {
			return "org:" + in.GitHubOrg
		}
		if len(in.GitHubRepos) > 0 {
			return "repos:" + sortedJoin(in.GitHubRepos)
		}
		return "all"
	case "slack":
		if in.SlackPrivate {
			return "all"
		}
		return "public"
	default:
		return ""
	}
}

// UsesExplicitQuery reports whether a source is driven by a raw query the
// connector treats as authoritative. Such a query ignores the watermark and
// returns its full result, so the run must replace rather than fold, and no
// watermark is kept for it, keeping the raw query out of the plain state file.
func UsesExplicitQuery(source, jiraJQL, confluenceCQL string) bool {
	switch source {
	case "jira":
		return strings.TrimSpace(jiraJQL) != ""
	case "confluence":
		return strings.TrimSpace(confluenceCQL) != ""
	default:
		return false
	}
}

// sortedJoin returns the values sorted and comma-joined, so a scope key does not
// depend on the order the flags were given.
func sortedJoin(values []string) string {
	v := append([]string(nil), values...)
	sort.Strings(v)
	return strings.Join(v, ",")
}
