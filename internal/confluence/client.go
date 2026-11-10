// Package confluence is a minimal Confluence Cloud client scoped to what whotoask
// ingests: pages and the people who wrote them. Credentials are held only in
// memory, never serialized, and never logged.
package confluence

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
	"github.com/dcadolph/whotoask/internal/util"
)

// API bases for the two Confluence deployments. Cloud serves the REST API under
// a /wiki context path; Server and Data Center serve it at the site root. Cloud
// reads pages through the v2 API, which enumerates content directly instead of
// through the search index the deprecated v1 content search depended on.
const (
	apiBaseCloud  = "/wiki/rest/api"
	apiBaseServer = "/rest/api"
	apiV2Cloud    = "/wiki/api/v2"
)

// labelWorkers bounds the concurrent per-page label lookups the v2 path makes,
// since v2 does not return labels inline the way the v1 search did.
const labelWorkers = 8

// Client calls the Confluence REST API for either a Cloud or a Server/Data
// Center deployment, which differ in the API context path, authentication, and
// how they name users.
type Client struct {
	// baseURL is the site root, for example https://acme.atlassian.net.
	baseURL string
	// auth is the Authorization header value, or empty for an anonymous
	// connection to a public Server/Data Center site.
	auth string
	// cloud reads pages through the Cloud v2 API; when false the client is a
	// Server or Data Center connection that reads through the v1 content search.
	cloud bool
	// searchPath is the v1 content search endpoint, used by Server and Data
	// Center and by Cloud only when the caller supplies a raw CQL query.
	searchPath string
	// userPath is the current-user endpoint, which carries the profile
	// timezone CQL dates are interpreted in.
	userPath string
	// loc is the user's CQL timezone, fetched once by userLocation.
	loc *time.Location
	// locOnce guards the single fetch behind userLocation.
	locOnce sync.Once
	// pingPath verifies reachability and, when authenticated, credentials.
	pingPath string
	// http performs requests.
	http httputil.Doer
	// maxRetries bounds retries on HTTP 429.
	maxRetries int
	// progress, when set, is called after each page with the running count.
	progress util.Progress
}

// Option configures a Client.
type Option func(*Client)

// WithProgress sets a callback invoked after each page with the running item
// count, so a long fetch can show movement.
func WithProgress(p util.Progress) Option {
	return func(c *Client) { c.progress = p }
}

// apiTimeout bounds one HTTP exchange so a hung server cannot stall a run.
const apiTimeout = 60 * time.Second

