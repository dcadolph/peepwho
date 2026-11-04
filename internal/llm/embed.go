package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// defaultEmbedModel is the embedding model used when none is given.
const defaultEmbedModel = "nomic-embed-text"

// EmbedTask says which side of retrieval a client embeds for. Some models,
// including the default nomic family, are trained asymmetrically: a stored
// item and a question about it must carry different task prefixes, and
// leaving them off quietly wrecks ranking rather than failing.
type EmbedTask int

const (
	// EmbedQueries marks text as a question looking for matches. It is the
	// default because most call sites embed what a person just asked.
	EmbedQueries EmbedTask = iota
	// EmbedDocuments marks text as an item being stored to be found later,
	// which is what indexing embeds.
	EmbedDocuments
)

// WithEmbedTask sets which side of retrieval this client embeds for.
func WithEmbedTask(task EmbedTask) Option {
	return func(o *Ollama) { o.embedTask = task }
}

// embedPrefix returns the task prefix the configured model needs, or nothing
// for models that embed both sides the same way.
func (o *Ollama) embedPrefix() string {
	if !strings.Contains(o.embedModel, "nomic-embed") {
		return ""
	}
	if o.embedTask == EmbedDocuments {
		return "search_document: "
	}
	return "search_query: "
}

// embedRequest is the body sent to /api/embeddings.
type embedRequest struct {
	// Model is the embedding model name.
	Model string `json:"model"`
	// Prompt is the text to embed.
	Prompt string `json:"prompt"`
}

// embedResponse is the body returned by /api/embeddings.
type embedResponse struct {
	// Embedding is the returned vector.
	Embedding []float32 `json:"embedding"`
	// Error holds a server error message when present.
	Error string `json:"error"`
}

// Embed returns the embedding vector for text using the configured embed
// model, applying the task prefix the model was trained to expect.
func (o *Ollama) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.embedModel, Prompt: o.embedPrefix() + text})
	if err != nil {
		return nil, fmt.Errorf("llm: encode embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: new embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, o.unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: read embed body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: %w: status %d: %s", ErrModel, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("llm: decode embed: %w", err)
	}
	if er.Error != "" {
		return nil, fmt.Errorf("llm: %w: %s", ErrModel, er.Error)
	}
	if len(er.Embedding) == 0 {
		return nil, ErrEmptyResponse
	}
	return er.Embedding, nil
}
