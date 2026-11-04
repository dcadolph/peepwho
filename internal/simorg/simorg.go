// Package simorg simulates a small company across every source peepwho reads,
// serving each tool's wire format from in-process HTTP servers. It exercises
// the full pipeline end to end, at the wire level, without any credentials.
package simorg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/dcadolph/peepwho/internal/confluence"
	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/github"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/jira"
	"github.com/dcadolph/peepwho/internal/pagerduty"
	"github.com/dcadolph/peepwho/internal/slack"
)

// The simulated cast. The Payments team is Angela, Oscar, Kevin, and Pam;
// Bob, Carol, Dan, Eve, Frank, Grace, and Heidi round out the rest of the
// company. Victor owned terraform two years ago and must lose to Carol on
// recency. Eve's GitHub account exposes no email, so only an alias joins her;
// bots must be skipped everywhere.

// OrgCSV returns the org chart in the org-csv source format.
func OrgCSV() string {
	return `name,email,title,team,topics,manager
Oscar Scott,oscar@corp.com,Engineering Manager,Payments,billing;roadmap,
Angela Malone,angela@corp.com,Staff Engineer,Payments,billing;retries,oscar@corp.com
Kevin Novak,kevin@corp.com,Software Engineer,Payments,retries,angela@corp.com
Pam Vance,pam@corp.com,Support Lead,Payments,billing;support,oscar@corp.com
Bob Smith,bob@corp.com,Senior Engineer,Data Platform,kafka;streaming,oscar@corp.com
Carol Lee,carol@corp.com,Site Reliability Engineer,Infrastructure,deploys,oscar@corp.com
Victor Old,victor@corp.com,Systems Engineer,Infrastructure,,carol@corp.com
Grace Kim,grace@corp.com,Site Reliability Engineer,Infrastructure,oncall;incidents,carol@corp.com
Dan Park,dan@corp.com,Security Engineer,Security,oauth;sso,oscar@corp.com
Eve Ng,eve@corp.com,Frontend Engineer,Web,react;frontend,oscar@corp.com
Frank Ito,frank@corp.com,Machine Learning Engineer,ML Platform,embeddings;models,oscar@corp.com
Heidi Cho,heidi@corp.com,Search Engineer,Search,relevance,oscar@corp.com
`
}

// CodeOwners returns a CODEOWNERS file owning terraform paths by email.
func CodeOwners() string {
	return "*.tf carol@corp.com\ninfra/ carol@corp.com\n"
}

// Aliases returns the alias file joining Eve's email-less GitHub login.
func Aliases() string {
	return `{"eve@corp.com": ["github:eve-dev"]}`
}

