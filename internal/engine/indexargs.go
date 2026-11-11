package engine

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/pflag"
)

// IndexArgs holds the values of every index flag except --source, so the
// command line and a refresh replaying recorded flags parse them the same way.
type IndexArgs struct {
	// File is the input path for a file source.
	File string
	// IncludePrivate requests private Slack channels.
	IncludePrivate bool
	// SlackJoin has the bot self-join public channels.
	SlackJoin bool
	// SinceDays is the Slack history window in days.
	SinceDays int
	// MaxMessages caps Slack messages per channel.
	MaxMessages int
	// Episodes records past conversations for recall.
	Episodes bool
	// MaxEpisodes caps episodes per channel.
	MaxEpisodes int
	// Archive keeps the words of each Slack conversation.
	Archive bool
	// MaxArchive caps retained messages per conversation.
	MaxArchive int
	// ChangesFile receives the index diff as JSON.
	ChangesFile string
	// Embed generates embeddings via Ollama.
	Embed bool
	// EmbedModel names the Ollama embedding model.
	EmbedModel string
	// OllamaURL is the Ollama base URL.
	OllamaURL string
	// Repos are the GitHub owner/name repositories.
	Repos []string
	// GitHubOrg is the GitHub org to index.
	GitHubOrg string
	// MaxRepos caps repositories taken from the org.
	MaxRepos int
	// GitHubEmails resolves GitHub user emails.
	GitHubEmails bool
	// JiraURL is the Jira site URL.
	JiraURL string
	// JiraProjects are the Jira project keys.
	JiraProjects []string
	// JiraServer selects self-hosted Jira.
	JiraServer bool
	// JiraJQL is an explicit Jira query.
	JiraJQL string
	// MaxIssues caps Jira issues read.
	MaxIssues int
	// Merge adds onto the existing index.
	Merge bool
	// Full re-reads everything on a merge.
	Full bool
	// AllowShrink accepts a much smaller read.
	AllowShrink bool
	// AliasesFile is the identity alias JSON file.
	AliasesFile string
	// HalfLifeDays is the recency half-life.
	HalfLifeDays int
	// RepoPaths are the local repositories for git.
	RepoPaths []string
	// GitSinceDays is the git history window in days.
	GitSinceDays int
	// GitUntilDays stops the git window this many days before today.
	GitUntilDays int
	// GitWorkers is how many commits to diff at once.
	GitWorkers int
	// MaxCommits caps commits per repository.
	MaxCommits int
	// ConfluenceURL is the Confluence site URL.
	ConfluenceURL string
	// ConfluenceSpaces are the Confluence space keys.
	ConfluenceSpaces []string
	// ConfluenceServer selects self-hosted Confluence.
	ConfluenceServer bool
	// ConfluenceCQL is an explicit Confluence query.
	ConfluenceCQL string
	// MaxPages caps Confluence pages read.
	MaxPages int
}

