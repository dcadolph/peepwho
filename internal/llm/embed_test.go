package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEmbed verifies a vector is returned and the embed model is sent.
func TestEmbed(t *testing.T) {
	t.Parallel()
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		io.WriteString(w, `{"embedding":[0.1,0.2,0.3]}`)
	}))
	t.Cleanup(srv.Close)

	c := New("llama3.1", WithBaseURL(srv.URL), WithEmbedModel("mxbai"))
	vec, err := c.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Errorf("vec = %v, want [0.1 0.2 0.3]", vec)
	}
	if gotModel != "mxbai" {
		t.Errorf("embed model = %q, want mxbai", gotModel)
	}
}

// TestEmbedError verifies a server error maps to ErrModel.
func TestEmbedError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "no model")
	}))
	t.Cleanup(srv.Close)
	if _, err := New("", WithBaseURL(srv.URL)).Embed(context.Background(), "x"); !errors.Is(err, ErrModel) {
		t.Fatalf("err = %v, want ErrModel", err)
	}
}

// TestEmbedEmpty verifies an empty vector maps to ErrEmptyResponse.
func TestEmbedEmpty(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"embedding":[]}`)
	}))
	t.Cleanup(srv.Close)
	if _, err := New("", WithBaseURL(srv.URL)).Embed(context.Background(), "x"); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("err = %v, want ErrEmptyResponse", err)
	}
}

// TestDefaultEmbedModel verifies the embed model defaults.
func TestDefaultEmbedModel(t *testing.T) {
	t.Parallel()
	if got := New("").embedModel; got != defaultEmbedModel {
		t.Errorf("default embed model = %q, want %q", got, defaultEmbedModel)
	}
}

// TestEmbedTaskPrefixes verifies the nomic family gets the task prefixes it
// was trained to expect, on the right side, and that other models get raw
// text. Missing prefixes do not fail; they quietly wreck ranking, which is
// why this is pinned by a test rather than a comment.
func TestEmbedTaskPrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Model      string
		Task       EmbedTask
		WantPrompt string
	}{
		{Model: "nomic-embed-text", Task: EmbedQueries, WantPrompt: "search_query: kafka lag"},
		{Model: "nomic-embed-text", Task: EmbedDocuments, WantPrompt: "search_document: kafka lag"},
		{Model: "nomic-embed-text-v1.5", Task: EmbedDocuments, WantPrompt: "search_document: kafka lag"},
		{Model: "mxbai-embed-large", Task: EmbedQueries, WantPrompt: "kafka lag"},
		{Model: "all-minilm", Task: EmbedDocuments, WantPrompt: "kafka lag"},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var got embedRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &got)
				io.WriteString(w, `{"embedding":[0.1]}`)
			}))
			t.Cleanup(srv.Close)
			c := New("", WithBaseURL(srv.URL), WithEmbedModel(test.Model), WithEmbedTask(test.Task))
			if _, err := c.Embed(context.Background(), "kafka lag"); err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if got.Prompt != test.WantPrompt {
				t.Errorf("prompt = %q, want %q", got.Prompt, test.WantPrompt)
			}
		})
	}
}