// SlackServer serves users, channels, and history in Slack's wire format. The
// first history call returns HTTP 429 so the client's retry path runs.
func SlackServer() *httptest.Server {
	users := []map[string]any{
		slackUser("U1", "Angela Malone", "angela@corp.com", "Staff Engineer"),
		slackUser("U2", "Bob Smith", "bob@corp.com", "Senior Engineer"),
		slackUser("U3", "Carol Lee", "carol@corp.com", "Site Reliability Engineer"),
		slackUser("U4", "Dan Park", "dan@corp.com", "Security Engineer"),
		slackUser("U5", "Grace Kim", "grace@corp.com", "Site Reliability Engineer"),
		slackUser("U6", "Oscar Scott", "oscar@corp.com", "Engineering Manager"),
	}
	channels := []map[string]any{
		slackChannel("C1", "payments", "billing and payment questions"),
		slackChannel("C2", "data-platform", "kafka and streaming"),
		slackChannel("C3", "infra", "kubernetes deploys and oncall"),
		slackChannel("C4", "security", "auth login and sso"),
	}
	history := map[string][]map[string]any{
		"C1": {
			slackMessage("U1", "billing retry backoff is fixed, dunning next", daysAgo(2)),
			slackMessage("U1", "payments reconciliation ran clean", daysAgo(1)),
			slackMessage("U6", "billing roadmap review is on for next week", daysAgo(3)),
		},
		"C2": {
			slackMessage("U2", "kafka consumer lag is back to zero", daysAgo(3)),
		},
		"C3": {
			slackMessage("U3", "terraform plan for the new cluster is up", daysAgo(2)),
			slackMessage("U5", "paging policy updated after the incident", daysAgo(4)),
			// A thread: Angela hit a problem, Carol and Grace worked it out with
			// her. This is what recall finds months later.
			slackThread("U1", "the staging certificate renewal keeps failing, anyone seen this",
				daysAgo(90), 4, []string{"U3", "U5"}, daysAgo(90).Add(20*time.Minute)),
		},
		"C4": {
			slackMessage("U4", "sso login flow now enforces mfa", daysAgo(5)),
		},
	}

	// Replies to the certificate thread, which only the archive reads.
	replies := map[string][]map[string]any{
		threadKey(daysAgo(90)): {
			slackMessage("U1", "the staging certificate renewal keeps failing, anyone seen this",
				daysAgo(90)),
			slackMessage("U3", "the dns challenge needs the wildcard record on the staging zone",
				daysAgo(90).Add(5*time.Minute)),
			slackMessage("U5", "add it, then rerun certbot with --force-renewal",
				daysAgo(90).Add(12*time.Minute)),
			slackMessage("U1", "that did it, renewed and staging is green",
				daysAgo(90).Add(20*time.Minute)),
		},
	}

	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "members": users})
	})
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "channels": channels})
	})
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"ok": true, "user_id": "U0", "url": "https://corp.slack.com/", "team": "Corp",
		})
	})
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"ok": true, "has_more": false, "messages": replies[r.Form.Get("ts")],
		})
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		limited := false
		once.Do(func() { limited = true })
		if limited {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"ok": true, "has_more": false, "messages": history[r.Form.Get("channel")],
		})
	})
	return httptest.NewServer(mux)
}

