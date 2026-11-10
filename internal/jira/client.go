// Package jira is a minimal Jira Cloud client scoped to what whotoask ingests:
// issues and the people on them. Credentials are held only in memory, never
// serialized, and never logged.
package jira

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
	"sync"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
	"github.com/dcadolph/whotoask/internal/util"
)

// API bases for the two Jira deployments. Cloud speaks v3 and returns rich text
// as a node tree; Server and Data Center speak v2 and return it as a string.
// Cloud also retired the offset /search endpoint in favor of the token-paged
// /search/jql, so the two deployments differ in the search path and its paging.
const (
	apiBaseCloud  = "/rest/api/3"
	apiBaseServer = "/rest/api/2"
)

// Client calls the Jira REST API for either a Cloud or a Server/Data Center
// deployment. The two differ in API version, authentication, and how they name
// users, all resolved at construction.
type Client struct {
	// baseURL is the site root, for example https://acme.atlassian.net.
	baseURL string
	// auth is the Authorization header value, or empty for an anonymous
	// connection to a public Server/Data Center site.
	auth string
	// searchPath is the version-appropriate issue search endpoint.
	searchPath string
	// tokenPaging selects the pagination style. Cloud's enhanced search pages by
	// an opaque nextPageToken and flags the final page; Server and Data Center
	// page by an offset against a total.
	tokenPaging bool
	// pingPath is the endpoint used to verify reachability and credentials.
	pingPath string
	// http performs requests.
	http httputil.Doer
	// userPath is the current-user endpoint, which carries the profile
	// timezone JQL dates are interpreted in.
	userPath string
	// loc is the user's JQL timezone, fetched once by UserLocation.
	loc *time.Location
	// locOnce guards the single fetch behind UserLocation.
	locOnce sync.Once
	// maxRetries bounds retries on HTTP 429.
	maxRetries int
	// progress, when set, is called after each page with the running count.
	progress util.Progress
}

// Option configures a Client.
type Option func(*Client)

// WithProgress sets a callback invoked after each page of a paging call with
// the running item count, so a long search can show movement.
func WithProgress(p util.Progress) Option {
	return func(c *Client) { c.progress = p }
}

// apiTimeout bounds one HTTP exchange so a hung server cannot stall a run.
const apiTimeout = 60 * time.Second

