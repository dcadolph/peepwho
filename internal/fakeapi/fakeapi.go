// Package fakeapi serves the source APIs peepwho reads, enforcing the parts of
// each contract that a hand-written stub quietly ignores.
//
// A stub written inside a test answers with whatever its author typed,
// whatever the client asked for. Real APIs do not: Jira returns only the
// fields named in the query, GitHub's list endpoint carries a narrower object
// than the single-item one, and both paginate on the caller's terms. A client
// that reads a field it never requested works perfectly against a stub and
// returns nothing in production, which is exactly what once made peepwho
// produce no Jira episodes at all while every test passed.
//
// These servers close that gap. They are deliberately strict: asking for
// something the real API would not give back yields nothing here either.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// JiraIssue is one issue a fake Jira site holds. It is the whole issue; what
// reaches the client depends on the fields that client asks for.
type JiraIssue struct {
	// Key is the issue key, such as OPS-7.
	Key string
	// Summary is the issue title.
	Summary string
	// Description is the issue description, served as the rich-text document
	// Jira Cloud returns rather than as a string.
	Description string
	// AssigneeEmail is the assignee's email; empty leaves the issue unassigned.
	AssigneeEmail string
	// ReporterEmail is the reporter's email.
	ReporterEmail string
	// Components are the issue components.
	Components []string
	// Labels are the issue labels.
	Labels []string
	// ProjectKey is the project key.
	ProjectKey string
	// ProjectName is the project name.
	ProjectName string
	// Updated is the last update time in Jira's format.
	Updated string
	// ResolutionDate is when the issue was resolved; empty means unresolved.
	ResolutionDate string
	// StatusName is the workflow status name, such as Done.
	StatusName string
	// StatusCategory is the status category key, "done" once finished.
	StatusCategory string
	// CommentAuthorEmails are the people who commented, returned in the comment
	// field only when the query asks for it.
	CommentAuthorEmails []string
}

// fields returns the issue as Jira's full field map, before the caller's
// field selection narrows it.
func (i JiraIssue) fields() map[string]any {
	user := func(email string) any {
		if email == "" {
			return nil
		}
		return map[string]any{
			"accountId":    "acct-" + email,
			"emailAddress": email,
			"displayName":  email,
		}
	}
	components := make([]any, 0, len(i.Components))
	for _, c := range i.Components {
		components = append(components, map[string]any{"name": c})
	}
	out := map[string]any{
		"summary":    i.Summary,
		"assignee":   user(i.AssigneeEmail),
		"reporter":   user(i.ReporterEmail),
		"components": components,
		"labels":     i.Labels,
		"project":    map[string]any{"key": i.ProjectKey, "name": i.ProjectName},
		"issuetype":  map[string]any{"name": "Task"},
		"updated":    i.Updated,
		"status": map[string]any{
			"name":           i.StatusName,
			"statusCategory": map[string]any{"key": i.StatusCategory},
		},
	}
	if i.ResolutionDate != "" {
		out["resolutiondate"] = i.ResolutionDate
	}
	if i.Description != "" {
		out["description"] = adfDoc(i.Description)
	}
	if c := commentField(i.CommentAuthorEmails, user); c != nil {
		out["comment"] = c
	}
	return out
}

// serverFields returns the issue as a Server or Data Center site does: users
// keyed by username with no account id or email, and a plain string
// description rather than a node tree.
func (i JiraIssue) serverFields() map[string]any {
	user := func(email string) any {
		if email == "" {
			return nil
		}
		name, _, _ := strings.Cut(email, "@")
		return map[string]any{"name": name, "displayName": name}
	}
	components := make([]any, 0, len(i.Components))
	for _, c := range i.Components {
		components = append(components, map[string]any{"name": c})
	}
	out := map[string]any{
		"summary":    i.Summary,
		"assignee":   user(i.AssigneeEmail),
		"reporter":   user(i.ReporterEmail),
		"components": components,
		"labels":     i.Labels,
		"project":    map[string]any{"key": i.ProjectKey, "name": i.ProjectName},
		"issuetype":  map[string]any{"name": "Task"},
		"updated":    i.Updated,
		"status": map[string]any{
			"name":           i.StatusName,
			"statusCategory": map[string]any{"key": i.StatusCategory},
		},
	}
	if i.ResolutionDate != "" {
		out["resolutiondate"] = i.ResolutionDate
	}
	if i.Description != "" {
		out["description"] = i.Description
	}
	if c := commentField(i.CommentAuthorEmails, user); c != nil {
		out["comment"] = c
	}
	return out
}

// commentField builds the comment field the search returns when asked for it,
// or nil when the issue has no comments. shape renders one author the way the
// deployment names users.
func commentField(emails []string, shape func(string) any) map[string]any {
	if len(emails) == 0 {
		return nil
	}
	comments := make([]any, 0, len(emails))
	for _, e := range emails {
		comments = append(comments, map[string]any{"author": shape(e)})
	}
	return map[string]any{"comments": comments}
}