// GitHubServer serves two repositories in GitHub's wire format: Angela's
// billing service and Eve's web app. Angela's profile exposes her email;
// Eve's does not, so she stays a github: identity until an alias joins her.
func GitHubServer() *httptest.Server {
	mux := http.NewServeMux()
	repo := func(name, desc string, topics []string) map[string]any {
		return map[string]any{
			"name": name, "full_name": "corp/" + name, "description": desc, "topics": topics,
		}
	}
	mux.HandleFunc("/repos/corp/billing-service", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, repo("billing-service", "Payment processing and dunning", []string{"billing"}))
	})
	mux.HandleFunc("/repos/corp/webapp", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, repo("webapp", "Customer facing web application", []string{"frontend"}))
	})
	mux.HandleFunc("/repos/corp/billing-service/contributors", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"login": "amalone", "contributions": 40}})
	})
	mux.HandleFunc("/repos/corp/webapp/contributors", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{
			{"login": "eve-dev", "contributions": 30},
			{"login": "buildbot[bot]", "contributions": 900},
		})
	})
	mux.HandleFunc("/repos/corp/billing-service/pulls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{
			"number":   412,
			"html_url": "https://github.com/corp/billing-service/pull/412",
			"title":    "Fix billing retry backoff", "user": map[string]any{"login": "amalone"},
			"labels":     []map[string]any{{"name": "billing"}},
			"updated_at": isoDaysAgo(2), "merged_at": isoDaysAgo(2),
		}, {
			"number":   377,
			"html_url": "https://github.com/corp/billing-service/pull/377",
			"title":    "Add dunning retry ceiling", "user": map[string]any{"login": "amalone"},
			"updated_at": isoDaysAgo(40),
		}})
	})
	mux.HandleFunc("/repos/corp/webapp/pulls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{
			"number": 88, "html_url": "https://github.com/corp/webapp/pull/88",
			"title":               "Rewrite the react frontend in typescript",
			"user":                map[string]any{"login": "eve-dev"},
			"requested_reviewers": []map[string]any{{"login": "bsmith"}},
			"updated_at":          isoDaysAgo(4), "merged_at": isoDaysAgo(4),
		}})
	})
	for _, r := range []string{"billing-service", "webapp"} {
		mux.HandleFunc("/repos/corp/"+r+"/issues", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []map[string]any{})
		})
	}
	mux.HandleFunc("/users/bsmith", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"login": "bsmith", "name": "Bob Smith", "email": "bob@corp.com"})
	})
	mux.HandleFunc("/users/amalone", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"login": "amalone", "name": "Angela Malone", "email": "angela@corp.com"})
	})
	mux.HandleFunc("/users/eve-dev", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"login": "eve-dev", "name": "Eve Ng"})
	})
	mux.HandleFunc("/users/buildbot[bot]", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"login": "buildbot[bot]"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

// JiraServer serves issue search in Jira Cloud's wire format.
func JiraServer() *httptest.Server {
	issue := func(key, summary string, assignee, reporter map[string]any, labels []string, project string, ago int) map[string]any {
		projectKey, _, _ := strings.Cut(key, "-")
		fields := map[string]any{
			"summary": summary, "labels": labels,
			"project": map[string]any{"key": projectKey, "name": project},
			"updated": jiraDaysAgo(ago),
		}
		if assignee != nil {
			fields["assignee"] = assignee
		}
		if reporter != nil {
			fields["reporter"] = reporter
		}
		return map[string]any{"key": key, "fields": fields}
	}
	bob := map[string]any{"accountId": "j-bob", "displayName": "Bob Smith", "emailAddress": "bob@corp.com"}
	dan := map[string]any{"accountId": "j-dan", "displayName": "Dan Park", "emailAddress": "dan@corp.com"}
	frank := map[string]any{"accountId": "j-frank", "displayName": "Frank Ito", "emailAddress": "frank@corp.com"}
	resolve := func(m map[string]any, ago int) map[string]any {
		f, _ := m["fields"].(map[string]any)
		f["resolutiondate"] = jiraDaysAgo(ago)
		f["status"] = map[string]any{
			"name": "Done", "statusCategory": map[string]any{"key": "done"},
		}
		return m
	}
	issues := []map[string]any{
		resolve(issue("DAT-1", "Kafka consumer lag on the stream ingest", bob, nil,
			[]string{"kafka"}, "Data Platform", 3), 3),
		resolve(issue("SEC-1", "Enforce mfa on the sso login flow", dan, nil,
			[]string{"sso"}, "Security", 5), 5),
		issue("DAT-2", "Embedding model serving latency", nil, frank,
			[]string{"embeddings"}, "Data Platform", 8),
	}
	mux := http.NewServeMux()
	// Jira Cloud pages issues through the token-based enhanced-search endpoint.
	// One page holds every generated issue, so the response marks itself last.
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issues": issues, "isLast": true})
	})
	return httptest.NewServer(mux)
}

// ConfluenceServer serves pages in Confluence Cloud's v2 wire format: pages name
// people by account id, and the space name, labels, and identities are read from
// separate endpoints rather than expanded inline.
func ConfluenceServer() *httptest.Server {
	type cfPage struct {
		ID, Title, SpaceID, AuthorID string
		Labels                       []string
		Ago                          int
	}
	pages := []cfPage{
		{"0", "SSO login runbook", "sp-sec", "c-dan", []string{"sso", "oauth"}, 6},
		{"1", "Embeddings model serving guide", "sp-ml", "c-frank", []string{"embeddings"}, 9},
	}
	spaceName := map[string]string{"sp-sec": "Security", "sp-ml": "ML Platform"}
	users := map[string]map[string]any{
		"c-dan":   {"accountId": "c-dan", "displayName": "Dan Park", "email": "dan@corp.com"},
		"c-frank": {"accountId": "c-frank", "displayName": "Frank Ito", "email": "frank@corp.com"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/rest/api/user/current", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"accountId": "c-me", "email": "demo@corp.com"})
	})
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := users[r.URL.Query().Get("accountId")]; ok {
			writeJSON(w, u)
			return
		}
		writeJSON(w, map[string]any{"accountId": r.URL.Query().Get("accountId")})
	})
	mux.HandleFunc("/wiki/api/v2/spaces/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/spaces/")
		writeJSON(w, map[string]any{"id": id, "key": id, "name": spaceName[id]})
	})
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		results := make([]any, 0, len(pages))
		for _, p := range pages {
			results = append(results, map[string]any{
				"id": p.ID, "title": p.Title, "spaceId": p.SpaceID, "authorId": p.AuthorID,
				"createdAt": isoDaysAgo(p.Ago),
				"version":   map[string]any{"authorId": p.AuthorID, "createdAt": isoDaysAgo(p.Ago)},
			})
		}
		writeJSON(w, map[string]any{"results": results, "_links": map[string]any{}})
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/pages/"), "/labels")
		labels := make([]any, 0)
		for _, p := range pages {
			if p.ID == id {
				for _, l := range p.Labels {
					labels = append(labels, map[string]any{"name": l})
				}
			}
		}
		writeJSON(w, map[string]any{"results": labels})
	})
	return httptest.NewServer(mux)
}