// RegisterIndexFlags defines every index flag except --source on fs, bound to a.
func RegisterIndexFlags(fs *pflag.FlagSet, a *IndexArgs) {
	fs.StringVar(&a.File, "file", "", "Path to the source file: the CSV for org-csv, the CODEOWNERS file or repo root for codeowners, the JSON array for json (- for stdin).")
	fs.BoolVar(&a.IncludePrivate, "include-private", false, "Ingest private Slack channels if policy allows.")
	fs.BoolVar(&a.SlackJoin, "slack-join", false,
		"Have the bot self-join public channels it is not in, so a workspace indexes without manual invites (needs channels:join; posts a join notice per channel).")
	fs.IntVar(&a.SinceDays, "since-days", 180, "Slack history window in days.")
	fs.IntVar(&a.MaxMessages, "max-messages", 5000, "Slack message cap per channel.")
	fs.BoolVar(&a.Episodes, "episodes", false,
		"Record past conversations so whotoask recall can point back at them.")
	fs.IntVar(&a.MaxEpisodes, "max-episodes-per-channel", 200, "Episode cap per channel.")
	fs.BoolVar(&a.Archive, "archive", false,
		"Slack only: keep the words of each conversation, not just a link. Needs an index key, implies --episodes.")
	fs.IntVar(&a.MaxArchive, "max-archive-messages", 50, "Retained message cap per conversation.")
	fs.StringVar(&a.ChangesFile, "changes-file", "", "Write the index diff as JSON to this path.")
	fs.BoolVar(&a.Merge, "merge", false, "Merge into the existing index instead of replacing it.")
	fs.BoolVar(&a.Full, "full", false,
		"With --merge, re-read every item and recompact instead of only what changed since the last run.")
	fs.BoolVar(&a.AllowShrink, "allow-shrink", false,
		"Accept a source returning far less than last time, which is otherwise refused as a truncated read.")
	fs.StringVar(&a.AliasesFile, "aliases", "",
		"JSON file mapping a canonical id to its aliases, joining one person across sources.")
	fs.IntVar(&a.HalfLifeDays, "half-life-days", 180,
		"Days for a dated record's weight to halve; 0 disables recency decay.")
	fs.StringArrayVar(&a.RepoPaths, "repo-path", nil, "Local repository root for the git source (repeatable).")
	fs.IntVar(&a.GitSinceDays, "git-since-days", 365, "Git history window in days.")
	fs.IntVar(&a.MaxCommits, "max-commits", 2000, "Commit cap per repository for the git source.")
	fs.IntVar(&a.GitUntilDays, "git-until-days", 0,
		"Stop the git window this many days before today; 0 reads up to the present.")
	fs.IntVar(&a.GitWorkers, "git-workers", 0,
		"Commits to diff at once for the git source; 0 picks a default from the machine.")
	fs.BoolVar(&a.Embed, "embed", false, "Generate embeddings via Ollama for semantic search.")
	fs.StringVar(&a.EmbedModel, "embed-model", "", "Ollama embed model (default nomic-embed-text).")
	fs.StringVar(&a.OllamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL for --embed.")
	fs.StringArrayVar(&a.Repos, "repo", nil, "GitHub repo owner/name (repeatable).")
	fs.StringVar(&a.GitHubOrg, "github-org", "", "GitHub org to index all repositories of.")
	fs.IntVar(&a.MaxRepos, "max-repos", 0, "Cap repositories taken from --github-org (0 = all).")
	fs.BoolVar(&a.GitHubEmails, "github-emails", false, "Resolve GitHub user emails to join other sources.")
	fs.StringVar(&a.JiraURL, "jira-url", "", "Jira site URL (or WHOTOASK_JIRA_URL).")
	fs.StringArrayVar(&a.JiraProjects, "jira-project", nil, "Jira project key (repeatable).")
	fs.StringVar(&a.JiraJQL, "jira-jql", "", "Jira JQL query (overrides --jira-project).")
	fs.IntVar(&a.MaxIssues, "max-issues", 1000, "Cap Jira issues read.")
	fs.BoolVar(&a.JiraServer, "jira-server", false,
		"Jira is self-hosted Server or Data Center (v2 API, bearer or anonymous auth), not Cloud.")
	fs.StringArrayVar(&a.ConfluenceSpaces, "confluence-space", nil, "Confluence space key (repeatable).")
	fs.StringVar(&a.ConfluenceURL, "confluence-url", "", "Confluence site URL; or WHOTOASK_CONFLUENCE_URL (falls back to the Jira URL).")
	fs.StringVar(&a.ConfluenceCQL, "confluence-cql", "", "Confluence CQL query (overrides --confluence-space).")
	fs.BoolVar(&a.ConfluenceServer, "confluence-server", false,
		"Confluence is self-hosted Server or Data Center (REST at site root, bearer/anonymous auth), not Cloud.")
	fs.IntVar(&a.MaxPages, "max-pages", 2000, "Cap Confluence pages read.")
}

// AbsPathFlags makes each relative path in the path flags of fs (--file,
// --repo-path, --aliases, and --changes-file) absolute against the working
// directory. An empty value, a path that is already absolute, and the "-" that
// means standard input for --file stay as given. The values the flags are bound
// to change with them, so a run, its refresh scope, and the flags a refresh
// replays all name the same place whatever directory the refresh starts in.
func AbsPathFlags(fs *pflag.FlagSet) error {
	for _, name := range []string{"file", "repo-path", "aliases", "changes-file"} {
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if err := absFlag(f); err != nil {
			return fmt.Errorf("resolve --%s: %w", name, err)
		}
	}
	return nil
}

// absFlag makes every relative path f holds absolute. A value that needs no
// change is left alone.
func absFlag(f *pflag.Flag) error {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		paths := sv.GetSlice()
		changed := false
		for i, p := range paths {
			abs, err := absPath(f.Name, p)
			if err != nil {
				return err
			}
			changed = changed || abs != p
			paths[i] = abs
		}
		if !changed {
			return nil
		}
		return sv.Replace(paths)
	}
	cur := f.Value.String()
	abs, err := absPath(f.Name, cur)
	if err != nil || abs == cur {
		return err
	}
	return f.Value.Set(abs)
}

// absPath returns p as an absolute path for the flag called name. An empty
// value, an absolute path, and the stdin marker of --file come back as given.
func absPath(name, p string) (string, error) {
	if p == "" || filepath.IsAbs(p) || (name == "file" && p == "-") {
		return p, nil
	}
	return filepath.Abs(p)
}
