package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// testRecallHandler builds a handler whose recall function records the person
// it was asked about and answers with one conversation.
func testRecallHandler(t *testing.T, asked *string) http.Handler {
	t.Helper()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{
		Ask: ask,
		Recall: func(_ context.Context, person, query string, _ int) (recall.Answer, error) {
			*asked = person
			return recall.Answer{
				Query:  query,
				Person: person,
				Episodes: []recall.Episode{{
					People: []recall.Person{{Name: "Billy Ray"}},
					Place:  "infra",
					Source: "slack",
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

// TestRecallAPI verifies the endpoint answers for the named person, and that
// it refuses a request that names nobody: recall is personal, so an unscoped
// query must never fall back to searching everyone.
func TestRecallAPI(t *testing.T) {
	t.Parallel()
	var asked string
	h := testRecallHandler(t, &asked)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/recall?q=certificate&me=jane@x.com", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if asked != "jane@x.com" {
		t.Errorf("asked about %q, want jane@x.com", asked)
	}
	var got recall.Answer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Episodes) != 1 || got.Episodes[0].People[0].Name != "Billy Ray" {
		t.Errorf("answer = %+v, want the conversation and who was in it", got)
	}

	// Missing the person is still a bad request: recall is personal.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recall?q=certificate", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing me: status = %d, want 400", rec.Code)
	}

	// Missing the topic is valid now: recall returns the conversations you
	// took part in, most recent first.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/recall?me=jane@x.com", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("missing q: status = %d, want 200", rec.Code)
	}
}

// TestRecallAPIDisabled verifies the endpoint is absent when recall is not
// configured, rather than answering with an empty result.
func TestRecallAPIDisabled(t *testing.T) {
	t.Parallel()
	h := testHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/recall?q=certificate&me=jane@x.com", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when recall is disabled", rec.Code)
	}
}