// New returns a Client for the site, authenticating with an email and API
// token. It panics if any argument is empty.
func New(baseURL, email, token string, opts ...Option) *Client {
	if baseURL == "" || email == "" || token == "" {
		panic("jira: New requires baseURL, email, and token")
	}
	c := &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		auth:        "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token)),
		searchPath:  apiBaseCloud + "/search/jql",
		tokenPaging: true,
		pingPath:    apiBaseCloud + "/myself",
		userPath:    apiBaseCloud + "/myself",
		http:        httputil.NewClient(apiTimeout),
		maxRetries:  3,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewServer returns a Client for a self-hosted Jira Server or Data Center site,
// which speaks the v2 API. token is a personal access token sent as a bearer,
// or empty for a public site that allows anonymous read, such as an open-source
// project's issue tracker. It panics on an empty baseURL.
func NewServer(baseURL, token string, opts ...Option) *Client {
	if baseURL == "" {
		panic("jira: NewServer requires a baseURL")
	}
	auth := ""
	if token != "" {
		auth = "Bearer " + token
	}
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		auth:       auth,
		searchPath: apiBaseServer + "/search",
		pingPath:   apiBaseServer + "/serverInfo",
		userPath:   apiBaseServer + "/myself",
		http:       httputil.NewClient(apiTimeout),
		maxRetries: 3,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// User is the subset of a Jira user whotoask reads.
type User struct {
	// AccountID is the stable account identifier on Jira Cloud.
	AccountID string `json:"accountId"`
	// Name is the username on Jira Server and Data Center, which have no
	// account id. It is the stable identity there.
	Name string `json:"name"`
	// Key is the user key on older Server and Data Center versions, used when
	// Name is absent.
	Key string `json:"key"`
	// DisplayName is the user's display name.
	DisplayName string `json:"displayName"`
	// EmailAddress is the user's email, present when the site exposes it. Many
	// Server and Data Center sites hide it for privacy.
	EmailAddress string `json:"emailAddress"`
}

// Identity returns the stable per-site identifier for the user, preferring the
// account id used by Cloud and falling back to the username or key used by
// Server and Data Center. It is empty only for a user with none of these.
func (u User) Identity() string {
	switch {
	case u.AccountID != "":
		return u.AccountID
	case u.Name != "":
		return u.Name
	default:
		return u.Key
	}
}

// Component is an issue component.
type Component struct {
	// Name is the component name.
	Name string `json:"name"`
}

// Issue is the subset of an issue whotoask reads.
type Issue struct {
	// Key is the issue key, for example PROJ-12.
	Key string `json:"key"`
	// Fields holds the issue fields.
	Fields struct {
		// Summary is the issue title.
		Summary string `json:"summary"`
		// Description is the issue description as Jira sends it, which on Cloud
		// is a rich-text node tree rather than a string. Use Description to read
		// it as words.
		Description json.RawMessage `json:"description"`
		// Assignee is the assigned user, if any.
		Assignee *User `json:"assignee"`
		// Reporter is the reporting user, if any.
		Reporter *User `json:"reporter"`
		// Components are the issue components.
		Components []Component `json:"components"`
		// Labels are the issue labels.
		Labels []string `json:"labels"`
		// Project is the issue's project.
		Project struct {
			// Key is the project key.
			Key string `json:"key"`
			// Name is the project name.
			Name string `json:"name"`
		} `json:"project"`
		// IssueType is the issue type.
		IssueType struct {
			// Name is the issue type name.
			Name string `json:"name"`
		} `json:"issuetype"`
		// Updated is the last update time in Jira's ISO 8601 format.
		Updated string `json:"updated"`
		// ResolutionDate is when the issue was resolved, empty when it is
		// still open.
		ResolutionDate string `json:"resolutiondate"`
		// Status is the issue's workflow status.
		Status struct {
			// Name is the status name, such as Done.
			Name string `json:"name"`
			// Category groups statuses; its key is "done" once the issue is
			// finished, whatever the workflow calls it.
			Category struct {
				// Key is the category key.
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		// Comment holds the issue comments, whose authors took part in working
		// the issue even when they were never the assignee or reporter.
		Comment struct {
			// Comments is the list of comments.
			Comments []struct {
				// Author is who wrote the comment.
				Author User `json:"author"`
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

// CommentAuthors returns the distinct users who commented on the issue. They
// helped settle it even when they were never assigned, which is exactly whom
// recall should surface and the assignee-and-reporter view misses.
func (i Issue) CommentAuthors() []User {
	authors := make([]User, 0, len(i.Fields.Comment.Comments))
	for _, c := range i.Fields.Comment.Comments {
		authors = append(authors, c.Author)
	}
	return util.Distinct(authors, User.Identity)
}

// Resolved reports whether the issue reached a finished state, which is what
// makes it a record of how something was settled rather than work in flight.
func (i Issue) Resolved() bool {
	return i.Fields.ResolutionDate != "" || i.Fields.Status.Category.Key == "done"
}

// Description returns the issue description as plain words, empty when the
// issue has none. It is what an issue says beyond its title, and is the text
// worth searching when someone asks how something was settled.
func (i Issue) Description() string { return adfText(i.Fields.Description) }

// BaseURL returns the site root, so a caller can build a link to an issue.
func (c *Client) BaseURL() string { return c.baseURL }

// offsetSearchResponse decodes the Server and Data Center search endpoint, which
// windows results by an offset against a total.
type offsetSearchResponse struct {
	// Issues is the page of issues.
	Issues []Issue `json:"issues"`
	// StartAt is the offset of this page.
	StartAt int `json:"startAt"`
	// Total is the total matching count.
	Total int `json:"total"`
}

// tokenSearchResponse decodes the Cloud enhanced-search endpoint, which pages by
// an opaque token and flags the final page rather than reporting a total.
type tokenSearchResponse struct {
	// Issues is the page of issues.
	Issues []Issue `json:"issues"`
	// NextPageToken is the cursor for the next page, empty on the last page.
	NextPageToken string `json:"nextPageToken"`
	// IsLast reports whether this is the final page.
	IsLast bool `json:"isLast"`
}

// searchFields are the issue fields Search asks for. Jira returns only the
// fields named here, so every field any caller reads must appear: omitting
// resolutiondate or status silently makes Resolved report false for every
// issue, and no issue ever becomes an episode.
const searchFields = "summary,assignee,reporter,components,labels,project," +
	"issuetype,updated,resolutiondate,status,description,comment"

// Search returns up to max issues matching jql, in pages of 100. A non-positive
// max returns all matches. Cloud and Server paginate differently, so it
// dispatches on the deployment fixed at construction. When max cuts the read
// short while more issues matched, the issues read so far are returned along
// with ErrTruncated, so a caller can warn rather than index a partial tracker
// as whole.
func (c *Client) Search(ctx context.Context, jql string, max int) ([]Issue, error) {
	if c.tokenPaging {
		return c.searchToken(ctx, jql, max)
	}
	return c.searchOffset(ctx, jql, max)
}

// searchToken pages the Cloud enhanced-search endpoint, which returns an opaque
// nextPageToken and an isLast flag instead of an offset and total. Atlassian
// retired the offset endpoint from Cloud, so this is the only search there.
func (c *Client) searchToken(ctx context.Context, jql string, max int) ([]Issue, error) {
	var all []Issue
	for token := ""; ; {
		page := 100
		if max > 0 && max-len(all) < page {
			page = max - len(all)
		}
		if page <= 0 {
			break
		}
		params := url.Values{
			"jql":        {jql},
			"maxResults": {strconv.Itoa(page)},
			"fields":     {searchFields},
		}
		if token != "" {
			params.Set("nextPageToken", token)
		}
		var resp tokenSearchResponse
		if err := c.get(ctx, c.searchPath, params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Issues...)
		c.progress.Report(len(all))
		if resp.IsLast || resp.NextPageToken == "" || len(resp.Issues) == 0 {
			break
		}
		token = resp.NextPageToken
		if max > 0 && len(all) >= max {
			// The endpoint reports no total, so how much was left is unknown;
			// that more matched is certain, since this page was not the last.
			return all, fmt.Errorf("%w after %d issues, with more matching", ErrTruncated, len(all))
		}
	}
	return all, nil
}

// searchOffset pages the Server and Data Center search endpoint, which windows
// results by an offset against a total.
func (c *Client) searchOffset(ctx context.Context, jql string, max int) ([]Issue, error) {
	var all []Issue
	for startAt := 0; ; {
		page := 100
		if max > 0 && max-len(all) < page {
			page = max - len(all)
		}
		if page <= 0 {
			break
		}
		params := url.Values{
			"jql":        {jql},
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(page)},
			"fields":     {searchFields},
		}
		var resp offsetSearchResponse
		if err := c.get(ctx, c.searchPath, params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Issues...)
		c.progress.Report(len(all))
		startAt += len(resp.Issues)
		if len(resp.Issues) == 0 || startAt >= resp.Total {
			break
		}
		if max > 0 && len(all) >= max {
			return all, fmt.Errorf("%w after %d of %d matching issues", ErrTruncated, len(all), resp.Total)
		}
	}
	return all, nil
}

// UserLocation returns the timezone Jira interprets JQL date literals in: the
// authenticated user's profile timezone, read once from the current-user
// endpoint and cached. JQL has no timezone syntax, so a watermark formatted in
// any other zone shifts the incremental window by the whole offset. It returns
// nil when the timezone cannot be learned, such as an anonymous connection or
// a name the zone database does not know, and the caller keeps its fallback.
func (c *Client) UserLocation(ctx context.Context) *time.Location {
	c.locOnce.Do(func() {
		var me struct {
			TimeZone string `json:"timeZone"`
		}
		if err := c.get(ctx, c.userPath, url.Values{}, &me); err != nil || me.TimeZone == "" {
			return
		}
		if loc, err := time.LoadLocation(me.TimeZone); err == nil {
			c.loc = loc
		}
	})
	return c.loc
}

// Ping verifies the site is reachable and, when authenticated, that the
// credentials work. It uses the current-user endpoint for an authenticated
// connection and the public server-info endpoint for an anonymous one. It
// returns nil on success, a *httputil.StatusError for a non-200 status such as
// 401 for bad credentials, or the transport error when the site is unreachable.
func (c *Client) Ping(ctx context.Context) error {
	endpoint := c.baseURL + c.pingPath
	resp, _, err := httputil.Do(ctx, c.http, c.maxRetries, nil, httputil.Get(ctx, endpoint, "Authorization", c.auth, "Accept", "application/json"))
	if errors.Is(err, httputil.ErrRateLimited) {
		return fmt.Errorf("jira ping: %w", ErrRateLimited)
	}
	if err != nil {
		return fmt.Errorf("jira ping: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &httputil.StatusError{Code: resp.StatusCode}
	}
	return nil
}

// get performs a GET request and decodes the JSON body into out, retrying on
// HTTP 429 up to maxRetries.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	endpoint := c.baseURL + path + "?" + params.Encode()
	return failure(path, httputil.GetJSON(ctx, c.http, c.maxRetries, endpoint, out,
		"Authorization", c.auth, "Accept", "application/json"))
}

// failure names this package in an error from a shared helper, and maps it onto
// the sentinels callers match against.
func failure(label string, err error) error {
	var status *httputil.StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, httputil.ErrRateLimited):
		return fmt.Errorf("jira %s: %w", label, ErrRateLimited)
	case errors.As(err, &status):
		return fmt.Errorf("jira %s: %w: %w", label, ErrStatus, status)
	default:
		return fmt.Errorf("jira %s: %w", label, err)
	}
}
