// Package llm provides local large language model clients. The Ollama client
// talks to a local Ollama server, so under the default policy no data leaves
// the machine.
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

	"github.com/dcadolph/peepwho/internal/httputil"
)

// defaultBaseURL is the local Ollama server address.
const defaultBaseURL = "http://localhost:11434"

// defaultModel is used when no model is given.
const defaultModel = "llama3.1"

// Doer performs an HTTP request. *http.Client satisfies it; tests inject a stub.
type Doer interface {
	// Do performs the request and returns the response.
	Do(req *http.Request) (*http.Response, error)
}

// Ollama calls a local Ollama chat model.
type Ollama struct {
	// baseURL is the Ollama server address.
	baseURL string
	// model is the model name to chat with.
	model string
	// embedModel is the model name used for embeddings.
	embedModel string
	// embedTask is which side of retrieval Embed serves.
	embedTask EmbedTask
	// http performs requests.
	http Doer
}

// Option configures an Ollama client.
type Option func(*Ollama)

// WithBaseURL overrides the server address.
func WithBaseURL(u string) Option {
	return func(o *Ollama) {
		if u != "" {
			o.baseURL = u
		}
	}
}

// WithEmbedModel overrides the embedding model.
func WithEmbedModel(name string) Option {
	return func(o *Ollama) {
		if name != "" {
			o.embedModel = name
		}
	}
}

// chatTimeout bounds one exchange with the model server. Local generation on
// modest hardware is slow, so the bound is generous.
const chatTimeout = 300 * time.Second

// New returns an Ollama client for model, defaulting the model when empty.
func New(model string, opts ...Option) *Ollama {
	o := &Ollama{
		baseURL: defaultBaseURL, model: model, embedModel: defaultEmbedModel,
		http: httputil.NewClient(chatTimeout),
	}
	if o.model == "" {
		o.model = defaultModel
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// message is one chat message.
type message struct {
	// Role is "system", "user", or "assistant".
	Role string `json:"role"`
	// Content is the message text.
	Content string `json:"content"`
}

// chatRequest is the body sent to /api/chat.
type chatRequest struct {
	// Model is the model name.
	Model string `json:"model"`
	// Messages is the conversation.
	Messages []message `json:"messages"`
	// Stream disables streaming when false.
	Stream bool `json:"stream"`
	// Format requests a JSON response when set to "json".
	Format string `json:"format,omitempty"`
}

// chatResponse is the body returned by /api/chat.
type chatResponse struct {
	// Message is the assistant reply.
	Message message `json:"message"`
	// Error holds a server error message when present.
	Error string `json:"error"`
}

// Chat sends a system and user message and returns the assistant content. It
// requests a JSON-formatted reply.
// unreachable turns a transport error into an actionable message. A refused or
// timed-out connection almost always means Ollama is not running, so it says so
// plainly instead of surfacing a raw dial error under a doubled prefix. ErrModel
// already carries the "llm:" prefix, so this adds none.
func (o *Ollama) unreachable(err error) error {
	return fmt.Errorf(
		"%w: cannot reach a local model at %s. Is Ollama running? Start it with `ollama serve`, "+
			"pull a model, or pass --ollama-url to point elsewhere (%v)", ErrModel, o.baseURL, err)
}

func (o *Ollama) Chat(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:  o.model,
		Stream: false,
		Format: "json",
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return "", o.unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llm: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: %w: status %d: %s", ErrModel, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("llm: decode: %w", err)
	}
	if cr.Error != "" {
		return "", fmt.Errorf("llm: %w: %s", ErrModel, cr.Error)
	}
	content := strings.TrimSpace(cr.Message.Content)
	if content == "" {
		return "", ErrEmptyResponse
	}
	return content, nil
}