// PagerDutyServer serves services and on-calls in PagerDuty's wire format.
func PagerDutyServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"more": false, "services": []map[string]any{
			{"id": "S1", "name": "Billing API", "description": "Payment processing",
				"escalation_policy": map[string]any{"id": "EP1"}},
			{"id": "S2", "name": "Platform Kubernetes", "description": "Cluster and deploys",
				"escalation_policy": map[string]any{"id": "EP2"}},
		}})
	})
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"more": false, "incidents": []map[string]any{
			{
				"id": "PINC1", "incident_number": 1041, "status": "resolved",
				"title":      "Billing API latency breached the error budget",
				"html_url":   "https://corp.pagerduty.com/incidents/PINC1",
				"created_at": isoDaysAgo(30), "resolved_at": isoDaysAgo(30),
				"service": map[string]any{"id": "S1", "summary": "Billing API"},
				"assignments": []map[string]any{
					{"assignee": map[string]any{
						"id": "P1", "name": "Angela Malone", "email": "angela@corp.com"}},
				},
				"acknowledgements": []map[string]any{
					{"acknowledger": map[string]any{
						"id": "P2", "name": "Grace Kim", "email": "grace@corp.com"}},
				},
			},
		}})
	})
	mux.HandleFunc("/oncalls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"more": false, "oncalls": []map[string]any{
			{"user": map[string]any{"id": "P1", "name": "Angela Malone", "email": "angela@corp.com"},
				"escalation_policy": map[string]any{"id": "EP1"}},
			{"user": map[string]any{"id": "P2", "name": "Grace Kim", "email": "grace@corp.com"},
				"escalation_policy": map[string]any{"id": "EP2"}},
		}})
	})
	return httptest.NewServer(mux)
}

// BuildGitRepo creates a repository under dir with the simulated history:
// Victor's heavy terraform work two years ago, Carol's recent terraform work,
// Heidi's recent search work, and a bot commit that must be skipped.
func BuildGitRepo(dir string) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return fmt.Errorf("simorg: init: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("simorg: worktree: %w", err)
	}
	commit := func(rel, content, name, email string, when time.Time) error {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return err
		}
		if _, err := wt.Add(rel); err != nil {
			return err
		}
		sig := &object.Signature{Name: name, Email: email, When: when}
		_, err := wt.Commit("touch "+rel, &git.CommitOptions{Author: sig, Committer: sig})
		return err
	}

	now := time.Now()
	steps := []struct {
		rel, content, name, email string
		when                      time.Time
	}{
		{"infra/vpc.tf", "v1", "Victor Old", "victor@corp.com", now.AddDate(-2, 0, 0)},
		{"infra/cluster.tf", "v1", "Victor Old", "victor@corp.com", now.AddDate(-2, 0, 3)},
		{"infra/dns.tf", "v1", "Victor Old", "victor@corp.com", now.AddDate(-2, 0, 6)},
		{"infra/iam.tf", "v1", "Victor Old", "victor@corp.com", now.AddDate(-2, 0, 9)},
		{"infra/vpc.tf", "v2", "Victor Old", "victor@corp.com", now.AddDate(-2, 0, 12)},
		{"infra/cluster.tf", "v2", "Carol Lee", "carol@corp.com", now.AddDate(0, 0, -14)},
		{"infra/nodepool.tf", "v1", "Carol Lee", "carol@corp.com", now.AddDate(0, 0, -3)},
		{"internal/search/rank.go", "v1", "Heidi Cho", "heidi@corp.com", now.AddDate(0, 0, -7)},
		{"go.sum", "v1", "dependabot[bot]", "1+dependabot[bot]@users.noreply.github.com",
			now.AddDate(0, 0, -1)},
	}
	for _, s := range steps {
		if err := commit(s.rel, s.content, s.name, s.email, s.when); err != nil {
			return fmt.Errorf("simorg: commit %s: %w", s.rel, err)
		}
	}
	return nil
}

