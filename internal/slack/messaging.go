package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
)

// okResp decodes a response that carries only the standard envelope.
type okResp struct {
	apiMeta
}

// PostMessage posts text to a channel. When threadTS is set, the message is a
// reply in that thread. Slack mrkdwn formatting in text is honored.
func (c *Client) PostMessage(ctx context.Context, channel, threadTS, text string) error {
	params := url.Values{"channel": {channel}, "text": {text}}
	if threadTS != "" {
		params.Set("thread_ts", threadTS)
	}
	var resp okResp
	return c.do(ctx, "chat.postMessage", params, &resp)
}

// PostEphemeral posts text visible only to one user in a channel. Recall
// answers use it: they name the conversations one person took part in, which
// is nobody else's business even in a channel they share.
func (c *Client) PostEphemeral(ctx context.Context, channel, user, text string) error {
	params := url.Values{"channel": {channel}, "user": {user}, "text": {text}}
	var resp okResp
	return c.do(ctx, "chat.postEphemeral", params, &resp)
}

// authTestResp decodes auth.test.
type authTestResp struct {
	apiMeta
	// UserID is the authenticated bot's own user ID.
	UserID string `json:"user_id"`
	// URL is the workspace base URL.
	URL string `json:"url"`
	// Team is the workspace name.
	Team string `json:"team"`
	// TeamID is the workspace ID.
	TeamID string `json:"team_id"`
}

// Auth describes the authenticated identity and the workspace it belongs to.
type Auth struct {
	// UserID is the authenticated bot's own user ID.
	UserID string
	// URL is the workspace base URL, such as "https://acme.slack.com/", and is
	// what message permalinks are built from.
	URL string
	// Team is the workspace name.
	Team string
	// TeamID is the workspace ID, such as "T0123", which a slack:// deep link
	// needs to open a direct message in the right workspace.
	TeamID string
}

// AuthTest returns the authenticated identity and workspace. The bot uses the
// user ID to ignore its own messages and to strip its mention from inbound
// text; indexing uses the URL to build permalinks offline.
func (c *Client) AuthTest(ctx context.Context) (Auth, error) {
	var resp authTestResp
	if err := c.do(ctx, "auth.test", url.Values{}, &resp); err != nil {
		return Auth{}, err
	}
	return Auth{UserID: resp.UserID, URL: resp.URL, Team: resp.Team, TeamID: resp.TeamID}, nil
}

// responseURLPrefix is the only host slash-command response URLs may name, so
// a forged payload cannot point the bot's POST anywhere else.
const responseURLPrefix = "https://hooks.slack.com/"

// Response visibility values Slack understands.
const (
	// inChannel shows the reply to everyone in the channel.
	inChannel = "in_channel"
	// ephemeral shows the reply only to the person who ran the command.
	ephemeral = "ephemeral"
)

// Respond posts text to a slash-command response URL, visible to the whole
// channel. The URL is minted by Slack per invocation and needs no token.
func Respond(ctx context.Context, responseURL, text string) error {
	return respond(ctx, responseURL, text, inChannel, responseURLPrefix)
}

// RespondPrivately posts text to a slash-command response URL so only the
// person who ran the command sees it. Recall answers take this path, so the
// visibility it sends is a privacy boundary rather than a preference.
func RespondPrivately(ctx context.Context, responseURL, text string) error {
	return respond(ctx, responseURL, text, ephemeral, responseURLPrefix)
}

// respond posts text to a slash-command response URL with the given
// visibility. allowedPrefix is the only host the URL may name; it is a
// parameter rather than a package variable so a test can point at a local
// server without mutating shared state that another test is reading.
func respond(ctx context.Context, responseURL, text, responseType, allowedPrefix string) error {
	if allowedPrefix == "" || !strings.HasPrefix(responseURL, allowedPrefix) {
		return fmt.Errorf("slack: respond: unexpected response url host")
	}
	body, err := json.Marshal(map[string]string{"response_type": responseType, "text": text})
	if err != nil {
		return fmt.Errorf("slack: respond: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: respond: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := httputil.NewClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: respond: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: respond: status %d", resp.StatusCode)
	}
	return nil
}

// connectionsOpenResp decodes apps.connections.open.
type connectionsOpenResp struct {
	apiMeta
	// URL is the WebSocket URL for a Socket Mode session.
	URL string `json:"url"`
}

// ConnectionsOpen opens a Socket Mode session and returns its WebSocket URL. It
// requires an app-level token (xapp-), not the bot token.
func (c *Client) ConnectionsOpen(ctx context.Context) (string, error) {
	var resp connectionsOpenResp
	if err := c.do(ctx, "apps.connections.open", url.Values{}, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}
