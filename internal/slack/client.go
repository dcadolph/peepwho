// Package slack is a minimal Slack Web API client scoped to what whotoask needs
// to ingest: workspace users, channels, and message history. The token is held
// only in memory, never serialized, and never logged.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
)

// defaultBaseURL is the Slack Web API root.
const defaultBaseURL = "https://slack.com/api"

// Client calls the Slack Web API.
type Client struct {
	// token is the bearer token; it is never serialized or logged.
	token string
	// baseURL is the API root, overridable for tests.
	baseURL string
	// http performs requests.
	http httputil.Doer
	// maxRetries bounds retries for the quick list and auth calls. An HTTP 429
	// is tried again, and so is a 500, 502, 503, or 504 on a method that is
	// safe to repeat.
	maxRetries int
	// historyRetries bounds the same retries for conversations.history and
	// conversations.replies. Slack throttles these two methods harder than the
	// rest, and since 2025 drops commercially distributed apps to roughly one
	// call per minute. An app a workspace builds for itself keeps the higher
	// tier, but the budget stays large so a busy channel still makes progress
	// under either limit rather than giving up after a few throttles.
	historyRetries int
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP doer.
func WithHTTPClient(d httputil.Doer) Option {
	return func(c *Client) {
		if d != nil {
			c.http = d
		}
	}
}

// WithBaseURL overrides the API base URL.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = u
		}
	}
}

// apiTimeout bounds one HTTP exchange so a hung server cannot stall a run.
const apiTimeout = 60 * time.Second

