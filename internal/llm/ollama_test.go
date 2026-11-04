package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChat verifies a successful chat returns the assistant content and sends
// the configured model with JSON format.
func TestChat(t *testing.T) {
	t.Parallel()
	var gotModel, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		gotFormat = req.Format
		io.WriteString(w, `{"message":{"role":"assistant","content":"{\"summary\":\"talk to jane\"}"}}`)
	}))
	t.Cleanup(srv.Close)

	c := New("qwen2.5", WithBaseURL(srv.URL))
	got, err := c.Chat(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != `{"summary":"talk to jane"}` {
		t.Errorf("content = %q", got)
	}
	if gotModel != "qwen2.5" {
		t.Errorf("model = %q, want qwen2.5", gotModel)
	}
	if gotFormat != "json" {
		t.Errorf("format = %q, want json", gotFormat)
	}
}

// TestChatServerError verifies a non-200 status maps to ErrModel.
func TestChatServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "model not found")
	}))
	t.Cleanup(srv.Close)

	c := New("", WithBaseURL(srv.URL))
	if _, err := c.Chat(context.Background(), "s", "u"); !errors.Is(err, ErrModel) {
		t.Fatalf("err = %v, want ErrModel", err)
	}
}

// TestChatEmpty verifies empty content maps to ErrEmptyResponse.
func TestChatEmpty(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"message":{"role":"assistant","content":"  "}}`)
	}))
	t.Cleanup(srv.Close)

	c := New("", WithBaseURL(srv.URL))
	if _, err := c.Chat(context.Background(), "s", "u"); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("err = %v, want ErrEmptyResponse", err)
	}
}

// TestDefaultModel verifies New defaults the model when empty.
func TestDefaultModel(t *testing.T) {
	t.Parallel()
	if New("").model != defaultModel {
		t.Errorf("default model = %q, want %q", New("").model, defaultModel)
	}
}