// adfDoc wraps text in the Atlassian Document Format tree Jira Cloud returns
// for a rich-text field, so a client that expects a plain string fails here
// the way it would in production.
func adfDoc(text string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": text}},
		}},
	}
}

// Jira is a fake Jira site. It honors the field selection and the paging window
// the client sends, which is what a stub does not. With ServerMode set it
// behaves as a self-hosted Server or Data Center site: the v2 API path,
// username-based users with no account id, and a string description instead of
// a node tree.
type Jira struct {
	// Issues are the issues the site holds.
	Issues []JiraIssue
	// ServerMode serves the v2 API and Server-shaped users and descriptions.
	ServerMode bool
	// Queries records the raw query string of every search request, so a test
	// can assert what the client actually asked for.
	Queries []string
}

// Server starts the fake site. The caller closes it.
func (j *Jira) Server() *httptest.Server {
	base := "/rest/api/3"
	if j.ServerMode {
		base = "/rest/api/2"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(base+"/myself", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"accountId": "acct-me", "emailAddress": "me@x.com"})
	})
	mux.HandleFunc(base+"/serverInfo", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": "9.4.0", "deploymentType": "Server"})
	})
	if j.ServerMode {
		// Server and Data Center still page the offset endpoint.
		mux.HandleFunc(base+"/search", j.handleOffsetSearch)
	} else {
		// Cloud retired the offset endpoint; calling it now returns 410 Gone, so
		// a client that still does must fail exactly as it does in production.
		mux.HandleFunc(base+"/search", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusGone)
		})
		mux.HandleFunc(base+"/search/jql", j.handleTokenSearch)
	}
	return httptest.NewServer(mux)
}

// handleOffsetSearch answers the Server search endpoint, windowing issues by the
// startAt offset the client sends against the total count.
func (j *Jira) handleOffsetSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	j.Queries = append(j.Queries, r.URL.RawQuery)
	wanted := wantedFields(q.Get("fields"))
	startAt, _ := strconv.Atoi(q.Get("startAt"))
	maxResults, err := strconv.Atoi(q.Get("maxResults"))
	if err != nil || maxResults <= 0 {
		maxResults = 50
	}
	issues := j.selectPage(wanted, startAt, maxResults)
	writeJSON(w, map[string]any{
		"issues": issues, "startAt": startAt, "total": len(j.Issues),
		"maxResults": maxResults,
	})
}

// handleTokenSearch answers the Cloud enhanced-search endpoint, paging by an
// opaque nextPageToken and flagging the final page rather than reporting a
// total. The token encodes the next offset, but the client must treat it as
// opaque and send it back to advance, which is what the paging code is verified
// to do.
func (j *Jira) handleTokenSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	j.Queries = append(j.Queries, r.URL.RawQuery)
	wanted := wantedFields(q.Get("fields"))
	maxResults, err := strconv.Atoi(q.Get("maxResults"))
	if err != nil || maxResults <= 0 {
		maxResults = 50
	}
	startAt := 0
	if tok := q.Get("nextPageToken"); tok != "" {
		startAt, _ = strconv.Atoi(tok)
	}
	issues := j.selectPage(wanted, startAt, maxResults)
	resp := map[string]any{"issues": issues}
	if next := startAt + len(issues); next < len(j.Issues) {
		resp["nextPageToken"] = strconv.Itoa(next)
		resp["isLast"] = false
	} else {
		resp["isLast"] = true
	}
	writeJSON(w, resp)
}

// selectPage returns the issues in [startAt, startAt+maxResults) shaped to the
// requested field set. Honoring the field selection is the point of the fake: a
// field the client did not request comes back absent, as it does in production.
func (j *Jira) selectPage(wanted map[string]bool, startAt, maxResults int) []any {
	issues := make([]any, 0, maxResults)
	for i := startAt; i < len(j.Issues) && len(issues) < maxResults; i++ {
		all := j.Issues[i].fields()
		if j.ServerMode {
			all = j.Issues[i].serverFields()
		}
		selected := make(map[string]any)
		for name, value := range all {
			if wanted[name] {
				selected[name] = value
			}
		}
		issues = append(issues, map[string]any{"key": j.Issues[i].Key, "fields": selected})
	}
	return issues
}

// wantedFields parses the comma-separated fields query parameter into a set.
func wantedFields(csv string) map[string]bool {
	wanted := make(map[string]bool)
	for _, name := range strings.Split(csv, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}
	return wanted
}