// BuildIndex assembles the simulated company into a merged, canonicalized
// index under dir: it writes the org chart, CODEOWNERS, and alias fixtures,
// creates the git repository, serves each tool's wire format from in-process
// HTTP servers, and ingests all eight sources through the real connectors.
func BuildIndex(dir string) (*index.Index, error) {
	ctx := context.Background()
	write := func(name, content string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(content), 0o600)
	}
	csvPath, err := write("org.csv", OrgCSV())
	if err != nil {
		return nil, fmt.Errorf("simorg: %w", err)
	}
	ownersPath, err := write("CODEOWNERS", CodeOwners())
	if err != nil {
		return nil, fmt.Errorf("simorg: %w", err)
	}
	aliasPath, err := write("aliases.json", Aliases())
	if err != nil {
		return nil, fmt.Errorf("simorg: %w", err)
	}
	repoDir := filepath.Join(dir, "repo")
	if err := BuildGitRepo(repoDir); err != nil {
		return nil, err
	}

	slackSrv := SlackServer()
	defer slackSrv.Close()
	githubSrv := GitHubServer()
	defer githubSrv.Close()
	jiraSrv := JiraServer()
	defer jiraSrv.Close()
	confluenceSrv := ConfluenceServer()
	defer confluenceSrv.Close()
	pagerdutySrv := PagerDutyServer()
	defer pagerdutySrv.Close()

	// BuildIndex builds the people graph; BuildEpisodes collects the
	// conversations separately, so this source needs no episode options.
	slackSource := connector.NewSlackWithClient(
		slack.New("xoxb-demo", slack.WithBaseURL(slackSrv.URL)), connector.SlackOptions{})

	sources := []struct {
		Name   string
		Source connector.Source
	}{
		{"org-csv", connector.NewOrgCSV(csvPath)},
		{"codeowners", connector.NewCodeOwners(ownersPath)},
		{"slack", slackSource},
		{"github", connector.NewGitHubWithClient(
			github.New("ghp-demo", github.WithBaseURL(githubSrv.URL)),
			connector.GitHubOptions{
				Repos: []string{"corp/billing-service", "corp/webapp"}, ResolveEmails: true,
			})},
		{"jira", connector.NewJiraWithClient(
			jira.New(jiraSrv.URL, "demo@corp.com", "token"), connector.JiraOptions{})},
		{"confluence", connector.NewConfluenceWithClient(
			confluence.New(confluenceSrv.URL, "demo@corp.com", "token"),
			connector.ConfluenceOptions{})},
		{"pagerduty", connector.NewPagerDutyWithClient(
			pagerduty.New("token", pagerduty.WithBaseURL(pagerdutySrv.URL)),
			connector.PagerDutyOptions{})},
		{"git", connector.NewGitHistory(connector.GitOptions{
			Paths: []string{repoDir}, SinceDays: 900,
		})},
	}

	ix := index.New()
	if err := ix.LoadAliases(aliasPath); err != nil {
		return nil, err
	}
	for _, s := range sources {
		recs, err := s.Source.Fetch(ctx)
		if err != nil {
			return nil, fmt.Errorf("simorg: %s: %w", s.Name, err)
		}
		if len(recs) == 0 {
			return nil, fmt.Errorf("simorg: %s returned no records", s.Name)
		}
		ix.Add(recs)
	}
	ix.AutoJoin()
	ix.Canonicalize()
	return ix, nil
}

