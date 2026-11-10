// Package github is a minimal GitHub REST client scoped to what whotoask ingests:
// repositories, contributors, pull requests, CODEOWNERS, and users. The token
// is held only in memory, never serialized, and never logged.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
	"github.com/dcadolph/whotoask/internal/util"
)

// defaultBaseURL is the GitHub REST API root.
const defaultBaseURL = "https://api.github.com"

// Client calls the GitHub REST API.
type Client struct {
	// token is the bearer token; it is never serialized or logged.
	token string
	// baseURL is the API root, overridable for tests.
	baseURL string
	// http performs requests.
	http httputil.Doer
	// maxRetries bounds retries on a secondary rate limit.
	maxRetries int
	// maxPages caps how many pages one listing walks.
	maxPages int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = u
		}
	}
}

// WithMaxPages raises how many pages one listing may walk. The default bounds
// a scheduled run so a pathological repository cannot eat the rate budget; an
// assessment reads a repository once and wants the depth, so it asks for more
// deliberately. A non-positive value keeps the default.
func WithMaxPages(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.maxPages = n
		}
	}
}

// apiTimeout bounds one HTTP exchange so a hung server cannot stall a run.
const apiTimeout = 60 * time.Second

// New returns a Client for token. It panics on an empty token; callers validate
// token presence before constructing the client.
func New(token string, opts ...Option) *Client {
	if token == "" {
		panic("github: New requires a non-empty token")
	}
	c := &Client{
		token: token, baseURL: defaultBaseURL, http: httputil.NewClient(apiTimeout),
		maxRetries: 3, maxPages: defaultMaxPages,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Repo is a repository's indexable metadata.
type Repo struct {
	// Name is the repository name.
	Name string `json:"name"`
	// FullName is the owner/name path.
	FullName string `json:"full_name"`
	// Description is the repository description.
	Description string `json:"description"`
	// Topics are the repository's topic tags.
	Topics []string `json:"topics"`
}

// Contributor is a repository contributor.
type Contributor struct {
	// Login is the contributor's handle.
	Login string `json:"login"`
	// Type is the account type, "User" or "Bot". GitHub App bots also carry a
	// "[bot]" login suffix, but the type is the API's own word for it.
	Type string `json:"type"`
	// Contributions is the contributor's commit count.
	Contributions int `json:"contributions"`
}

// IsBot reports whether the contributor is an automation account.
func (c Contributor) IsBot() bool {
	return c.Type == "Bot" || strings.HasSuffix(c.Login, "[bot]")
}

// label is a pull request label.
type label struct {
	// Name is the label text.
	Name string `json:"name"`
}

// account is a user reference inside other payloads.
type account struct {
	// Login is the user's handle.
	Login string `json:"login"`
}

// PullRequest is the subset of a pull request whotoask reads.
type PullRequest struct {
	// Number is the pull request number.
	Number int `json:"number"`
	// HTMLURL links to the pull request on GitHub.
	HTMLURL string `json:"html_url"`
	// Title is the pull request title.
	Title string `json:"title"`
	// Body is the pull request description, which is where the reason for a
	// change is usually written. The list endpoint returns it, so reading it
	// costs no extra request.
	Body string `json:"body"`
	// User is the author.
	User account `json:"user"`
	// Labels are the applied labels.
	Labels []label `json:"labels"`
	// RequestedReviewers are users asked to review.
	RequestedReviewers []account `json:"requested_reviewers"`
	// Assignees are users assigned to the pull request.
	Assignees []account `json:"assignees"`
	// UpdatedAt is when the pull request last changed.
	UpdatedAt time.Time `json:"updated_at"`
	// MergedAt is when the pull request was merged; zero when it was not.
	MergedAt time.Time `json:"merged_at"`
}

// Merged reports whether the pull request landed, which is what makes it a
// record of how something was actually changed.
func (p PullRequest) Merged() bool { return !p.MergedAt.IsZero() }

// People returns everyone who took part: the author first, then reviewers and
// assignees, without repeats.
func (p PullRequest) People() []string {
	out := []string{p.User.Login}
	seen := map[string]bool{p.User.Login: true}
	for _, a := range append(append([]account{}, p.RequestedReviewers...), p.Assignees...) {
		if a.Login == "" || seen[a.Login] {
			continue
		}
		seen[a.Login] = true
		out = append(out, a.Login)
	}
	return out
}

// Author returns the pull request author's login.
func (p PullRequest) Author() string { return p.User.Login }

// LabelNames returns the label names.
func (p PullRequest) LabelNames() []string { return labelNames(p.Labels) }

// Reviewers returns the requested reviewers' logins.
func (p PullRequest) Reviewers() []string { return accountLogins(p.RequestedReviewers) }

// AssigneeLogins returns the assignees' logins.
func (p PullRequest) AssigneeLogins() []string { return accountLogins(p.Assignees) }

// accountLogins returns the logins of accounts.
func accountLogins(accs []account) []string {
	out := make([]string, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.Login)
	}
	return out
}

// labelNames returns the names of labels.
func labelNames(labels []label) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

// Account is a user's public profile subset.
type Account struct {
	// Login is the user's handle.
	Login string `json:"login"`
	// Name is the user's display name.
	Name string `json:"name"`
	// Email is the user's public email, often empty.
	Email string `json:"email"`
}

// Repo returns a repository's metadata.
func (c *Client) Repo(ctx context.Context, owner, repo string) (Repo, error) {
	var r Repo
	err := c.get(ctx, repoPath(owner, repo), nil, &r)
	return r, err
}

// Contributors returns a repository's contributors, most commits first,
// following pagination up to the client page cap, in pages of 100.
func (c *Client) Contributors(ctx context.Context, owner, repo string) ([]Contributor, error) {
	q := url.Values{"per_page": {"100"}}
	return getAll[Contributor](ctx, c, repoPath(owner, repo, "contributors"), q)
}

// PullRequests returns a repository's pull requests of any state, most
// recently updated first, following pagination up to the client page cap.
func (c *Client) PullRequests(ctx context.Context, owner, repo string) ([]PullRequest, error) {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	return getAll[PullRequest](ctx, c, repoPath(owner, repo, "pulls"), q)
}

// review is one submitted review on a pull request.
type review struct {
	// User is the reviewer.
	User account `json:"user"`
}

// issueComment is one comment on a pull request or issue.
type issueComment struct {
	// User is the comment author.
	User account `json:"user"`
}

// PullReviewers returns the distinct logins of people who actually submitted a
// review on a pull request. This differs from the requested_reviewers on the
// list object, which GitHub clears once a person reviews, so it is the reviewer
// who acted, often the strongest signal of who knows a change.
func (c *Client) PullReviewers(ctx context.Context, owner, repo string, number int) ([]string, error) {
	revs, err := getAll[review](ctx, c,
		repoPath(owner, repo, "pulls", strconv.Itoa(number), "reviews"), url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	logins := make([]string, 0, len(revs))
	for _, r := range revs {
		logins = append(logins, r.User.Login)
	}
	return util.Distinct(logins, func(l string) string { return l }), nil
}

// PullCommenters returns the distinct logins of people who commented on a pull
// request. Pull comments live on the issue comments endpoint. It captures the
// person who diagnosed a fix in discussion without ever being a reviewer.
func (c *Client) PullCommenters(ctx context.Context, owner, repo string, number int) ([]string, error) {
	comments, err := getAll[issueComment](ctx, c,
		repoPath(owner, repo, "issues", strconv.Itoa(number), "comments"), url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	logins := make([]string, 0, len(comments))
	for _, cm := range comments {
		logins = append(logins, cm.User.Login)
	}
	return util.Distinct(logins, func(l string) string { return l }), nil
}

// OrgRepos returns an org's repositories, most recently updated first,
// following pagination up to the client page cap, in pages of 100.
func (c *Client) OrgRepos(ctx context.Context, org string) ([]Repo, error) {
	q := url.Values{"per_page": {"100"}, "sort": {"updated"}}
	return getAll[Repo](ctx, c, "/orgs/"+url.PathEscape(org)+"/repos", q)
}

// Account returns a user's public profile.
func (c *Client) Account(ctx context.Context, login string) (Account, error) {
	var a Account
	err := c.get(ctx, "/users/"+url.PathEscape(login), nil, &a)
	return a, err
}

// Ping verifies the token with the authenticated-user endpoint, the cheapest
// read-only call that proves the token is valid. It returns nil when the token
// is accepted, a *httputil.StatusError for a non-200 status such as 401 for a
// bad token, or the transport error when the API is unreachable.
func (c *Client) Ping(ctx context.Context) error {
	resp, _, err := httputil.Do(ctx, c.http, c.maxRetries, githubRetryable, httputil.Get(ctx, c.baseURL+"/user", "Authorization", "Bearer "+c.token, "Accept", "application/vnd.github+json"))
	if errors.Is(err, httputil.ErrRateLimited) {
		return fmt.Errorf("github ping: %w", ErrRateLimited)
	}
	if err != nil {
		return fmt.Errorf("github ping: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &httputil.StatusError{Code: resp.StatusCode}
	}
	return nil
}

// FileContents returns the decoded contents of a file, or ErrNotFound if it is
// absent.
func (c *Client) FileContents(ctx context.Context, owner, repo, path string) ([]byte, error) {
	var resp struct {
		// Content is the base64-encoded file content.
		Content string `json:"content"`
		// Encoding is the content encoding, normally base64.
		Encoding string `json:"encoding"`
	}
	endpoint := repoPath(owner, repo, "contents") + "/" + escapeSegments(path)
	if err := c.get(ctx, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Encoding != "base64" {
		return []byte(resp.Content), nil
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(resp.Content, "\n", ""))
}

// repoPath builds "/repos/{owner}/{repo}[/rest...]" with every segment escaped
// so a name carrying a slash or reserved character cannot alter the path.
func repoPath(owner, repo string, rest ...string) string {
	segs := append([]string{"repos", owner, repo}, rest...)
	return "/" + escapeSegments(strings.Join(segs, "/"))
}

// escapeSegments escapes each slash-separated segment of p and rejoins them,
// preserving the slashes so nested content paths still address the right file.
func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// defaultMaxPages caps how many pages one listing walks so a pathological
// repository cannot consume the whole rate budget. An assessment reads a
// repository once and wants depth, so it raises this deliberately; a
// scheduled refresh should not.
const defaultMaxPages = 10

// get performs a GET request and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	_, err := c.getURL(ctx, endpoint, path, out)
	return err
}

// getAll fetches every page of a list endpoint by following the Link header,
// up to the client page cap. Pagination stays on the API host so the bearer token cannot be
// sent anywhere else. If the cap is reached while another page remains, it
// returns the partial results with ErrTruncated rather than passing them off as
// complete, so the caller can warn instead of indexing a busy repo as whole.
func getAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("github: base url: %w", err)
	}
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var all []T
	for page := 0; endpoint != ""; page++ {
		if page >= c.maxPages {
			return all, fmt.Errorf("github %s: %w at %d pages", path, ErrTruncated, c.maxPages)
		}
		var batch []T
		next, err := c.getURL(ctx, endpoint, path, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if next == "" {
			break
		}
		u, err := url.Parse(next)
		if err != nil || u.Host != base.Host {
			break
		}
		endpoint = next
	}
	return all, nil //nolint:nilerr // A malformed next link ends pagination, keeping fetched pages.
}

// getURL performs one GET against a full endpoint URL, decodes the JSON body
// into out, and returns the Link header's rel="next" URL when present. It
// retries on a secondary rate limit that sends Retry-After, and returns
// ErrRateLimited or ErrNotFound for those conditions. path labels errors.
func (c *Client) getURL(ctx context.Context, endpoint, path string, out any) (string, error) {
	resp, body, err := httputil.Do(ctx, c.http, c.maxRetries, githubRetryable, httputil.Get(ctx, endpoint, "Authorization", "Bearer "+c.token, "Accept", "application/vnd.github+json"))
	if errors.Is(err, httputil.ErrRateLimited) {
		return "", fmt.Errorf("github %s: %w", path, ErrRateLimited)
	}
	if err != nil {
		return "", fmt.Errorf("github %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("github %s: %w", path, ErrNotFound)
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		resp.Header.Get("X-RateLimit-Remaining") == "0":
		return "", fmt.Errorf("github %s: %w (resets at %s)", path, ErrRateLimited, resetTime(resp))
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("github %s: %w: %w", path, ErrStatus, &httputil.StatusError{Code: resp.StatusCode})
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("github %s: decode: %w", path, err)
	}
	return nextLink(resp.Header.Get("Link")), nil
}

// githubRetryable reports a retryable secondary rate limit: a 403 or 429 that
// carries a Retry-After header. A primary limit sends no Retry-After and is left
// to getURL to map to ErrRateLimited.
func githubRetryable(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	// A 429 is a secondary limit: short, and worth waiting out. So is a 403
	// carrying Retry-After, where GitHub has said how long to wait. httputil
	// applies a default backoff when the header is absent.
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if _, ok := httputil.RetryAfter(resp); ok {
		return true
	}
	// An exhausted primary quota is different and must not be retried. It
	// resets on a fixed clock that can be an hour away, so every retry fails
	// exactly as the first did, and burning the budget on them ends in a
	// generic rate-limit error. Returning false lets getURL report the reset
	// time instead, which is the one thing the caller can act on.
	return false
}

// nextLink extracts the rel="next" URL from a Link header, or empty.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		for _, param := range segs[1:] {
			if strings.TrimSpace(param) == `rel="next"` {
				return target
			}
		}
	}
	return ""
}

// resetTime formats the X-RateLimit-Reset header as a time, or "unknown".
func resetTime(resp *http.Response) string {
	v := resp.Header.Get("X-RateLimit-Reset")
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(secs, 0).UTC().Format(time.RFC3339)
	}
	return "unknown"
}