// GitHubPull is one pull request a fake repository holds.
type GitHubPull struct {
	// Number is the pull request number.
	Number int
	// Title is the pull request title.
	Title string
	// Body is the description.
	Body string
	// AuthorLogin is the author's handle.
	AuthorLogin string
	// ReviewerLogins are the requested reviewers, whom GitHub drops from the
	// list object once they actually review.
	ReviewerLogins []string
	// ReviewedBy are the people who actually submitted a review, served from the
	// per-pull reviews endpoint rather than the list object.
	ReviewedBy []string
	// CommentedBy are the people who commented, served from the issue comments
	// endpoint.
	CommentedBy []string
	// Labels are the applied labels.
	Labels []string
	// UpdatedAt is when it last changed, in RFC 3339.
	UpdatedAt string
	// MergedAt is when it merged, in RFC 3339; empty means never merged.
	MergedAt string
}

// listObject returns the pull request exactly as the list endpoint sends it.
// Fields the single-pull endpoint adds, such as merged, mergeable, and the
// file and comment counts, are deliberately absent: a client that reads one
// of those from a list response would get nothing in production.
func (p GitHubPull) listObject(owner, repo string) map[string]any {
	labels := make([]any, 0, len(p.Labels))
	for _, l := range p.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	reviewers := make([]any, 0, len(p.ReviewerLogins))
	for _, l := range p.ReviewerLogins {
		reviewers = append(reviewers, map[string]any{"login": l})
	}
	state := "open"
	if p.MergedAt != "" {
		state = "closed"
	}
	out := map[string]any{
		"number":              p.Number,
		"state":               state,
		"title":               p.Title,
		"body":                p.Body,
		"html_url":            fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, p.Number),
		"user":                map[string]any{"login": p.AuthorLogin},
		"labels":              labels,
		"requested_reviewers": reviewers,
		"assignees":           []any{},
		"updated_at":          p.UpdatedAt,
		"draft":               false,
	}
	if p.MergedAt != "" {
		out["merged_at"] = p.MergedAt
	}
	return out
}

// GitHubRepo is one repository a fake GitHub holds.
type GitHubRepo struct {
	// Owner is the owning account or org.
	Owner string
	// Name is the repository name.
	Name string
	// Contributors maps a login to its commit count.
	Contributors map[string]int
	// Pulls are the repository's pull requests, of any state.
	Pulls []GitHubPull
}

// GitHub is a fake GitHub API. It pages with a Link header and answers the
// list endpoint with the narrower object the real list endpoint returns.
type GitHub struct {
	// Repos are the repositories the API holds.
	Repos []GitHubRepo
	// PerPage caps a page; zero honors whatever the client asks for.
	PerPage int
}

// Server starts the fake API. The caller closes it.
func (g *GitHub) Server() *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/repos/"), "/"), "/")
		if len(parts) < 2 {
			http.NotFound(w, r)
			return
		}
		repo := g.find(parts[0], parts[1])
		if repo == nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case len(parts) == 2:
			writeJSON(w, map[string]any{
				"name": repo.Name, "full_name": repo.Owner + "/" + repo.Name,
			})
		case parts[2] == "contributors":
			out := make([]any, 0, len(repo.Contributors))
			for login, count := range repo.Contributors {
				out = append(out, map[string]any{"login": login, "contributions": count})
			}
			writeJSON(w, out)
		case len(parts) == 5 && parts[2] == "pulls" && parts[4] == "reviews":
			g.servePullSubList(w, repo, parts[3], func(p GitHubPull) []string { return p.ReviewedBy })
		case len(parts) == 5 && parts[2] == "issues" && parts[4] == "comments":
			g.servePullSubList(w, repo, parts[3], func(p GitHubPull) []string { return p.CommentedBy })
		case parts[2] == "pulls":
			g.servePulls(w, r, srv, repo)
		default:
			http.NotFound(w, r)
		}
	})
	srv = httptest.NewServer(mux)
	return srv
}

// servePullSubList answers a per-pull sub-resource, the reviews or the issue
// comments, as a list of objects each naming a user, which is how those
// endpoints identify who actually took part in a change.
func (g *GitHub) servePullSubList(
	w http.ResponseWriter, repo *GitHubRepo, number string, logins func(GitHubPull) []string,
) {
	out := make([]any, 0)
	for _, p := range repo.Pulls {
		if strconv.Itoa(p.Number) == number {
			for _, l := range logins(p) {
				out = append(out, map[string]any{"user": map[string]any{"login": l}})
			}
		}
	}
	writeJSON(w, out)
}

