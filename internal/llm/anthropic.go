package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/httputil"
)

// Anthropic API defaults.
const (
	// anthropicBaseURL is the Claude API endpoint.
	anthropicBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the required API version header value.
	anthropicVersion = "2023-06-01"
	// anthropicDefaultModel is used when no model is configured. Override it
	// with WithAnthropicModel to trade capability for cost.
	anthropicDefaultModel = "claude-opus-5"
	// anthropicMaxTokens bounds the reply; answers here are one JSON object.
	anthropicMaxTokens = 1024
)

// Anthropic is a minimal Claude Messages API client. It exists for the
// explicitly opted-in cloud path: the caller is responsible for checking the
// egress policy and redacting the payload before any call. The API key is
// never logged and travels only in the request header.
type Anthropic struct {
	// apiKey authenticates requests.
	apiKey string
	// model is the Claude model id.
	model string
	// baseURL is the API root, overridable for tests.
	baseURL string
	// http performs requests.
	http *http.Client
}

// AnthropicOption configures the client.
type AnthropicOption func(*Anthropic)

// WithAnthropicModel overrides the default model.
func WithAnthropicModel(model string) AnthropicOption {
	return func(a *Anthropic) {
		if model != "" {
			a.model = model
		}
	}
}

// WithAnthropicBaseURL overrides the API root, mainly for tests.
func WithAnthropicBaseURL(u string) AnthropicOption {
	return func(a *Anthropic) {
		if u != "" {
			a.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithAnthropicTransport wraps the client's HTTP transport, used to enforce the
// egress policy on the actual outbound host.
func WithAnthropicTransport(rt http.RoundTripper) AnthropicOption {
	return func(a *Anthropic) {
		if rt != nil {
			a.http.Transport = rt
		}
	}
}

// NewAnthropic returns a Claude client. It panics on an empty key: the caller
// must have read it from the environment before constructing the client.
func NewAnthropic(apiKey string, opts ...AnthropicOption) *Anthropic {
	if apiKey == "" {
		panic("llm: NewAnthropic requires an api key")
	}
	a := &Anthropic{
		apiKey:  apiKey,
		model:   anthropicDefaultModel,
		baseURL: anthropicBaseURL,
		http:    httputil.NewClient(120 * time.Second),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// anthropicRequest is the Messages API request body.
type anthropicRequest struct {
	// Model is the Claude model id.
	Model string `json:"model"`
	// MaxTokens caps the reply length.
	MaxTokens int `json:"max_tokens"`
	// System is the system prompt.
	System string `json:"system,omitempty"`
	// Messages is the conversation, here a single user turn.
	Messages []anthropicMessage `json:"messages"`
}

// anthropicMessage is one conversation turn.
type anthropicMessage struct {
	// Role is "user" or "assistant".
	Role string `json:"role"`
	// Content is the turn's text.
	Content string `json:"content"`
}

// anthropicResponse is the subset of the Messages API response whotoask reads.
type anthropicResponse struct {
	// Content holds the reply blocks.
	Content []struct {
		// Type discriminates block kinds; text blocks carry the reply.
		Type string `json:"type"`
		// Text is the block's text.
		Text string `json:"text"`
	} `json:"content"`
	// StopReason says why generation ended; "refusal" means declined.
	StopReason string `json:"stop_reason"`
}

// Chat sends the system and user prompts to Claude and returns the text reply.
func (a *Anthropic) Chat(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(anthropicRequest{
		Model:     a.model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", fmt.Errorf("llm: anthropic encode: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: anthropic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: %w: anthropic request: %w", ErrModel, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: anthropic read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: %w: anthropic status %d: %s",
			ErrModel, resp.StatusCode, apiErrorMessage(raw))
	}
	var out anthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm: %w: anthropic decode: %w", ErrModel, err)
	}
	if out.StopReason == "refusal" {
		return "", fmt.Errorf("llm: %w: anthropic declined the request", ErrModel)
	}
	var b strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	reply := strings.TrimSpace(b.String())
	if reply == "" {
		return "", fmt.Errorf("llm: %w: anthropic returned no text", ErrModel)
	}
	return reply, nil
}
