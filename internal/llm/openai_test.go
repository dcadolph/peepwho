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

func TestOpenAIChat(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		var req openaiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "gpt-4o" || len(req.Messages) != 2 || req.Messages[0].Role != "system" {
			t.Errorf("request = %+v", req)
		}
		_, _ = io.WriteString(w,
			`{"choices":[{"message":{"role":"assistant","content":"{\"people\":[\"2\"]}"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := NewOpenAI("sk-test", WithOpenAIBaseURL(srv.URL+"/v1"))
	got, err := c.Chat(context.Background(), "system prompt", "user prompt")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != `{"people":["2"]}` {
		t.Errorf("reply = %q", got)
	}
}

func TestOpenAIChatNoKeyOmitsHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization = %q, want empty for a local server", got)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	c := NewOpenAI("", WithOpenAIBaseURL(srv.URL))
	if _, err := c.Chat(context.Background(), "s", "u"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
}

func TestOpenAIChatErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Status int
		Body   string
	}{{ // Test 0: API error status wraps ErrModel.
		Status: 429, Body: `{"error":{"message":"rate limited"}}`,
	}, { // Test 1: An empty reply wraps ErrModel.
		Status: 200, Body: `{"choices":[]}`,
	}}
	for testNum, test := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.Status)
			_, _ = io.WriteString(w, test.Body)
		}))
		t.Cleanup(srv.Close)
		c := NewOpenAI("sk-test", WithOpenAIBaseURL(srv.URL))
		if _, err := c.Chat(context.Background(), "s", "u"); !errors.Is(err, ErrModel) {
			t.Errorf("test %d: error = %v, want ErrModel", testNum, err)
		}
	}
}