// servePulls answers the list endpoint, honoring the state filter and paging
// with a Link header the way GitHub does.
func (g *GitHub) servePulls(
	w http.ResponseWriter, r *http.Request, srv *httptest.Server, repo *GitHubRepo,
) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		// GitHub defaults to open, which is what silently hides every merged
		// pull request from a client that forgets to ask for all of them.
		state = "open"
	}
	var matching []GitHubPull
	for _, p := range repo.Pulls {
		merged := p.MergedAt != ""
		if state == "all" || (state == "open" && !merged) || (state == "closed" && merged) {
			matching = append(matching, p)
		}
	}

	perPage, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || perPage <= 0 {
		perPage = 30
	}
	if g.PerPage > 0 && g.PerPage < perPage {
		perPage = g.PerPage
	}
	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page <= 0 {
		page = 1
	}
	start := (page - 1) * perPage
	if start > len(matching) {
		start = len(matching)
	}
	end := min(start+perPage, len(matching))
	out := make([]any, 0, end-start)
	for _, p := range matching[start:end] {
		out = append(out, p.listObject(repo.Owner, repo.Name))
	}
	if end < len(matching) && srv != nil {
		next := fmt.Sprintf("%s%s?state=%s&per_page=%d&page=%d",
			srv.URL, r.URL.Path, state, perPage, page+1)
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", next))
	}
	writeJSON(w, out)
}

// find returns the named repository, or nil.
func (g *GitHub) find(owner, name string) *GitHubRepo {
	for i := range g.Repos {
		if g.Repos[i].Owner == owner && g.Repos[i].Name == name {
			return &g.Repos[i]
		}
	}
	return nil
}

// PagerDutyIncident is one incident a fake PagerDuty holds.
type PagerDutyIncident struct {
	// ID is the incident id.
	ID string
	// Number is the human-facing incident number.
	Number int
	// Title is the incident title.
	Title string
	// Description is the incident description.
	Description string
	// Status is the incident status, such as resolved or triggered.
	Status string
	// ServiceName is the affected service.
	ServiceName string
	// CreatedAt is when it opened, in RFC 3339.
	CreatedAt string
	// ResolvedAt is when it resolved, in RFC 3339; empty means unresolved.
	ResolvedAt string
	// AssigneeEmails are the people it was assigned to.
	AssigneeEmails []string
	// AcknowledgerEmails are the people who picked it up.
	AcknowledgerEmails []string
	// ResolverEmail is who resolved the incident, emitted as
	// last_status_change_by, which a manually-resolved incident carries even
	// with no assignee.
	ResolverEmail string
	// Notes are the incident's triage and resolution notes, served from the
	// per-incident notes endpoint rather than inline.
	Notes []string
}

// PagerDuty is a fake PagerDuty API honoring the status filter and the
// offset paging the client sends.
type PagerDuty struct {
	// Incidents are the incidents the API holds.
	Incidents []PagerDutyIncident
	// Queries records each incidents query, so a test can assert the filter.
	Queries []string
}

// Server starts the fake API. The caller closes it.
func (p *PagerDuty) Server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"users": []any{}, "more": false})
	})
	mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"services": []any{}, "more": false})
	})
	mux.HandleFunc("/oncalls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"oncalls": []any{}, "more": false})
	})
	// The notes for one incident come from a per-incident endpoint, not inline,
	// so a client that wants them must ask for each incident separately.
	mux.HandleFunc("/incidents/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/incidents/"), "/notes")
		notes := make([]any, 0)
		for _, in := range p.Incidents {
			if in.ID == id {
				for _, n := range in.Notes {
					notes = append(notes, map[string]any{"content": n})
				}
			}
		}
		writeJSON(w, map[string]any{"notes": notes})
	})
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.Queries = append(p.Queries, r.URL.RawQuery)
		wantStatuses := q["statuses[]"]

		var matching []PagerDutyIncident
		for _, in := range p.Incidents {
			if len(wantStatuses) == 0 {
				matching = append(matching, in)
				continue
			}
			for _, s := range wantStatuses {
				if in.Status == s {
					matching = append(matching, in)
					break
				}
			}
		}

		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil || limit <= 0 {
			limit = 25
		}
		if offset > len(matching) {
			offset = len(matching)
		}
		end := min(offset+limit, len(matching))
		out := make([]any, 0, end-offset)
		for _, in := range matching[offset:end] {
			out = append(out, in.object())
		}
		writeJSON(w, map[string]any{"incidents": out, "more": end < len(matching)})
	})
	return httptest.NewServer(mux)
}

