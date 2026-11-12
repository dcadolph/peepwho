package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

// TestAnthropicChatEffort verifies each request leaves room for thinking and
// asks for low effort, and that a model rejecting the effort setting gets one
// more try without it.
func TestAnthropicChatEffort(t *testing.T) {
	t.Parallel()
	const reply = `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`
	const rejected = `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`
	tests := []struct {
		Want          error
		WantResult    string
		WantEfforts   []string
		WantMaxTokens int
		RejectEffort  bool
		RejectAll     bool
	}{{ // Test 0: A request carries low effort and a limit with room to think.
		WantResult: "ok", WantEfforts: []string{"low"}, WantMaxTokens: 8000,
	}, { // Test 1: A model that rejects the effort setting is retried without it.
		RejectEffort: true,
		WantResult:   "ok", WantEfforts: []string{"low", ""}, WantMaxTokens: 8000,
	}, { // Test 2: A request rejected both ways fails after one retry.
		RejectAll: true,
		Want:      ErrModel, WantEfforts: []string{"low", ""}, WantMaxTokens: 8000,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var efforts []string
			handler := func(w http.ResponseWriter, r *http.Request) {
				var req anthropicRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if req.MaxTokens != test.WantMaxTokens {
					t.Errorf("max_tokens = %d, want %d", req.MaxTokens, test.WantMaxTokens)
				}
				effort := ""
				if req.OutputConfig != nil {
					effort = req.OutputConfig.Effort
				}
				mu.Lock()
				efforts = append(efforts, effort)
				mu.Unlock()
				if test.RejectAll || (test.RejectEffort && effort != "") {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, rejected)
					return
				}
				_, _ = io.WriteString(w, reply)
			}
			srv := httptest.NewServer(http.HandlerFunc(handler))
			t.Cleanup(srv.Close)

			c := NewAnthropic("sk-test", WithAnthropicBaseURL(srv.URL))
			got, err := c.Chat(context.Background(), "s", "u")
			if !errors.Is(err, test.Want) {
				t.Fatalf("error = %v, want %v", err, test.Want)
			}
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("reply mismatch (-want +got):\n%s", diff)
			}
			mu.Lock()
			defer mu.Unlock()
			if diff := cmp.Diff(test.WantEfforts, efforts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("efforts sent mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAnthropicChatTruncated verifies a response cut off at the token limit
// before any text is a model error that says so, while one cut off after some
// text still returns it.
func TestAnthropicChatTruncated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want        error
		WantResult  string
		WantMessage string
		Body        string
	}{{ // Test 0: Thinking that used the whole limit is a model error.
		Body: `{"content":[{"type":"thinking","thinking":""}],"stop_reason":"max_tokens"}`,
		Want: ErrModel, WantMessage: "8000 token limit before any text",
	}, { // Test 1: A limit reached with no content at all is a model error.
		Body: `{"content":[],"stop_reason":"max_tokens"}`,
		Want: ErrModel, WantMessage: "8000 token limit before any text",
	}, { // Test 2: A limit reached with only blank text is a model error.
		Body: `{"content":[{"type":"text","text":" \n"}],"stop_reason":"max_tokens"}`,
		Want: ErrModel, WantMessage: "8000 token limit before any text",
	}, { // Test 3: A limit reached after some text returns that text.
		Body:       `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens"}`,
		WantResult: "partial",
	}, { // Test 4: An empty reply that ended normally is not blamed on the limit.
		Body: `{"content":[],"stop_reason":"end_turn"}`,
		Want: ErrModel, WantMessage: "returned no text",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			handler := func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.Body)
			}
			srv := httptest.NewServer(http.HandlerFunc(handler))
			t.Cleanup(srv.Close)

			c := NewAnthropic("sk-test", WithAnthropicBaseURL(srv.URL))
			got, err := c.Chat(context.Background(), "s", "u")
			if !errors.Is(err, test.Want) {
				t.Fatalf("error = %v, want %v", err, test.Want)
			}
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("reply mismatch (-want +got):\n%s", diff)
			}
			if err != nil && !strings.Contains(err.Error(), test.WantMessage) {
				t.Errorf("error = %q, want it to contain %q", err, test.WantMessage)
			}
		})
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
