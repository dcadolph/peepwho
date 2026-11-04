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

func TestAnthropicChat(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != anthropicDefaultModel || req.System == "" || len(req.Messages) != 1 {
			t.Errorf("request = %+v", req)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"people\":[\"1\"]}"}],"stop_reason":"end_turn"}`)
	}))
	t.Cleanup(srv.Close)

	c := NewAnthropic("sk-test", WithAnthropicBaseURL(srv.URL))
	got, err := c.Chat(context.Background(), "system prompt", "user prompt")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != `{"people":["1"]}` {
		t.Errorf("reply = %q", got)
	}
}

func TestAnthropicChatErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Status int
		Body   string
	}{{ // Test 0: API error status wraps ErrModel.
		Status: 401, Body: `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`,
	}, { // Test 1: A refusal wraps ErrModel.
		Status: 200, Body: `{"content":[],"stop_reason":"refusal"}`,
	}, { // Test 2: An empty reply wraps ErrModel.
		Status: 200, Body: `{"content":[],"stop_reason":"end_turn"}`,
	}}
	for testNum, test := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.Status)
			_, _ = io.WriteString(w, test.Body)
		}))
		t.Cleanup(srv.Close)
		c := NewAnthropic("sk-test", WithAnthropicBaseURL(srv.URL))
		if _, err := c.Chat(context.Background(), "s", "u"); !errors.Is(err, ErrModel) {
			t.Errorf("test %d: error = %v, want ErrModel", testNum, err)
		}
	}
}

func TestNewAnthropicEmptyKeyPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewAnthropic(\"\") did not panic")
		}
	}()
	NewAnthropic("")
}