// object returns the incident as the incidents endpoint sends it.
func (i PagerDutyIncident) object() map[string]any {
	person := func(email string) map[string]any {
		return map[string]any{"id": "PD-" + email, "summary": email, "email": email}
	}
	assignments := make([]any, 0, len(i.AssigneeEmails))
	for _, e := range i.AssigneeEmails {
		assignments = append(assignments, map[string]any{"assignee": person(e)})
	}
	acks := make([]any, 0, len(i.AcknowledgerEmails))
	for _, e := range i.AcknowledgerEmails {
		acks = append(acks, map[string]any{"acknowledger": person(e)})
	}
	out := map[string]any{
		"id":               i.ID,
		"incident_number":  i.Number,
		"title":            i.Title,
		"description":      i.Description,
		"status":           i.Status,
		"html_url":         "https://x.pagerduty.com/incidents/" + i.ID,
		"created_at":       i.CreatedAt,
		"service":          map[string]any{"id": "S-" + i.ServiceName, "summary": i.ServiceName},
		"assignments":      assignments,
		"acknowledgements": acks,
	}
	if i.ResolvedAt != "" {
		out["resolved_at"] = i.ResolvedAt
	}
	if i.ResolverEmail != "" {
		out["last_status_change_by"] = map[string]any{
			"id": "PD-" + i.ResolverEmail, "type": "user_reference",
			"summary": i.ResolverEmail, "email": i.ResolverEmail,
		}
	}
	return out
}

// ConfluencePage is one page a fake Confluence site holds.
type ConfluencePage struct {
	// Title is the page title.
	Title string
	// SpaceKey is the space key.
	SpaceKey string
	// SpaceName is the space name.
	SpaceName string
	// Labels are the page labels.
	Labels []string
	// CreatedByEmail is the page creator's email.
	CreatedByEmail string
	// CreatedAt is when the page was created, in RFC 3339.
	CreatedAt string
	// EditedByEmail is the last editor's email.
	EditedByEmail string
	// EditedAt is when the page was last edited, in RFC 3339.
	EditedAt string
	// Body is the page content, served in the storage format when the read asks
	// for it and withheld otherwise.
	Body string
}

// sections returns the page split by the expansion each part needs, so the
// server can withhold anything the client did not ask to expand.
func (p ConfluencePage) sections(server bool) map[string]any {
	user := func(email string) any {
		if email == "" {
			return nil
		}
		if server {
			// Server and Data Center: username and user key, no account id or
			// email, exactly as a public wiki such as Apache's returns.
			name, _, _ := strings.Cut(email, "@")
			return map[string]any{"username": name, "userKey": "key-" + name, "displayName": name}
		}
		return map[string]any{
			"accountId":   "acct-" + email,
			"email":       email,
			"publicName":  email,
			"displayName": email,
		}
	}
	labels := make([]any, 0, len(p.Labels))
	for _, l := range p.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	return map[string]any{
		"space": map[string]any{"key": p.SpaceKey, "name": p.SpaceName},
		"metadata.labels": map[string]any{
			"labels": map[string]any{"results": labels},
		},
		"history": map[string]any{
			"history": map[string]any{"createdBy": user(p.CreatedByEmail), "createdDate": p.CreatedAt},
		},
		"version": map[string]any{
			"version": map[string]any{"by": user(p.EditedByEmail), "when": p.EditedAt},
		},
		"body.storage": map[string]any{
			"body": map[string]any{"storage": map[string]any{"value": p.Body}},
		},
	}
}

// Confluence is a fake Confluence site. It withholds anything the client did
// not name in expand, which is how the real API behaves and what turns a
// forgotten expansion into a visibly empty field rather than a passing test.
// With ServerMode set it behaves as a self-hosted Server or Data Center site:
// the REST API at the root rather than under /wiki, and username-based users.
type Confluence struct {
	// ServerMode serves the Server/Data Center path and user shape.
	ServerMode bool
	// Pages are the pages the site holds.
	Pages []ConfluencePage
	// Queries records each search query string.
	Queries []string
}

// Server starts the fake site. The caller closes it. Server and Data Center
// read through the v1 content search; Cloud reads through the v2 API, which
// names people by account id and serves the space name and labels apart from
// the page, so a client that expects them inline sees them missing here too.
func (c *Confluence) Server() *httptest.Server {
	mux := http.NewServeMux()
	if c.ServerMode {
		base := "/rest/api"
		mux.HandleFunc(base+"/space", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"results": []any{}, "size": 0})
		})
		mux.HandleFunc(base+"/user/current", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"accountId": "acct-me", "email": "me@x.com"})
		})
		mux.HandleFunc(base+"/content/search", c.handleV1Search)
		return httptest.NewServer(mux)
	}
	mux.HandleFunc("/wiki/rest/api/user/current", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"accountId": "acct-me", "email": "me@x.com"})
	})
	mux.HandleFunc("/wiki/rest/api/user", c.handleV2User)
	mux.HandleFunc("/wiki/api/v2/spaces", c.handleV2Spaces)
	mux.HandleFunc("/wiki/api/v2/spaces/", c.handleV2SpaceByID)
	mux.HandleFunc("/wiki/api/v2/pages", c.handleV2Pages)
	mux.HandleFunc("/wiki/api/v2/pages/", c.handleV2PageLabels)
	return httptest.NewServer(mux)
}