// BuildEpisodes returns everything the simulated company worked through: Slack
// conversations with their content, merged changes, resolved tickets, and
// resolved incidents, with participants resolved against ix so one person's
// work is findable across every source. It lets the demo show recall with no
// credentials.
func BuildEpisodes(ix *index.Index) (*episode.Store, error) {
	ctx := context.Background()
	slackSrv := SlackServer()
	defer slackSrv.Close()
	githubSrv := GitHubServer()
	defer githubSrv.Close()
	jiraSrv := JiraServer()
	defer jiraSrv.Close()
	pagerdutySrv := PagerDutyServer()
	defer pagerdutySrv.Close()

	sources := []struct {
		Name   string
		Source interface {
			connector.Source
			connector.EpisodeSource
		}
	}{
		{"slack", connector.NewSlackWithClient(
			slack.New("xoxb-demo", slack.WithBaseURL(slackSrv.URL)),
			connector.SlackOptions{Episodes: true, Archive: true})},
		{"github", connector.NewGitHubWithClient(
			github.New("ghp-demo", github.WithBaseURL(githubSrv.URL)),
			connector.GitHubOptions{
				Repos:    []string{"corp/billing-service", "corp/webapp"},
				Episodes: true, ResolveEmails: true,
			})},
		{"jira", connector.NewJiraWithClient(
			jira.New(jiraSrv.URL, "demo@corp.com", "token"),
			connector.JiraOptions{Episodes: true})},
		{"pagerduty", connector.NewPagerDutyWithClient(
			pagerduty.New("token", pagerduty.WithBaseURL(pagerdutySrv.URL)),
			connector.PagerDutyOptions{Episodes: true})},
	}

	store := episode.New()
	for _, s := range sources {
		if _, err := s.Source.Fetch(ctx); err != nil {
			return nil, fmt.Errorf("simorg: %s episodes: %w", s.Name, err)
		}
		eps := s.Source.Episodes()
		if ix != nil {
			ix.CanonicalizeEpisodes(eps)
		}
		for _, ep := range eps {
			store.Add(ep)
		}
	}
	return store, nil
}

// slackUser builds one users.list member.
func slackUser(id, name, email, title string) map[string]any {
	return map[string]any{"id": id, "profile": map[string]any{
		"real_name": name, "email": email, "title": title,
	}}
}

// slackChannel builds one conversations.list channel.
func slackChannel(id, name, topic string) map[string]any {
	return map[string]any{
		"id": id, "name": name,
		"topic":   map[string]any{"value": topic},
		"purpose": map[string]any{"value": topic},
	}
}

// slackMessage builds one history message with an epoch timestamp.
func slackMessage(user, text string, when time.Time) map[string]any {
	return map[string]any{
		"type": "message", "user": user, "text": text,
		"ts": fmt.Sprintf("%d.000100", when.Unix()),
	}
}

// slackThread builds a history parent that drew replies, carrying the thread
// shape Slack reports without a second call.
func slackThread(
	user, text string, when time.Time, replyCount int, replyUsers []string, latest time.Time,
) map[string]any {
	m := slackMessage(user, text, when)
	m["thread_ts"] = m["ts"]
	m["reply_count"] = replyCount
	m["reply_users"] = replyUsers
	m["latest_reply"] = fmt.Sprintf("%d.000100", latest.Unix())
	return m
}

// threadKey is the timestamp a thread's replies are keyed by.
func threadKey(when time.Time) string { return fmt.Sprintf("%d.000100", when.Unix()) }

// daysAgo returns a time n days in the past.
func daysAgo(n int) time.Time { return time.Now().AddDate(0, 0, -n) }

// isoDaysAgo formats a past time in RFC 3339, the GitHub and Confluence form.
func isoDaysAgo(n int) string { return daysAgo(n).UTC().Format(time.RFC3339) }

// jiraDaysAgo formats a past time in Jira's ISO 8601 form.
func jiraDaysAgo(n int) string { return daysAgo(n).Format("2006-01-02T15:04:05.000-0700") }

// writeJSON encodes v to the response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
