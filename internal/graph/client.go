package graph

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/httputil"
)

const (
	// defaultBaseURL is the public Microsoft Graph root. A sovereign or national
	// cloud tenant overrides it with WithBaseURL.
	defaultBaseURL = "https://graph.microsoft.com"
	// apiTimeout bounds one HTTP exchange so a hung server cannot stall a run.
	apiTimeout = 60 * time.Second
)

// User is a directory user with the reporting line, the manager reference no
// code crawl can supply.
type User struct {
	// ID is the stable directory object id.
	ID string `json:"id"`
	// DisplayName is the person's name.
	DisplayName string `json:"displayName"`
	// Mail is the primary email, when set.
	Mail string `json:"mail"`
	// UserPrincipalName is the sign-in name, used as email when Mail is empty.
	UserPrincipalName string `json:"userPrincipalName"`
	// JobTitle is the person's title.
	JobTitle string `json:"jobTitle"`
	// Department is the person's department, used as the team.
	Department string `json:"department"`
	// Manager is the expanded manager reference, nil for an org root.
	Manager *struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	} `json:"manager"`
}

// Email returns the user's best email: the primary mail, else the sign-in name.
func (u User) Email() string {
	if u.Mail != "" {
		return u.Mail
	}
	return u.UserPrincipalName
}

// ManagerEmail returns the manager's best email, or empty for an org root.
func (u User) ManagerEmail() string {
	if u.Manager == nil {
		return ""
	}
	if u.Manager.Mail != "" {
		return u.Manager.Mail
	}
	return u.Manager.UserPrincipalName
}

// Client reads users from Microsoft Graph.
type Client struct {
	// token is the bearer token; it is never serialized or logged.
	token string
	// baseURL is the Graph root, overridable for a sovereign cloud or tests.
	baseURL string
	// http performs requests.
	http httputil.Doer
	// maxRetries bounds retries on HTTP 429.
	maxRetries int
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP doer, for tests.
func WithHTTPClient(d httputil.Doer) Option {
	return func(c *Client) {
		if d != nil {
			c.http = d
		}
	}
}

// WithBaseURL overrides the Graph root, for a sovereign or national cloud.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// New returns a Client for token. It panics on an empty token; callers validate
// token presence before constructing the client.
func New(token string, opts ...Option) *Client {
	if token == "" {
		panic("graph: New requires a non-empty token")
	}
	c := &Client{token: token, baseURL: defaultBaseURL, http: httputil.NewClient(apiTimeout), maxRetries: 3}
	for _, o := range opts {
		o(c)
	}
	return c
}

// page is one page of a Graph collection response.
type page struct {
	// Value holds the users on this page.
	Value []User `json:"value"`
	// NextLink is the URL of the next page, empty on the last.
	NextLink string `json:"@odata.nextLink"`
}

// maxUserPages bounds how many nextLink pages one listing may follow. At a
// hundred users a page it allows a million, past any real tenant, so hitting
// it means the cursor chain is looping rather than ending, and the read fails
// loudly instead of spinning.
const maxUserPages = 10000

// Users lists every user with their manager, following pagination. A nextLink
// is followed only while it stays on the Graph host, because the request
// carries the bearer token and the link is server-supplied: a hostile value
// would otherwise send the credential wherever it pointed. A link that leaves
// the host, or a chain that never ends, fails the read with ErrPagination
// rather than quietly returning part of the directory.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("graph: base url: %w", err)
	}
	params := url.Values{}
	params.Set("$select", "id,displayName,mail,userPrincipalName,jobTitle,department")
	params.Set("$expand", "manager($select=mail,userPrincipalName)")
	params.Set("$top", "100")
	next := c.baseURL + "/v1.0/users?" + params.Encode()

	var users []User
	for pages := 0; next != ""; pages++ {
		if pages >= maxUserPages {
			return nil, fmt.Errorf("graph: %w: users listing still paging after %d pages",
				ErrPagination, pages)
		}
		var p page
		if err := c.getURL(ctx, next, &p); err != nil {
			return nil, err
		}
		users = append(users, p.Value...)
		next = p.NextLink
		if next == "" {
			break
		}
		u, err := url.Parse(next)
		if err != nil || u.Host != base.Host {
			host := "an unparseable URL"
			if err == nil {
				host = u.Host
			}
			return nil, fmt.Errorf("graph: %w: nextLink points at %s, where the token must not be sent",
				ErrPagination, host)
		}
	}
	return users, nil
}

// Ping reads a single user to confirm the token and endpoint work.
func (c *Client) Ping(ctx context.Context) error {
	var p page
	return c.getURL(ctx, c.baseURL+"/v1.0/users?$top=1", &p)
}

// getURL performs one GET against a full URL and decodes the body into out. It
// takes a full URL so a Graph nextLink is followed as-is.
func (c *Client) getURL(ctx context.Context, endpoint string, out any) error {
	return failure(httputil.GetJSON(ctx, c.http, c.maxRetries, endpoint, out,
		"Authorization", "Bearer "+c.token, "Accept", "application/json"))
}

// failure names this package in an error from a shared helper, and maps it onto
// the sentinels callers match against.
func failure(err error) error {
	var status *httputil.StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, httputil.ErrRateLimited):
		return fmt.Errorf("graph: %w", ErrRateLimited)
	case errors.As(err, &status):
		return fmt.Errorf("graph: %w: %w", ErrStatus, status)
	default:
		return fmt.Errorf("graph: %w", err)
	}
}