// handleV1Search answers the Server content search, withholding anything the
// client did not name in expand.
func (c *Confluence) handleV1Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.Queries = append(c.Queries, r.URL.RawQuery)
	expanded := make(map[string]bool)
	for _, name := range strings.Split(q.Get("expand"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			expanded[name] = true
		}
	}
	start, _ := strconv.Atoi(q.Get("start"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 25
	}
	if start > len(c.Pages) {
		start = len(c.Pages)
	}
	end := min(start+limit, len(c.Pages))
	results := make([]any, 0, end-start)
	for _, p := range c.Pages[start:end] {
		out := map[string]any{"title": p.Title, "type": "page"}
		for name, section := range p.sections(c.ServerMode) {
			if !expanded[name] {
				continue
			}
			// A section may carry its own key, as history and version do.
			for k, v := range section.(map[string]any) {
				out[k] = v
			}
			if name == "space" {
				out["space"] = section
			}
		}
		results = append(results, out)
	}
	writeJSON(w, map[string]any{"results": results, "size": len(results), "limit": limit})
}

// confluenceAccountID encodes an email as the account id the fake Cloud names
// people by, or empty when there is no user.
func confluenceAccountID(email string) string {
	if email == "" {
		return ""
	}
	return "acct:" + email
}

// confluenceSpaceID is the synthetic v2 space id for a space key.
func confluenceSpaceID(key string) string { return "sid:" + key }

// handleV2User resolves an account id to a display name and email, the identity
// the v2 content endpoints leave to a separate lookup.
func (c *Confluence) handleV2User(w http.ResponseWriter, r *http.Request) {
	acct := r.URL.Query().Get("accountId")
	email := strings.TrimPrefix(acct, "acct:")
	writeJSON(w, map[string]any{"accountId": acct, "displayName": email, "email": email})
}

// handleV2Spaces resolves a space key to its id and name.
func (c *Confluence) handleV2Spaces(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("keys")
	for _, p := range c.Pages {
		if p.SpaceKey == key {
			writeJSON(w, map[string]any{"results": []any{map[string]any{
				"id": confluenceSpaceID(key), "key": key, "name": p.SpaceName,
			}}})
			return
		}
	}
	writeJSON(w, map[string]any{"results": []any{}})
}

// handleV2SpaceByID resolves a numeric space id to its key and name.
func (c *Confluence) handleV2SpaceByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/spaces/")
	key := strings.TrimPrefix(id, "sid:")
	name := ""
	for _, p := range c.Pages {
		if p.SpaceKey == key {
			name = p.SpaceName
			break
		}
	}
	writeJSON(w, map[string]any{"id": id, "key": key, "name": name})
}

// handleV2Pages pages through the pages the space-id filter selects, following a
// cursor that encodes the next offset, and withholds the space name and labels
// the way v2 does.
func (c *Confluence) handleV2Pages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c.Queries = append(c.Queries, r.URL.RawQuery)
	wantSpaces := q["space-id"]
	var matched []int
	for i, p := range c.Pages {
		if len(wantSpaces) == 0 || slices.Contains(wantSpaces, confluenceSpaceID(p.SpaceKey)) {
			matched = append(matched, i)
		}
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 25
	}
	start, _ := strconv.Atoi(q.Get("cursor"))
	if start > len(matched) {
		start = len(matched)
	}
	end := min(start+limit, len(matched))
	results := make([]any, 0, end-start)
	for _, idx := range matched[start:end] {
		p := c.Pages[idx]
		obj := map[string]any{
			"id": strconv.Itoa(idx), "title": p.Title,
			"spaceId":   confluenceSpaceID(p.SpaceKey),
			"createdAt": p.CreatedAt,
			"version": map[string]any{
				"authorId": confluenceAccountID(p.EditedByEmail), "createdAt": p.EditedAt,
			},
		}
		if r.URL.Query().Get("body-format") == "storage" {
			obj["body"] = map[string]any{"storage": map[string]any{"value": p.Body}}
		}
		if aid := confluenceAccountID(p.CreatedByEmail); aid != "" {
			obj["authorId"] = aid
		}
		results = append(results, obj)
	}
	links := map[string]any{}
	if end < len(matched) {
		nq := url.Values{}
		if l := q.Get("limit"); l != "" {
			nq.Set("limit", l)
		}
		for _, s := range wantSpaces {
			nq.Add("space-id", s)
		}
		nq.Set("cursor", strconv.Itoa(end))
		links["next"] = "/wiki/api/v2/pages?" + nq.Encode()
	}
	writeJSON(w, map[string]any{"results": results, "_links": links})
}

// handleV2PageLabels answers the per-page labels endpoint the v2 read needs
// because pages do not carry labels inline.
func (c *Confluence) handleV2PageLabels(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/pages/")
	idStr := strings.TrimSuffix(rest, "/labels")
	labels := make([]any, 0)
	if i, err := strconv.Atoi(idStr); err == nil && i >= 0 && i < len(c.Pages) {
		for _, l := range c.Pages[i].Labels {
			labels = append(labels, map[string]any{"name": l})
		}
	}
	writeJSON(w, map[string]any{"results": labels})
}