// New returns a Client for the site, authenticating with an email and API
// token. It panics if any argument is empty.
func New(siteURL, email, token string, opts ...Option) *Client {
	if siteURL == "" || email == "" || token == "" {
		panic("confluence: New requires siteURL, email, and token")
	}
	c := &Client{
		baseURL:    strings.TrimRight(siteURL, "/"),
		auth:       "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token)),
		cloud:      true,
		searchPath: apiBaseCloud + "/content/search",
		userPath:   apiBaseCloud + "/user/current",
		pingPath:   apiBaseCloud + "/user/current",
		http:       httputil.NewClient(apiTimeout),
		maxRetries: 3,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewServer returns a Client for a self-hosted Confluence Server or Data Center
// site, which serves the REST API at the site root rather than under /wiki.
// token is a personal access token sent as a bearer, or empty for a public
// site that allows anonymous read, such as an open-source project's wiki. It
// panics on an empty siteURL.
func NewServer(siteURL, token string, opts ...Option) *Client {
	if siteURL == "" {
		panic("confluence: NewServer requires a siteURL")
	}
	auth := ""
	if token != "" {
		auth = "Bearer " + token
	}
	c := &Client{
		baseURL:    strings.TrimRight(siteURL, "/"),
		auth:       auth,
		searchPath: apiBaseServer + "/content/search",
		userPath:   apiBaseServer + "/user/current",
		pingPath:   apiBaseServer + "/space?limit=1",
		http:       httputil.NewClient(apiTimeout),
		maxRetries: 3,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// User is the subset of a Confluence user whotoask reads.
type User struct {
	// AccountID is the stable account identifier on Confluence Cloud.
	AccountID string `json:"accountId"`
	// Username is the login on Confluence Server and Data Center, which have no
	// account id.
	Username string `json:"username"`
	// UserKey is the internal user key on Server and Data Center, used when
	// Username is absent.
	UserKey string `json:"userKey"`
	// DisplayName is the user's display name.
	DisplayName string `json:"displayName"`
	// Email is the user's email, present when the site exposes it. Server and
	// Data Center sites usually hide it.
	Email string `json:"email"`
}

// Identity returns the stable per-site identifier for the user, preferring the
// account id used by Cloud and falling back to the username or key used by
// Server and Data Center. It is empty only for a user with none of these.
func (u User) Identity() string {
	switch {
	case u.AccountID != "":
		return u.AccountID
	case u.Username != "":
		return u.Username
	default:
		return u.UserKey
	}
}

// label is a content label.
type label struct {
	// Name is the label text.
	Name string `json:"name"`
}

// Page is the subset of a content page whotoask reads.
type Page struct {
	// Title is the page title.
	Title string `json:"title"`
	// Space is the page's space.
	Space struct {
		// Key is the space key.
		Key string `json:"key"`
		// Name is the space name.
		Name string `json:"name"`
	} `json:"space"`
	// Metadata holds the page labels.
	Metadata struct {
		// Labels are the page labels.
		Labels struct {
			// Results is the label list.
			Results []label `json:"results"`
		} `json:"labels"`
	} `json:"metadata"`
	// History holds the page creator and creation time.
	History struct {
		// CreatedBy is the page's creator.
		CreatedBy *User `json:"createdBy"`
		// CreatedAt is when the page was created.
		CreatedAt time.Time `json:"createdDate"`
	} `json:"history"`
	// Version holds the last editor and edit time.
	Version struct {
		// By is the last editor.
		By *User `json:"by"`
		// When is the last edit time.
		When time.Time `json:"when"`
	} `json:"version"`
	// Body holds the page content in Confluence's storage format, present when
	// the read asked for it. It is where the substance of a page lives.
	Body struct {
		// Storage is the storage-format representation.
		Storage struct {
			// Value is the XHTML storage markup.
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

// BodyText returns the page body as plain words, with the storage-format markup
// stripped, empty when the read did not fetch the body. It is the substance a
// page carries beyond its title, the text worth mining for who knows what.
func (p Page) BodyText() string { return stripHTML(p.Body.Storage.Value) }

// stripHTML removes tags from Confluence storage markup and unescapes entities,
// leaving the words. Tag and macro names must not survive into topics, so a
// scan that drops everything between angle brackets is enough for indexing.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
			b.WriteByte(' ')
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return html.UnescapeString(b.String())
}

// LabelNames returns the page's label names.
func (p Page) LabelNames() []string {
	out := make([]string, 0, len(p.Metadata.Labels.Results))
	for _, l := range p.Metadata.Labels.Results {
		out = append(out, l.Name)
	}
	return out
}

// Authors returns the distinct creator and last editor of the page, keyed by
// whatever identity the deployment provides. Keying by account id alone
// dropped every author on Server and Data Center, where users carry a
// username and no account id, so self-hosted pages credited nobody.
func (p Page) Authors() []*User {
	return util.Distinct([]*User{p.History.CreatedBy, p.Version.By}, func(u *User) string {
		if u == nil {
			return ""
		}
		return u.Identity()
	})
}

// Query describes which pages to read. It lets the client choose the transport:
// a raw CQL string keeps the legacy search path, while a space scope drives the
// Cloud v2 enumeration that has no search-index dependency.
type Query struct {
	// Spaces scopes the read to these space keys; empty means every space.
	Spaces []string
	// CQL, when set, overrides Spaces with a raw Confluence Query Language
	// string. Cloud honors it through the legacy content search.
	CQL string
	// Max caps the number of pages returned; non-positive returns all.
	Max int
	// Since, when set, limits the read to pages last modified at or after it,
	// oldest first, for an incremental re-index. It forces the CQL search path on
	// Cloud, since the v2 enumeration cannot filter by modification time.
	Since time.Time
}

// Pages returns the pages the query selects. Server and Data Center always read
// through the v1 content search; Cloud reads through the v2 API unless the
// caller supplied a raw CQL string, which only the search endpoint understands.
func (c *Client) Pages(ctx context.Context, q Query) ([]Page, error) {
	// The v2 enumeration cannot filter by modification time, so an incremental
	// read (Since set) always takes the CQL search path even on Cloud.
	if c.cloud && strings.TrimSpace(q.CQL) == "" && q.Since.IsZero() {
		return c.enumerateV2(ctx, q.Spaces, q.Max)
	}
	// An incremental read formats its watermark as a wall-clock the server
	// reads in the user's profile timezone, so convert the instant into that
	// zone first. Failing to learn the zone leaves the time as given, which is
	// the old behavior.
	if !q.Since.IsZero() {
		if loc := c.userLocation(ctx); loc != nil {
			q.Since = q.Since.In(loc)
		}
	}
	return c.searchCQL(ctx, buildCQL(q), q.Max)
}

// buildCQL renders a query as a CQL string: an explicit override, or a space
// scope, or every page.
func buildCQL(q Query) string {
	if s := strings.TrimSpace(q.CQL); s != "" {
		return s
	}
	cql := "type = page"
	if len(q.Spaces) > 0 {
		quoted := make([]string, len(q.Spaces))
		for i, s := range q.Spaces {
			quoted[i] = `"` + s + `"`
		}
		cql += " and space in (" + strings.Join(quoted, ",") + ")"
	}
	// Incremental: restrict to pages modified since the watermark, oldest first,
	// so a capped read leaves the newest pages for the next run and never skips.
	if !q.Since.IsZero() {
		cql += fmt.Sprintf(` and lastmodified >= "%s" order by lastmodified asc`, confluenceCQLTime(q.Since))
	}
	return cql
}

// confluenceCQLTime formats t as a CQL absolute timestamp in t's own location,
// backed off by a small margin so minor clock skew re-reads a little rather
// than skips. The caller converts t into the timezone Confluence interprets
// CQL dates in; the margin only has to cover clock drift, never a zone offset.
func confluenceCQLTime(t time.Time) string {
	return t.Add(-2 * time.Minute).Format("2006/01/02 15:04")
}

// userLocation returns the timezone Confluence interprets CQL date literals in:
// the authenticated user's profile timezone, read once from the current-user
// endpoint and cached. CQL has no timezone syntax, so a watermark formatted in
// any other zone shifts the incremental window by the whole offset. It returns
// nil when the timezone cannot be learned, and the caller keeps the time as it
// was given.
func (c *Client) userLocation(ctx context.Context) *time.Location {
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

// searchResponse decodes the v1 content search endpoint.
type searchResponse struct {
	// Results is the page of content.
	Results []Page `json:"results"`
	// Size is the number returned in this page.
	Size int `json:"size"`
	// Limit is the requested page size.
	Limit int `json:"limit"`
	// Links carries the cursor to the next page. Its presence, not the page
	// size, is what says whether more results exist: permission filtering
	// removes results after the window is cut, so a short or even empty page
	// can sit in the middle of a longer result set.
	Links struct {
		// Next is the relative URL of the next page, empty on the last.
		Next string `json:"next"`
	} `json:"_links"`
}

// maxSearchRequests bounds how many result windows one search may walk when no
// caller cap applies, so a server that keeps promising another page cannot
// hold the read in a loop forever. At a hundred results per window it allows a
// hundred thousand pages, far past any real read, and hitting it is reported
// as a truncation rather than passed off as completion.
const maxSearchRequests = 1000

// searchCQL reads pages through the v1 content search, paginating in pages of
// 100. A non-positive max returns all matches. It expands the space, labels,
// creator, and last editor inline, which is what the v2 path reassembles by
// hand from separate endpoints. A read cut short by max or by the request
// bound returns what it has alongside ErrTruncated, so the caller can warn
// rather than index a partial wiki as whole.
func (c *Client) searchCQL(ctx context.Context, cql string, max int) ([]Page, error) {
	var all []Page
	for start, requests := 0, 0; ; requests++ {
		if requests >= maxSearchRequests {
			return all, fmt.Errorf("%w after %d result windows", ErrTruncated, requests)
		}
		limit := 100
		if max > 0 && max-len(all) < limit {
			limit = max - len(all)
		}
		if limit <= 0 {
			break
		}
		params := url.Values{
			"cql":    {cql},
			"start":  {strconv.Itoa(start)},
			"limit":  {strconv.Itoa(limit)},
			"expand": {"space,metadata.labels,history,version,body.storage"},
		}
		var resp searchResponse
		if err := c.get(ctx, c.searchPath, params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Results...)
		c.progress.Report(len(all))
		// The window advances by what was asked for, not by what came back:
		// permission filtering shrinks a page after the window is cut, so a
		// short page does not mean the results are over, and advancing by the
		// filtered size would re-read rows. The server says whether more exist
		// through the next link; a response without one falls back to the size
		// heuristic for servers old enough to omit it.
		start += limit
		if resp.Links.Next == "" && (resp.Size == 0 || resp.Size < limit) {
			break
		}
		if max > 0 && len(all) >= max {
			return all, fmt.Errorf("%w after %d pages read", ErrTruncated, len(all))
		}
	}
	return all, nil
}

// v2Page decodes one page from the Cloud v2 pages endpoint, which names people
// by account id only and carries neither the space name nor the labels inline.
type v2Page struct {
	// ID is the page id, needed to look up its labels.
	ID string `json:"id"`
	// Title is the page title.
	Title string `json:"title"`
	// SpaceID is the numeric id of the page's space.
	SpaceID string `json:"spaceId"`
	// AuthorID is the account id of the page's creator.
	AuthorID string `json:"authorId"`
	// CreatedAt is when the page was created.
	CreatedAt time.Time `json:"createdAt"`
	// Version holds the last edit.
	Version struct {
		// AuthorID is the account id of the last editor.
		AuthorID string `json:"authorId"`
		// CreatedAt is when the last edit happened.
		CreatedAt time.Time `json:"createdAt"`
	} `json:"version"`
	// Body holds the storage-format content when the read asked for it.
	Body struct {
		// Storage is the storage-format representation.
		Storage struct {
			// Value is the XHTML storage markup.
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

// v2Links carries the cursor to the next page of a v2 list as a relative URL.
type v2Links struct {
	// Next is the relative URL of the next page, empty on the last page.
	Next string `json:"next"`
}

// v2PageList decodes a page of the v2 pages endpoint.
type v2PageList struct {
	// Results is the page of pages.
	Results []v2Page `json:"results"`
	// Links carries the cursor to the next page.
	Links v2Links `json:"_links"`
}

// v2Space decodes a space from the v2 spaces endpoint.
type v2Space struct {
	// ID is the numeric space id.
	ID string `json:"id"`
	// Key is the space key.
	Key string `json:"key"`
	// Name is the space name.
	Name string `json:"name"`
}

// v2SpaceList decodes a page of the v2 spaces endpoint.
type v2SpaceList struct {
	// Results is the page of spaces.
	Results []v2Space `json:"results"`
}

// v2LabelList decodes the v2 per-page labels endpoint.
type v2LabelList struct {
	// Results is the page's labels.
	Results []label `json:"results"`
}

// enumerateV2 reads pages through the Cloud v2 API. It resolves the requested
// space keys to ids, pages through the content by cursor, then fills in the
// space names, the creator and last-editor identities, and the labels that v2,
// unlike the old search, does not return inline.
func (c *Client) enumerateV2(ctx context.Context, spaceKeys []string, max int) ([]Page, error) {
	spaces := make(map[string]v2Space) // keyed by space id
	var spaceIDs []string
	for _, key := range spaceKeys {
		sp, err := c.spaceByKey(ctx, key)
		if err != nil {
			return nil, err
		}
		if sp.ID == "" {
			continue // unknown space key: nothing to read
		}
		spaces[sp.ID] = sp
		spaceIDs = append(spaceIDs, sp.ID)
	}

	raw, err := c.pageV2(ctx, spaceIDs, max)
	if err != nil && !errors.Is(err, ErrTruncated) {
		return nil, err
	}
	// A truncation still enriches and returns what was read; the sentinel
	// rides along so the caller can warn about the rest.
	truncated := err

	users := make(map[string]*User) // account id to resolved user
	resolve := func(id string) (*User, error) {
		if id == "" {
			return nil, nil
		}
		if u, ok := users[id]; ok {
			return u, nil
		}
		u, err := c.userByAccountID(ctx, id)
		if err != nil {
			return nil, err
		}
		users[id] = u
		return u, nil
	}

	labels, err := c.labelsFor(ctx, raw)
	if err != nil {
		return nil, err
	}

	pages := make([]Page, 0, len(raw))
	for i, r := range raw {
		sp, ok := spaces[r.SpaceID]
		if !ok {
			got, err := c.spaceByID(ctx, r.SpaceID)
			if err != nil {
				return nil, err
			}
			spaces[r.SpaceID] = got
			sp = got
		}
		creator, err := resolve(r.AuthorID)
		if err != nil {
			return nil, err
		}
		editor, err := resolve(r.Version.AuthorID)
		if err != nil {
			return nil, err
		}
		var p Page
		p.Title = r.Title
		p.Space.Key = sp.Key
		p.Space.Name = sp.Name
		p.Metadata.Labels.Results = labels[i]
		p.History.CreatedBy = creator
		p.History.CreatedAt = r.CreatedAt
		p.Version.By = editor
		p.Version.When = r.Version.CreatedAt
		p.Body.Storage.Value = r.Body.Storage.Value
		pages = append(pages, p)
	}
	return pages, truncated
}

// pageV2 pages through the v2 pages endpoint, following the cursor until the max
// is reached or the pages run out. An empty spaceIDs reads every space. A read
// max cut short while more pages remained returns the partial set alongside
// ErrTruncated.
func (c *Client) pageV2(ctx context.Context, spaceIDs []string, max int) ([]v2Page, error) {
	params := url.Values{"limit": {"100"}, "body-format": {"storage"}}
	for _, id := range spaceIDs {
		params.Add("space-id", id)
	}
	next := apiV2Cloud + "/pages?" + params.Encode()
	var all []v2Page
	for next != "" {
		var list v2PageList
		if err := c.getRaw(ctx, next, &list); err != nil {
			return nil, err
		}
		all = append(all, list.Results...)
		c.progress.Report(len(all))
		if max > 0 && len(all) >= max {
			more := len(all) > max || list.Links.Next != ""
			all = all[:max]
			if more {
				return all, fmt.Errorf("%w after %d pages read", ErrTruncated, len(all))
			}
			break
		}
		next = list.Links.Next
		// The cursor comes from the server, and getRaw appends it to baseURL, so a
		// hostile value such as "@evil.com/x" would send the credentialed request
		// to another host. Refuse to follow a cursor that leaves the site's host.
		if next != "" && !c.sameHost(next) {
			break
		}
	}
	return all, nil
}

// sameHost reports whether following next, which getRaw appends to baseURL,
// stays on the site's own host, so a server-supplied cursor cannot redirect the
// authenticated request elsewhere.
func (c *Client) sameHost(next string) bool {
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	u, err := url.Parse(c.baseURL + next)
	return err == nil && u.Host == base.Host
}

// labelsFor fetches each page's labels concurrently, since v2 serves them from a
// per-page endpoint rather than inline. The result is aligned with pages by
// index. Bounded workers keep a large space from opening a request per page all
// at once.
func (c *Client) labelsFor(ctx context.Context, pages []v2Page) ([][]label, error) {
	out := make([][]label, len(pages))
	errs := make([]error, len(pages))
	sem := make(chan struct{}, labelWorkers)
	var wg sync.WaitGroup
	for i := range pages {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			var list v2LabelList
			if err := c.getRaw(ctx, apiV2Cloud+"/pages/"+pages[i].ID+"/labels", &list); err != nil {
				errs[i] = err
				return
			}
			out[i] = list.Results
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// spaceByKey resolves a space key to its id and name. An unknown key yields a
// zero v2Space, which the caller skips.
func (c *Client) spaceByKey(ctx context.Context, key string) (v2Space, error) {
	var list v2SpaceList
	if err := c.getRaw(ctx, apiV2Cloud+"/spaces?keys="+url.QueryEscape(key), &list); err != nil {
		return v2Space{}, err
	}
	if len(list.Results) == 0 {
		return v2Space{}, nil
	}
	return list.Results[0], nil
}

// spaceByID resolves a numeric space id to its key and name, for a page whose
// space was not among the requested keys.
func (c *Client) spaceByID(ctx context.Context, id string) (v2Space, error) {
	var sp v2Space
	if err := c.getRaw(ctx, apiV2Cloud+"/spaces/"+id, &sp); err != nil {
		return v2Space{}, err
	}
	return sp, nil
}

// userByAccountID resolves an account id to a display name and, when the site
// exposes it, an email. Cloud v2 content names people by account id only, so
// the identity comes from the user endpoint the old search expanded inline.
func (c *Client) userByAccountID(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.getRaw(ctx, apiBaseCloud+"/user?accountId="+url.QueryEscape(id), &u); err != nil {
		return nil, err
	}
	if u.AccountID == "" {
		u.AccountID = id
	}
	return &u, nil
}

// Ping verifies the site is reachable and, when authenticated, that the
// credentials work. Cloud checks the current user; an anonymous Server
// connection checks a space listing, since the current-user endpoint needs
// auth. It returns nil on success, a *httputil.StatusError for a non-200
// status, or the transport error when the site is unreachable.
func (c *Client) Ping(ctx context.Context) error {
	endpoint := c.baseURL + c.pingPath
	resp, _, err := httputil.Do(ctx, c.http, c.maxRetries, nil, httputil.Get(ctx, endpoint, "Authorization", c.auth, "Accept", "application/json"))
	if errors.Is(err, httputil.ErrRateLimited) {
		return fmt.Errorf("confluence ping: %w", ErrRateLimited)
	}
	if err != nil {
		return fmt.Errorf("confluence ping: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &httputil.StatusError{Code: resp.StatusCode}
	}
	return nil
}

// get performs a GET against path with the given query parameters and decodes
// the JSON body into out.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	return c.getRaw(ctx, path+"?"+params.Encode(), out)
}

// getRaw performs a GET against a path that already carries its query string,
// such as a v2 cursor link, and decodes the JSON body into out, retrying on HTTP
// 429 up to maxRetries.
func (c *Client) getRaw(ctx context.Context, pathWithQuery string, out any) error {
	label := pathWithQuery
	if i := strings.IndexByte(label, '?'); i >= 0 {
		label = label[:i]
	}
	endpoint := c.baseURL + pathWithQuery
	if err := failure(label, httputil.GetJSON(ctx, c.http, c.maxRetries, endpoint, out,
		"Authorization", c.auth, "Accept", "application/json")); err != nil {
		return err
	}
	return nil
}

// failure names this package in an error from a shared helper, and maps it onto
// the sentinels callers match against.
func failure(label string, err error) error {
	var status *httputil.StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, httputil.ErrRateLimited):
		return fmt.Errorf("confluence %s: %w", label, ErrRateLimited)
	case errors.As(err, &status):
		return fmt.Errorf("confluence %s: %w: %w", label, ErrStatus, status)
	default:
		return fmt.Errorf("confluence %s: %w", label, err)
	}
}