// New returns a Client for token. It panics on an empty token; callers validate
// token presence before constructing the client.
func New(token string, opts ...Option) *Client {
	if token == "" {
		panic("slack: New requires a non-empty token")
	}
	c := &Client{
		token: token, baseURL: defaultBaseURL, http: httputil.NewClient(apiTimeout),
		maxRetries: 3, historyRetries: 10,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Profile holds the subset of a Slack user profile whotoask uses.
type Profile struct {
	// RealName is the user's display name.
	RealName string `json:"real_name"`
	// Email is the user's email, present only with the users:read.email scope.
	Email string `json:"email"`
	// Title is the user's profile title.
	Title string `json:"title"`
}

// User is a Slack workspace member.
type User struct {
	// ID is the Slack user ID.
	ID string `json:"id"`
	// Name is the Slack handle.
	Name string `json:"name"`
	// Deleted reports whether the account is deactivated.
	Deleted bool `json:"deleted"`
	// IsBot reports whether the account is a bot.
	IsBot bool `json:"is_bot"`
	// Profile holds name, email, and title.
	Profile Profile `json:"profile"`
}

// value wraps a Slack topic or purpose object.
type value struct {
	// Value is the text content.
	Value string `json:"value"`
}

// Channel is a Slack conversation.
type Channel struct {
	// ID is the channel ID.
	ID string `json:"id"`
	// Name is the channel name without the leading hash.
	Name string `json:"name"`
	// IsPrivate reports whether the channel is private.
	IsPrivate bool `json:"is_private"`
	// IsMember reports whether the bot is already in the channel, so a join
	// can be skipped and no needless join notice is posted.
	IsMember bool `json:"is_member"`
	// IsArchived reports whether the channel is archived.
	IsArchived bool `json:"is_archived"`
	// NumMembers is the member count.
	NumMembers int `json:"num_members"`
	// Topic is the channel topic.
	Topic value `json:"topic"`
	// Purpose is the channel purpose.
	Purpose value `json:"purpose"`
}

// Message is a Slack message. The thread fields arrive on the parent of a
// thread in conversations.history, so a thread's shape is known without
// calling conversations.replies.
type Message struct {
	// Type is the message type, normally "message".
	Type string `json:"type"`
	// Subtype distinguishes system messages from user messages.
	Subtype string `json:"subtype"`
	// User is the author's Slack user ID.
	User string `json:"user"`
	// BotID is set when a bot authored the message.
	BotID string `json:"bot_id"`
	// Text is the message body.
	Text string `json:"text"`
	// TS is the message timestamp.
	TS string `json:"ts"`
	// ThreadTS is the parent timestamp of the thread this message belongs to,
	// equal to TS on the parent itself and empty when the message is not
	// threaded.
	ThreadTS string `json:"thread_ts"`
	// ReplyCount is the number of replies on a thread parent.
	ReplyCount int `json:"reply_count"`
	// ReplyUsers lists up to five user IDs that replied in the thread.
	ReplyUsers []string `json:"reply_users"`
	// LatestReply is the timestamp of the most recent reply in the thread.
	LatestReply string `json:"latest_reply"`
	// Files are the files shared in the message. Only the metadata Slack
	// already sent is read; whotoask never downloads file content.
	Files []File `json:"files"`
}

// File is a file shared in a message. Slack sends this metadata inside the
// history payload that whotoask already reads, so a shared document costs no
// extra request. Only the name and kind are kept: whotoask never downloads a
// file, so a video, an installer, or a slide deck is never transferred, stored,
// or indexed beyond what it is called.
type File struct {
	// Name is the file name as uploaded.
	Name string `json:"name"`
	// Title is the display title, which is the name when none was given.
	Title string `json:"title"`
	// Filetype is Slack's short kind, such as "pdf" or "mp4".
	Filetype string `json:"filetype"`
}

// humanSubtypes are the message subtypes a person authors. Slack marks
// everything else, such as joins, leaves, and topic changes, with a subtype
// too, and those are channel bookkeeping rather than conversation.
var humanSubtypes = map[string]bool{
	"file_share": true, "thread_broadcast": true, "me_message": true,
}

// FromPerson reports whether the message is one a human wrote, which is what
// an episode is built from. A file share is a person talking and is kept; a
// join notice or a bot post is not.
func (m Message) FromPerson() bool {
	return m.User != "" && m.BotID == "" && (m.Subtype == "" || humanSubtypes[m.Subtype])
}

// FileNames returns what each shared file is called, preferring the title when
// one was given. It never reads file content.
func (m Message) FileNames() []string {
	out := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		name := f.Title
		if name == "" {
			name = f.Name
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// SearchText returns the words of a message for indexing: what was written,
// plus what any shared file is called. A file name such as
// "billing-retry-postmortem.pdf" is often the most searchable thing in a
// conversation, and it is metadata Slack already sent.
func (m Message) SearchText() string {
	if len(m.Files) == 0 {
		return m.Text
	}
	parts := make([]string, 0, len(m.Files)*2+1)
	if m.Text != "" {
		parts = append(parts, m.Text)
	}
	seen := make(map[string]bool)
	for _, f := range m.Files {
		for _, s := range []string{f.Name, f.Title} {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// Permalink builds the archive URL for a message from data every history page
// already carries, so no chat.getPermalink call is needed. workspaceURL is the
// url reported by auth.test. It returns an empty string when workspaceURL or
// ts is missing. When threadTS names a different message, the link points at
// the reply inside its thread.
func Permalink(workspaceURL, channelID, ts, threadTS string) string {
	if workspaceURL == "" || channelID == "" || ts == "" {
		return ""
	}
	link := strings.TrimSuffix(workspaceURL, "/") +
		"/archives/" + channelID + "/p" + strings.ReplaceAll(ts, ".", "")
	if threadTS != "" && threadTS != ts {
		link += "?thread_ts=" + threadTS + "&cid=" + channelID
	}
	return link
}

// apiMeta is the common envelope of every Slack Web API response.
type apiMeta struct {
	// OK reports whether the call succeeded.
	OK bool `json:"ok"`
	// Error holds the error code when OK is false.
	Error string `json:"error"`
	// Metadata carries the pagination cursor.
	Metadata struct {
		// NextCursor is the cursor for the next page, empty when done.
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// response is implemented by every decoded Slack response via embedded apiMeta.
type response interface {
	// ok reports whether the call succeeded.
	ok() bool
	// errMsg returns the Slack error code.
	errMsg() string
	// cursor returns the next-page cursor.
	cursor() string
}

// ok reports whether the call succeeded.
func (a apiMeta) ok() bool { return a.OK }

// errMsg returns the Slack error code.
func (a apiMeta) errMsg() string { return a.Error }

// cursor returns the next-page cursor.
func (a apiMeta) cursor() string { return a.Metadata.NextCursor }

// usersListResp decodes users.list.
type usersListResp struct {
	apiMeta
	// Members is the page of users.
	Members []User `json:"members"`
}

// channelsListResp decodes conversations.list.
type channelsListResp struct {
	apiMeta
	// Channels is the page of channels.
	Channels []Channel `json:"channels"`
}

// historyResp decodes conversations.history.
type historyResp struct {
	apiMeta
	// Messages is the page of messages.
	Messages []Message `json:"messages"`
	// HasMore reports whether more pages remain.
	HasMore bool `json:"has_more"`
}

// maxListPages bounds how many cursor pages one listing may follow, so a
// cursor chain that never ends cannot hold a read in a loop forever. At two
// hundred items a page it allows two hundred thousand, far past any real
// workspace; hitting it returns the partial set with ErrTruncated rather than
// passing it off as complete.
const maxListPages = 1000

// Users returns every non-bot user in the workspace, deactivated members
// included. Dropping the deactivated split a departed person's in-window
// messages onto an opaque slack:UID that joins none of their other sources,
// and the recently departed are exactly who a knowledge-continuity read is
// about; the export path keeps them too, so both reads of one workspace now
// index the same people.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var all []User
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxListPages {
			return all, fmt.Errorf("slack users.list: %w after %d pages", ErrTruncated, pages)
		}
		params := url.Values{"limit": {"200"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp usersListResp
		if err := c.do(ctx, "users.list", params, &resp); err != nil {
			return nil, err
		}
		for _, u := range resp.Members {
			if u.IsBot {
				continue
			}
			all = append(all, u)
		}
		cursor = resp.cursor()
		if cursor == "" {
			return all, nil
		}
	}
}

// Channels lists conversations of the given comma-separated types (for example
// "public_channel" or "public_channel,private_channel"), excluding archived
// channels.
func (c *Client) Channels(ctx context.Context, types string) ([]Channel, error) {
	var all []Channel
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= maxListPages {
			return all, fmt.Errorf("slack conversations.list: %w after %d pages", ErrTruncated, pages)
		}
		params := url.Values{
			"limit":            {"200"},
			"exclude_archived": {"true"},
			"types":            {types},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp channelsListResp
		if err := c.do(ctx, "conversations.list", params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Channels...)
		cursor = resp.cursor()
		if cursor == "" {
			return all, nil
		}
	}
}

// History returns up to limit messages from channelID newer than oldest, a
// Slack timestamp string ("" means no lower bound). It stops at limit messages
// or when Slack reports no more pages.
// JoinChannel adds the bot to a public channel so its history can be read
// without a manual invite. It needs the channels:join scope. Joining a channel
// the bot is already in is a no-op. It cannot join a private channel; Slack
// only lets a member invite the bot there.
func (c *Client) JoinChannel(ctx context.Context, channelID string) error {
	var resp okResp
	return c.do(ctx, "conversations.join", url.Values{"channel": {channelID}}, &resp)
}

func (c *Client) History(ctx context.Context, channelID, oldest string, limit int) ([]Message, error) {
	var all []Message
	cursor := ""
	for limit <= 0 || len(all) < limit {
		params := url.Values{
			"channel": {channelID},
			"limit":   {"200"},
		}
		if oldest != "" {
			params.Set("oldest", oldest)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp historyResp
		if err := c.do(ctx, "conversations.history", params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Messages...)
		cursor = resp.cursor()
		if cursor == "" || !resp.HasMore {
			break
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Replies returns up to limit messages in the thread under threadTS,
// beginning with the parent. It is the only call that reads a conversation
// rather than its shape, so it runs only when an archive is being kept.
func (c *Client) Replies(ctx context.Context, channelID, threadTS string, limit int) ([]Message, error) {
	if channelID == "" || threadTS == "" {
		return nil, nil
	}
	var all []Message
	cursor := ""
	for limit <= 0 || len(all) < limit {
		params := url.Values{
			"channel": {channelID},
			"ts":      {threadTS},
			"limit":   {"200"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var resp historyResp
		if err := c.do(ctx, "conversations.replies", params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Messages...)
		cursor = resp.cursor()
		if cursor == "" || !resp.HasMore {
			break
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// do calls a Slack Web API method with form params and decodes the result into
// out. It tries again after an HTTP 429 and, for a method that is safe to
// repeat, after a 500, 502, 503, or 504, waiting for Retry-After or one second
// between tries. The budget is maxRetries, or historyRetries for the two
// history methods. A 429 that outlasts the budget comes back as ErrRateLimited.
// A server error that outlasts it comes back as the unexpected status error a
// single try would give, so a caller never mistakes an outage for a throttle.
func (c *Client) do(ctx context.Context, method string, params url.Values, out response) error {
	endpoint := c.baseURL + "/" + method
	retries := c.maxRetries
	if method == "conversations.history" || method == "conversations.replies" {
		retries = c.historyRetries
	}
	serverErrors := repeatable(method)
	last := 0
	retry := func(resp *http.Response) bool {
		if !retryStatus(resp.StatusCode, serverErrors) {
			return false
		}
		last = resp.StatusCode
		return true
	}
	resp, body, err := httputil.Do(ctx, c.http, retries, retry, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(
			ctx, http.MethodPost, endpoint, strings.NewReader(params.Encode()))
		if err != nil {
			return nil, fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	})
	if errors.Is(err, httputil.ErrRateLimited) {
		if last != http.StatusTooManyRequests {
			return fmt.Errorf("slack %s: unexpected status %d", method, last)
		}
		return fmt.Errorf("slack %s: %w", method, ErrRateLimited)
	}
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s: unexpected status %d", method, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("slack %s: decode: %w", method, err)
	}
	if !out.ok() {
		return fmt.Errorf("slack %s: %w: %s", method, ErrAPI, out.errMsg())
	}
	return nil
}

// repeatable reports whether a second try of method after a server error does
// no harm. Reads, the join, the identity check, and the socket URL request
// qualify. A message post does not: its first try may have gone through, and a
// repeat would post the message twice.
func repeatable(method string) bool {
	switch method {
	case "users.list", "conversations.list", "conversations.join", "conversations.history",
		"conversations.replies", "auth.test", "apps.connections.open":
		return true
	}
	return false
}

// retryStatus reports whether a response with this status code is tried again.
// A 429 always is. A 500, 502, 503, or 504 is when serverErrors is set.
func retryStatus(code int, serverErrors bool) bool {
	switch code {
	case http.StatusTooManyRequests:
		return true
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return serverErrors
	}
	return false
}