// SlackUser is one member of a fake Slack workspace.
type SlackUser struct {
	// ID is the Slack user id.
	ID string
	// Name is the handle.
	Name string
	// RealName is the display name.
	RealName string
	// Email is the address, present only when the token has users:read.email.
	Email string
	// Title is the profile title.
	Title string
	// Bot marks a bot account.
	Bot bool
	// Deleted marks a deactivated account.
	Deleted bool
}

// SlackMessage is one message in a fake channel. A message with Replies is a
// thread parent: conversations.history returns it with the thread fields set,
// and conversations.replies returns the parent followed by its replies.
type SlackMessage struct {
	// User is the author's user id.
	User string
	// Text is the body.
	Text string
	// TS is the message timestamp.
	TS string
	// Subtype marks a non-human message when set.
	Subtype string
	// Replies are the thread replies under this parent.
	Replies []SlackMessage
}

// SlackChannel is one conversation in a fake workspace.
type SlackChannel struct {
	// ID is the channel id.
	ID string
	// Name is the channel name.
	Name string
	// Topic is the channel topic.
	Topic string
	// Purpose is the channel purpose.
	Purpose string
	// IsPrivate marks a private channel.
	IsPrivate bool
	// IsMember reports whether the bot is already in the channel.
	IsMember bool
	// Messages are the channel's messages, newest handling left to the client.
	Messages []SlackMessage
}

// Slack is a fake Slack workspace that enforces the parts of the Web API a
// hand-written stub ignores: cursor pagination with the has_more/next_cursor
// pairing, the small per-request object cap the post-2025 history tier imposes
// (a client that assumes it gets its requested limit under-reads busy channels),
// and 429 responses with a Retry-After header. Slack was the one major source
// with no contract simulator, which is how the drift that broke Jira could have
// hidden here too.
type Slack struct {
	// URL is the archive URL auth.test reports, used to build permalinks.
	URL string
	// Team is the workspace name.
	Team string
	// BotUserID is the id auth.test reports for the caller.
	BotUserID string
	// Users are the workspace members.
	Users []SlackUser
	// Channels are the conversations.
	Channels []SlackChannel
	// HistoryPageCap caps objects per conversations.history and .replies page.
	// Zero models the restricted tier's cap of 15, which is the point of the
	// fake; set it higher to model a Marketplace-approved app.
	HistoryPageCap int
	// ListPageCap caps objects per users.list and conversations.list page; zero
	// uses 200.
	ListPageCap int
	// ThrottleFirst makes the first N history/replies calls answer 429 with a
	// zero Retry-After, exercising retry handling without a real wait.
	ThrottleFirst int
	// calls counts requests per method so a test can assert paging happened.
	calls map[string]int
	// throttled counts how many 429s have been served so far.
	throttled int
}

// Calls returns how many times a Web API method was called.
func (s *Slack) Calls(method string) int { return s.calls[method] }

// Server starts the fake workspace. The caller closes it.
func (s *Slack) Server() *httptest.Server {
	s.calls = map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		s.calls["auth.test"]++
		writeJSON(w, map[string]any{"ok": true, "url": s.URL, "team": s.Team, "user_id": s.BotUserID})
	})
	mux.HandleFunc("/conversations.join", func(w http.ResponseWriter, _ *http.Request) {
		s.calls["conversations.join"]++
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, r *http.Request) {
		s.calls["users.list"]++
		_ = r.ParseForm()
		start := atoiOr(r.PostForm.Get("cursor"), 0)
		end := min(start+s.listCap(), len(s.Users))
		members := make([]any, 0, end-start)
		for _, u := range s.Users[start:end] {
			members = append(members, map[string]any{
				"id": u.ID, "name": u.Name, "deleted": u.Deleted, "is_bot": u.Bot,
				"profile": map[string]any{"real_name": u.RealName, "email": u.Email, "title": u.Title},
			})
		}
		writeJSON(w, withNextCursor(map[string]any{"ok": true, "members": members}, end, len(s.Users)))
	})
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		s.calls["conversations.list"]++
		_ = r.ParseForm()
		start := atoiOr(r.PostForm.Get("cursor"), 0)
		end := min(start+s.listCap(), len(s.Channels))
		chans := make([]any, 0, end-start)
		for _, ch := range s.Channels[start:end] {
			chans = append(chans, map[string]any{
				"id": ch.ID, "name": ch.Name, "is_private": ch.IsPrivate, "is_member": ch.IsMember,
				"topic": map[string]any{"value": ch.Topic}, "purpose": map[string]any{"value": ch.Purpose},
			})
		}
		writeJSON(w, withNextCursor(map[string]any{"ok": true, "channels": chans}, end, len(s.Channels)))
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		s.calls["conversations.history"]++
		if s.maybeThrottle(w) {
			return
		}
		_ = r.ParseForm()
		ch := s.channel(r.PostForm.Get("channel"))
		if ch == nil {
			writeJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		all := make([]any, len(ch.Messages))
		for i, m := range ch.Messages {
			all[i] = historyMessage(m)
		}
		s.pageMessages(w, all, r.PostForm.Get("cursor"))
	})
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		s.calls["conversations.replies"]++
		if s.maybeThrottle(w) {
			return
		}
		_ = r.ParseForm()
		ch := s.channel(r.PostForm.Get("channel"))
		ts := r.PostForm.Get("ts")
		if ch == nil {
			writeJSON(w, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		flat := threadMessages(ch, ts)
		all := make([]any, len(flat))
		for i, m := range flat {
			all[i] = replyMessage(m, ts)
		}
		s.pageMessages(w, all, r.PostForm.Get("cursor"))
	})
	return httptest.NewServer(mux)
}

