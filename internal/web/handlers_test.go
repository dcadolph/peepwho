package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// TestCLIHandler covers the plain-text command mirror: a known command comes
// back as the CLI printed it, an unknown one is a 404.
func TestCLIHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name     string
		URL      string
		Known    bool
		WantCode int
		WantBody string
	}{{ // Test 0: A known command streams its output as text.
		Name: "known", URL: "/api/cli?cmd=ask+retries", Known: true,
		WantCode: http.StatusOK, WantBody: "ran: ask retries",
	}, { // Test 1: An unknown command is a 404, not an empty success.
		Name: "unknown", URL: "/api/cli?cmd=nope", Known: false,
		WantCode: http.StatusNotFound, WantBody: "no such command",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			h := cliHandler(func(command string) (string, bool) {
				return "ran: " + command, test.Known
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.URL, nil))
			if rec.Code != test.WantCode {
				t.Fatalf("code = %d, want %d", rec.Code, test.WantCode)
			}
			if !strings.Contains(rec.Body.String(), test.WantBody) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), test.WantBody)
			}
		})
	}
}

// TestRelatedHandler covers the neighbor-topics endpoint: the topic parameter
// is required, and the limit parameter reaches the resolver.
func TestRelatedHandler(t *testing.T) {
	t.Parallel()
	h := relatedHandler(func(topic string, limit int) []resolve.TopicRelation {
		return []resolve.TopicRelation{{Topic: fmt.Sprintf("%s-near-%d", topic, limit), Overlap: 0.5}}
	})

	// Test 0: No topic named is a 400 that says how to name one.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/related", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no topic: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// Test 1: The topic and a positive limit are handed to the resolver.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/related?topic=billing&limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var got struct {
		Topic   string                  `json:"topic"`
		Related []resolve.TopicRelation `json:"related"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Topic != "billing" || len(got.Related) != 1 || got.Related[0].Topic != "billing-near-3" {
		t.Errorf("got %+v, want billing with billing-near-3", got)
	}

	// Test 2: A non-numeric limit falls back to the default instead of erroring.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/related?topic=billing&limit=x", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Related[0].Topic != "billing-near-8" {
		t.Errorf("limit fallback: got %q, want billing-near-8", got.Related[0].Topic)
	}
}

// TestAttestHandler covers the sealed-bundle download: the bytes pass through
// untouched, download mode names the file, and a sealing failure is a 500 that
// reaches the log rather than the visitor.
func TestAttestHandler(t *testing.T) {
	t.Parallel()

	// Test 0: The manifest is served verbatim with a JSON content type.
	var log strings.Builder
	sealed := Sealed{Manifest: []byte(`{"sealed":true}`), Archive: []byte("PK-zip")}
	h := attestHandler(func() (Sealed, error) { return sealed, nil }, &log)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attest", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"sealed":true}` {
		t.Fatalf("code %d body %q, want 200 with the manifest verbatim", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("plain fetch set Content-Disposition, want none")
	}

	// Test 1: Download mode serves the zip under the artifact's name.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attest?download=1", nil))
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "peepwho-finding.zip") {
		t.Errorf("download Content-Disposition = %q, want the artifact filename", cd)
	}
	if rec.Body.String() != "PK-zip" || rec.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("download body %q type %q, want the archive as a zip",
			rec.Body.String(), rec.Header().Get("Content-Type"))
	}

	// Test 2: A sealing error stays out of the response and lands in the log.
	h = attestHandler(func() (Sealed, error) { return Sealed{}, fmt.Errorf("no signing key") }, &log)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attest", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("error: code = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "signing key") {
		t.Error("error detail leaked to the visitor")
	}
	if !strings.Contains(log.String(), "no signing key") {
		t.Errorf("log %q does not carry the cause", log.String())
	}
}

// TestDepartureHandler covers the exposure endpoint: the person parameter is
// required, and the impact encodes as-is.
func TestDepartureHandler(t *testing.T) {
	t.Parallel()
	h := departureHandler(func(person string) resolve.DepartureImpact {
		return resolve.DepartureImpact{Person: person, Name: "Ana", Sole: []string{"billing-retries"}}
	})

	// Test 0: No person named is a 400.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/departure", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no person: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// Test 1: The named person's impact comes back whole.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/departure?person=ana%40corp.com", nil))
	var got resolve.DepartureImpact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Person != "ana@corp.com" || len(got.Sole) != 1 {
		t.Errorf("got %+v, want ana@corp.com holding one sole topic", got)
	}
}

// TestHandlerFactoriesPanicWithoutDeps pins the developer-error contract: a
// handler wired without its function is a panic at build time, not a nil
// dereference at request time.
func TestHandlerFactoriesPanicWithoutDeps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name string
		Call func()
	}{
		{"cli", func() { cliHandler(nil) }},
		{"related", func() { relatedHandler(nil) }},
		{"attest", func() { attestHandler(nil, &strings.Builder{}) }},
		{"departure", func() { departureHandler(nil) }},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic on nil dependency", test.Name)
				}
			}()
			test.Call()
		})
	}
}
