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

// OpenAI-compatible API defaults.
const (
	// openaiBaseURL is the OpenAI endpoint; any compatible server works. The
	// base carries the API version so a provider whose version path differs,
	// such as Gemini's OpenAI-compatible endpoint, sets its own base.
	openaiBaseURL = "https://api.openai.com/v1"
	// openaiDefaultModel is used when no model is configured.
	openaiDefaultModel = "gpt-4o"
)

// OpenAI is a minimal chat-completions client. The wire format is the de facto
// standard spoken by OpenAI and by local servers like LM Studio, llamafile,
// and vLLM, so one client covers them all via the base URL. It exists for the
// explicitly opted-in cloud path: the caller is responsible for checking the
// egress policy and redacting the payload before any call. The API key is
// never logged and travels only in the request header.
type OpenAI struct {
	// apiKey authenticates requests; may be empty for local servers.
	apiKey string
	// model is the model id.
	model string
	// baseURL is the API root.
	baseURL string
	// http performs requests.
	http *http.Client
}

// OpenAIOption configures the client.
type OpenAIOption func(*OpenAI)

// WithOpenAIModel overrides the default model.
func WithOpenAIModel(model string) OpenAIOption {
	return func(o *OpenAI) {
		if model != "" {
			o.model = model
		}
	}
}

// WithOpenAIBaseURL points the client at a compatible server, such as a local
// LM Studio or vLLM instance.
func WithOpenAIBaseURL(u string) OpenAIOption {
	return func(o *OpenAI) {
		if u != "" {
			o.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// WithOpenAITransport wraps the client's HTTP transport, used to enforce the
// egress policy on the actual outbound host.
func WithOpenAITransport(rt http.RoundTripper) OpenAIOption {
	return func(o *OpenAI) {
		if rt != nil {
			o.http.Transport = rt
		}
	}
}

// NewOpenAI returns an OpenAI-compatible client. The key may be empty when the
// server does not require one, as local servers usually do not.
func NewOpenAI(apiKey string, opts ...OpenAIOption) *OpenAI {
	o := &OpenAI{
		apiKey:  apiKey,
		model:   openaiDefaultModel,
		baseURL: openaiBaseURL,
		http:    httputil.NewClient(120 * time.Second),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// openaiRequest is the chat-completions request body.
type openaiRequest struct {
	// Model is the model id.
	Model string `json:"model"`
	// Messages is the conversation.
	Messages []openaiMessage `json:"messages"`
}

// openaiMessage is one conversation turn.
type openaiMessage struct {
	// Role is "system", "user", or "assistant".
	Role string `json:"role"`
	// Content is the turn's text.
	Content string `json:"content"`
}

// openaiResponse is the subset of the chat-completions response whotoask reads.
type openaiResponse struct {
	// Choices holds the completions; the first carries the reply.
	Choices []struct {
		// Message is the assistant turn.
		Message openaiMessage `json:"message"`
	} `json:"choices"`
}

// Chat sends the system and user prompts and returns the text reply.
func (o *OpenAI) Chat(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(openaiRequest{
		Model: o.model,
		Messages: []openaiMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm: openai encode: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: %w: openai request: %w", ErrModel, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: openai read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: %w: openai status %d: %s",
			ErrModel, resp.StatusCode, apiErrorMessage(raw))
	}
	var out openaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm: %w: openai decode: %w", ErrModel, err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm: %w: openai returned no text", ErrModel)
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