// listCap is the object cap for the list endpoints.
func (s *Slack) listCap() int {
	if s.ListPageCap > 0 {
		return s.ListPageCap
	}
	return 200
}

// historyCap is the per-page object cap for history and replies. The default
// models the restricted tier that caps these methods at 15 objects.
func (s *Slack) historyCap() int {
	if s.HistoryPageCap > 0 {
		return s.HistoryPageCap
	}
	return 15
}

// maybeThrottle serves a 429 with a zero Retry-After for the first ThrottleFirst
// history and replies calls, so a client's retry path runs without a real wait.
func (s *Slack) maybeThrottle(w http.ResponseWriter) bool {
	if s.throttled < s.ThrottleFirst {
		s.throttled++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"ok": false, "error": "ratelimited"})
		return true
	}
	return false
}

// channel returns the channel with the given id, or nil.
func (s *Slack) channel(id string) *SlackChannel {
	for i := range s.Channels {
		if s.Channels[i].ID == id {
			return &s.Channels[i]
		}
	}
	return nil
}

// pageMessages writes one page of messages capped at historyCap, pairing
// has_more with a next_cursor exactly as Slack does.
func (s *Slack) pageMessages(w http.ResponseWriter, all []any, cursor string) {
	start := atoiOr(cursor, 0)
	if start > len(all) {
		start = len(all)
	}
	end := min(start+s.historyCap(), len(all))
	resp := map[string]any{"ok": true, "messages": all[start:end], "has_more": end < len(all)}
	if end < len(all) {
		resp["response_metadata"] = map[string]any{"next_cursor": strconv.Itoa(end)}
	}
	writeJSON(w, resp)
}

// historyMessage renders a message as conversations.history returns it, setting
// the thread fields on a parent that has replies.
func historyMessage(m SlackMessage) map[string]any {
	o := map[string]any{"type": "message", "user": m.User, "text": m.Text, "ts": m.TS}
	if m.Subtype != "" {
		o["subtype"] = m.Subtype
	}
	if len(m.Replies) > 0 {
		o["thread_ts"] = m.TS
		o["reply_count"] = len(m.Replies)
		o["reply_users"] = replyUsers(m.Replies)
		o["latest_reply"] = m.Replies[len(m.Replies)-1].TS
	}
	return o
}

// replyMessage renders a message as conversations.replies returns it, tagged
// with its thread parent.
func replyMessage(m SlackMessage, threadTS string) map[string]any {
	o := map[string]any{"type": "message", "user": m.User, "text": m.Text, "ts": m.TS, "thread_ts": threadTS}
	if m.Subtype != "" {
		o["subtype"] = m.Subtype
	}
	return o
}

// threadMessages returns the parent whose TS is ts followed by its replies, or
// nil when no such thread exists.
func threadMessages(ch *SlackChannel, ts string) []SlackMessage {
	for _, m := range ch.Messages {
		if m.TS == ts {
			return append([]SlackMessage{m}, m.Replies...)
		}
	}
	return nil
}

// replyUsers returns up to five distinct reply authors, the cap Slack applies to
// the reply_users field on a thread parent.
func replyUsers(replies []SlackMessage) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 5)
	for _, r := range replies {
		if r.User == "" || seen[r.User] {
			continue
		}
		seen[r.User] = true
		out = append(out, r.User)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// withNextCursor adds a next_cursor when more objects remain past end.
func withNextCursor(resp map[string]any, end, total int) map[string]any {
	if end < total {
		resp["response_metadata"] = map[string]any{"next_cursor": strconv.Itoa(end)}
	}
	return resp
}

// atoiOr parses s as an int, returning def when it is empty or invalid.
func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
