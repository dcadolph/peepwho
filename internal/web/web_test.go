package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/llm"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/report"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// testHandler builds a handler whose Ask returns one canned person.
func testHandler(t *testing.T) http.Handler {
	t.Helper()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{
			Summary: "talk to jane",
			People: []model.Match{{
				Person:  &model.Person{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer"},
				Score:   1,
				Reasons: []string{"retries (topic)"},
			}},
		}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

// TestIndexPage verifies the root serves HTML with the version.
func TestIndexPage(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "whotoask") || !strings.Contains(body, "test") {
		t.Errorf("index page missing whotoask or version:\n%s", body)
	}
}

// TestAskAPI verifies the ask endpoint returns the answer as JSON.
func TestAskAPI(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ask?q=retries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var ans resolve.JSONAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ans.Summary != "talk to jane" {
		t.Errorf("summary = %q", ans.Summary)
	}
	if len(ans.People) != 1 || ans.People[0].Email != "jane@x.com" {
		t.Errorf("people = %+v, want jane@x.com", ans.People)
	}
}

// TestAskMissingQuery verifies a missing q is a 400.
func TestAskMissingQuery(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ask", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestNilAskPanics verifies the handler guards a nil Ask function.
func TestNilAskPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("Handler with nil Ask did not panic")
		}
	}()
	_, _ = Handler(Config{})
}

// TestFeedbackAPI verifies the feedback endpoint records votes and rejects bad
// requests.
func TestFeedbackAPI(t *testing.T) {
	t.Parallel()
	var got feedback.Entry
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{
		Ask:      ask,
		Feedback: func(e feedback.Entry) error { got = e; return nil },
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	body := `{"query":"billing retries","person":"jane@x.com","vote":"helpful","comment":"she owns it"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got.Query != "billing retries" || got.Person != "jane@x.com" ||
		got.Vote != feedback.Helpful || got.Comment != "she owns it" {
		t.Errorf("recorded entry = %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/feedback", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback",
		strings.NewReader(`{"query":"","vote":"helpful"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid entry status = %d, want 400", rec.Code)
	}
}

// TestUnauthorizedGetsSecurityHeaders verifies the 401 for a missing token
// still carries the hardening headers and a JSON content type, since
// securityHeaders wraps outermost.
func TestUnauthorizedGetsSecurityHeaders(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test", AuthToken: "secret"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ask?q=x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff on a 401", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY on a 401", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("Content-Security-Policy = %q, want default-src 'self' on a 401", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json on a 401", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on an API reply = %q, want no-store", got)
	}
}

// TestTokenCookieSecureBehindProxy verifies the session cookie is marked
// Secure when the TLS-terminating proxy the help text recommends forwards the
// request as plain HTTP with X-Forwarded-Proto. Reading r.TLS alone marked it
// insecure in exactly the deployment the docs describe.
func TestTokenCookieSecureBehindProxy(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test", AuthToken: "sekret"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/?token=sekret", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, authCookie+"=") {
		t.Fatalf("no session cookie set: %q", cookie)
	}
	if !strings.Contains(cookie, "Secure") {
		t.Errorf("cookie is not Secure behind the TLS proxy: %q", cookie)
	}

	plain := httptest.NewRequest(http.MethodGet, "/?token=sekret", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, plain)
	if strings.Contains(rec.Header().Get("Set-Cookie"), "Secure") {
		t.Errorf("plain local HTTP got a Secure cookie the browser would drop: %q",
			rec.Header().Get("Set-Cookie"))
	}
}

// TestAPICacheControl verifies API and report responses carry no-store while
// static assets stay cacheable: the APIs return personal data a shared proxy
// or browser cache must not keep, and the assets carry none.
func TestAPICacheControl(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	tests := []struct {
		Path      string
		WantStore bool
	}{{ // Test 0: An API reply is never cached.
		Path: "/api/ask?q=x", WantStore: true,
	}, { // Test 1: The report download is never cached.
		Path: "/report/risk.html", WantStore: true,
	}, { // Test 2: A static asset carries no such header.
		Path: "/static/portable.js", WantStore: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.Path, nil))
			got := rec.Header().Get("Cache-Control") == "no-store"
			if got != test.WantStore {
				t.Errorf("%s Cache-Control no-store = %t, want %t", test.Path, got, test.WantStore)
			}
		})
	}
}

// TestLocalOnlyHostGuard verifies the DNS-rebinding defense: with LocalOnly
// set, a request whose Host names anything but a loopback host is refused. A
// page on an attacker's domain rebound to 127.0.0.1 becomes same-origin with
// the tokenless loopback server in the browser, and the rebound domain in the
// Host header is the only trace of it.
func TestLocalOnlyHostGuard(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test", LocalOnly: true})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	open, err := Handler(Config{Ask: ask, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	tests := []struct {
		Host      string
		LocalOnly bool
		WantCode  int
	}{{ // Test 0: A rebound domain is refused.
		Host: "evil.example:8765", LocalOnly: true, WantCode: http.StatusForbidden,
	}, { // Test 1: 127.0.0.1 is the server's own name.
		Host: "127.0.0.1:8765", LocalOnly: true, WantCode: http.StatusOK,
	}, { // Test 2: localhost too.
		Host: "localhost:8765", LocalOnly: true, WantCode: http.StatusOK,
	}, { // Test 3: The IPv6 loopback, bracketed as a Host header carries it.
		Host: "[::1]:8765", LocalOnly: true, WantCode: http.StatusOK,
	}, { // Test 4: An ssh forward changes the port, never the loopback host.
		Host: "localhost:9999", LocalOnly: true, WantCode: http.StatusOK,
	}, { // Test 5: A host with no port at all still resolves.
		Host: "localhost", LocalOnly: true, WantCode: http.StatusOK,
	}, { // Test 6: Without LocalOnly, a proxied Host passes: the documented
		// TLS proxy forwards its public name.
		Host: "whotoask.corp.example", LocalOnly: false, WantCode: http.StatusOK,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			handler := h
			if !test.LocalOnly {
				handler = open
			}
			req := httptest.NewRequest(http.MethodGet, "/api/ask?q=x", nil)
			req.Host = test.Host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.WantCode {
				t.Errorf("host %q status = %d, want %d: %s",
					test.Host, rec.Code, test.WantCode, rec.Body.String())
			}
		})
	}
}

// TestSameOrigin verifies the CSRF origin check matches host and web scheme and
// rejects opaque or foreign origins.
func TestSameOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Origin     string
		Host       string
		WantResult bool
	}{
		{Origin: "http://whotoask.local", Host: "whotoask.local", WantResult: true},
		{Origin: "https://whotoask.local:8765", Host: "whotoask.local:8765", WantResult: true},
		{Origin: "http://evil.example", Host: "whotoask.local", WantResult: false},
		{Origin: "null", Host: "whotoask.local", WantResult: false},
		{Origin: "file://whotoask.local", Host: "whotoask.local", WantResult: false},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := sameOrigin(test.Origin, test.Host); got != test.WantResult {
				t.Errorf("sameOrigin(%q, %q) = %v, want %v",
					test.Origin, test.Host, got, test.WantResult)
			}
		})
	}
}

// TestFeedbackTooLarge verifies an oversized feedback body is rejected with 413
// rather than being read into memory.
func TestFeedbackTooLarge(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{
		Ask:      ask,
		Feedback: func(feedback.Entry) error { return nil },
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	big := `{"query":"q","person":"jane@x.com","vote":"helpful","comment":"` +
		strings.Repeat("a", 128<<10) + `"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(big)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413 for an oversized body", rec.Code)
	}
}

// TestFeedbackAPIDisabled verifies the endpoint is absent without a callback.
func TestFeedbackAPIDisabled(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when feedback is disabled", rec.Code)
	}
}

// TestPersonAPI verifies the person endpoint returns a profile and a 404 for
// unknown identifiers.
func TestPersonAPI(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	person := func(id string) (resolve.JSONProfile, bool) {
		if id != "jane@x.com" {
			return resolve.JSONProfile{}, false
		}
		return resolve.JSONProfile{ID: id, Name: "Jane Roe", Channels: []string{"payments"}}, true
	}
	h, err := Handler(Config{Ask: ask, Person: person, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/person?id=jane%40x.com", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got resolve.JSONProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Jane Roe" || len(got.Channels) != 1 {
		t.Errorf("profile = %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/person?id=ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown person status = %d, want 404", rec.Code)
	}
}

// TestDirectoryAPI verifies the directory endpoint serves the inventory and
// is absent when not configured.
func TestDirectoryAPI(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	dir := resolve.Directory{People: []resolve.DirectoryPerson{{ID: "jane@x.com", Name: "Jane Roe"}}}
	h, err := Handler(Config{Ask: ask, Version: "test", Directory: &dir})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/directory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got resolve.Directory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.People) != 1 || got.People[0].Name != "Jane Roe" {
		t.Errorf("directory = %+v", got)
	}

	rec = httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/directory", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unconfigured status = %d, want 404", rec.Code)
	}
}

// TestModesAPI verifies the modes endpoint reports mode and provider
// readiness and is absent when not configured.
func TestModesAPI(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	modes := func(context.Context) ModesReport {
		return ModesReport{
			Modes: map[string]ModeInfo{"keyword": {Ready: true}},
			Providers: map[string]ModeInfo{
				"ollama": {Ready: false, Hint: "Ollama is not running on this machine."},
			},
			Provider: "ollama",
		}
	}
	h, err := Handler(Config{Ask: ask, Version: "test", Modes: modes})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/modes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got ModesReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Modes["keyword"].Ready || got.Providers["ollama"].Ready || got.Provider != "ollama" {
		t.Errorf("report = %+v", got)
	}

	rec = httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/modes", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unconfigured status = %d, want 404", rec.Code)
	}
}

// TestAskProviderParam verifies the provider query parameter reaches the ask
// function.
func TestAskProviderParam(t *testing.T) {
	t.Parallel()
	var gotProvider string
	ask := func(_ context.Context, _, _, provider string, _ int) (resolve.Answer, error) {
		gotProvider = provider
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ask?q=x&mode=llm&provider=anthropic", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotProvider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", gotProvider)
	}
}

// TestAuthToken verifies the token gate: header, query parameter, and cookie
// all admit; anything else is a 401. A query token sets the session cookie and
// a GET is then redirected to the same URL stripped of the token, so the
// secret does not stay in the address bar, the history, or a request-line log.
func TestAuthToken(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{Ask: ask, Version: "test", AuthToken: "sekret"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	tests := []struct {
		Target       string
		Bearer       string
		Cookie       string
		WantLocation string
		WantCode     int
		WantCookie   bool
	}{{ // Test 0: No credential is a 401.
		Target: "/api/ask?q=x", WantCode: http.StatusUnauthorized,
	}, { // Test 1: A wrong bearer token is a 401.
		Target: "/api/ask?q=x", Bearer: "nope", WantCode: http.StatusUnauthorized,
	}, { // Test 2: The right bearer token admits.
		Target: "/api/ask?q=x", Bearer: "sekret", WantCode: http.StatusOK,
	}, { // Test 3: The right query token sets the session cookie and redirects
		// to the URL without the token.
		Target: "/?token=sekret", WantCode: http.StatusSeeOther, WantCookie: true,
		WantLocation: "/",
	}, { // Test 4: A wrong query token is a 401.
		Target: "/?token=nope", WantCode: http.StatusUnauthorized,
	}, { // Test 5: The session cookie admits.
		Target: "/api/ask?q=x", Cookie: "sekret", WantCode: http.StatusOK,
	}, { // Test 6: The redirect keeps the rest of the query.
		Target: "/?q=who+owns+billing&token=sekret", WantCode: http.StatusSeeOther,
		WantCookie: true, WantLocation: "/?q=who+owns+billing",
	}, { // Test 7: An API request with a query token answers in place. A
		// script has no cookie jar, so a redirect away from the token would
		// turn the documented curl pattern into a 401.
		Target: "/api/ask?q=x&token=sekret", WantCode: http.StatusOK, WantCookie: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, test.Target, nil)
			if test.Bearer != "" {
				req.Header.Set("Authorization", "Bearer "+test.Bearer)
			}
			if test.Cookie != "" {
				req.AddCookie(&http.Cookie{Name: authCookie, Value: test.Cookie})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != test.WantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.WantCode, rec.Body.String())
			}
			gotCookie := strings.Contains(rec.Header().Get("Set-Cookie"), authCookie+"=")
			if gotCookie != test.WantCookie {
				t.Errorf("set-cookie = %t, want %t", gotCookie, test.WantCookie)
			}
			if test.WantLocation != "" {
				if got := rec.Header().Get("Location"); got != test.WantLocation {
					t.Errorf("location = %q, want %q", got, test.WantLocation)
				}
				if strings.Contains(rec.Header().Get("Location"), "token=") {
					t.Error("redirect still carries the token")
				}
			}
		})
	}
}

// TestFeedbackCrossOrigin verifies a cross-origin vote is rejected and a
// same-origin one is recorded.
func TestFeedbackCrossOrigin(t *testing.T) {
	t.Parallel()
	recorded := 0
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	h, err := Handler(Config{
		Ask:      ask,
		Feedback: func(feedback.Entry) error { recorded++; return nil },
		Version:  "test",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	body := `{"query":"billing","person":"jane@x.com","vote":"helpful"}`

	req := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(body))
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || recorded != 0 {
		t.Errorf("cross-origin: status = %d recorded = %d, want 403 and 0", rec.Code, recorded)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || recorded != 1 {
		t.Errorf("same-origin: status = %d recorded = %d, want 200 and 1", rec.Code, recorded)
	}
}

// askQuery is the question the ask error tests send, which must never reach
// the server log.
const askQuery = "privatequestion"

// TestAskAPIErrors checks that a failed ask answers with a status and a message
// the asker can act on: Ollama guidance only where Ollama ran, the cloud
// provider by name with the error text kept out, a policy denial as 403 with
// its message, and the cause logged without the query.
func TestAskAPIErrors(t *testing.T) {
	t.Parallel()
	const secret = "sk-test-echoed-key"
	denied := fmt.Errorf("cloud provider anthropic: %w: mode=strict dest=api.anthropic.com "+
		"(use --policy redacted to send anonymized candidates, or --policy open)",
		policy.ErrEgressDenied)
	tests := []struct {
		WantBody   string
		WantLog    string
		Params     string
		Err        error
		NotBody    []string
		WantStatus int
	}{{ // Test 0: Ollama down in llm mode gets the Ollama steps.
		Err:    fmt.Errorf("llm resolve: %w: connection refused", llm.ErrModel),
		Params: "mode=llm&provider=ollama", WantStatus: http.StatusBadGateway,
		WantBody: "ollama.com", WantLog: "connection refused",
	}, { // Test 1: Semantic mode embeds through Ollama whatever provider is named.
		Err:    fmt.Errorf("semantic resolve: %w: connection refused", llm.ErrModel),
		Params: "mode=semantic", WantStatus: http.StatusBadGateway,
		WantBody: "ollama.com", WantLog: "connection refused",
	}, { // Test 2: A Claude failure names Claude and keeps the error text out.
		Err:    fmt.Errorf("llm: %w: anthropic status 401: invalid key %s", llm.ErrModel, secret),
		Params: "mode=llm&provider=anthropic", WantStatus: http.StatusBadGateway,
		WantBody: "Claude did not answer", NotBody: []string{"ollama", secret, "401"},
		WantLog: "anthropic status 401",
	}, { // Test 3: A ChatGPT failure names ChatGPT.
		Err:    fmt.Errorf("llm: %w: openai status 429: slow down %s", llm.ErrModel, secret),
		Params: "mode=llm&provider=openai", WantStatus: http.StatusBadGateway,
		WantBody: "ChatGPT did not answer", NotBody: []string{"ollama", secret, "429"},
		WantLog: "openai status 429",
	}, { // Test 4: A Gemini failure names Gemini.
		Err:    fmt.Errorf("llm: %w: openai returned no text", llm.ErrModel),
		Params: "mode=llm&provider=gemini", WantStatus: http.StatusBadGateway,
		WantBody: "Gemini did not answer", NotBody: []string{"ollama", "returned no text"},
		WantLog: "returned no text",
	}, { // Test 5: The server's default provider is unknown here, so no model is named.
		Err:    fmt.Errorf("llm: %w: anthropic status 500: %s", llm.ErrModel, secret),
		Params: "mode=llm", WantStatus: http.StatusBadGateway,
		WantBody: "The model did not answer", NotBody: []string{"ollama", "Claude", secret, "500"},
		WantLog: "anthropic status 500",
	}, { // Test 6: A policy denial is a 403 that says how to allow the call.
		Err: denied, Params: "mode=llm&provider=anthropic", WantStatus: http.StatusForbidden,
		WantBody: "--policy redacted", NotBody: []string{"ollama", "unavailable"},
	}, { // Test 7: A denial inside a model call is still a policy denial.
		Err:    fmt.Errorf("llm: %w: anthropic request: %w", llm.ErrModel, denied),
		Params: "mode=llm&provider=anthropic", WantStatus: http.StatusForbidden,
		WantBody: "egress denied", NotBody: []string{"ollama", "Claude did not answer"},
	}, { // Test 8: Any other failure stays generic and is logged.
		Err: errors.New("index closed"), Params: "mode=keyword", WantStatus: http.StatusBadGateway,
		WantBody: "the answer service is unavailable", NotBody: []string{"index closed"},
		WantLog: "index closed",
	}, { // Test 9: A bad request is a 400 with its message and no log line.
		Err:    fmt.Errorf("%w: mode %q", ErrBadRequest, "x"),
		Params: "mode=x", WantStatus: http.StatusBadRequest, WantBody: "mode",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
				return resolve.Answer{}, test.Err
			}
			var logBuf bytes.Buffer
			h, err := Handler(Config{Ask: ask, Version: "test", Log: &logBuf})
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			target := "/api/ask?q=" + askQuery + "&" + test.Params
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			body, logged := rec.Body.String(), logBuf.String()
			if rec.Code != test.WantStatus {
				t.Errorf("status = %d, want %d", rec.Code, test.WantStatus)
			}
			if !strings.Contains(body, test.WantBody) {
				t.Errorf("body = %s, want it to contain %q", body, test.WantBody)
			}
			for _, not := range test.NotBody {
				if strings.Contains(strings.ToLower(body), strings.ToLower(not)) {
					t.Errorf("body = %s, want no %q", body, not)
				}
			}
			if test.WantLog == "" && logged != "" {
				t.Errorf("log = %q, want nothing", logged)
			}
			if !strings.Contains(logged, test.WantLog) {
				t.Errorf("log = %q, want it to contain %q", logged, test.WantLog)
			}
			if strings.Contains(logged, askQuery) {
				t.Errorf("log = %q, carries the query", logged)
			}
		})
	}
}

// TestAskAPILogsTheModelFailureBehindAnAnswer checks that an answer ranked
// without the model still reaches the asker, and that the model's failure,
// which the answer carries off the error path, lands in the log without the
// query and never in the response.
func TestAskAPILogsTheModelFailureBehindAnAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ModelErr error
		WantLog  string
	}{{ // Test 0: A model failure behind the answer is logged.
		ModelErr: fmt.Errorf("llm resolve: %w: anthropic status 429: slow down", llm.ErrModel),
		WantLog: "web: model failed, answered without it: " +
			"llm resolve: llm: model error: anthropic status 429",
	}, { // Test 1: An answer the model ranked logs nothing.
		ModelErr: nil, WantLog: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
				return resolve.Answer{Summary: "talk to jane", ModelErr: test.ModelErr}, nil
			}
			var logBuf bytes.Buffer
			h, err := Handler(Config{Ask: ask, Version: "test", Log: &logBuf})
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/ask?q="+askQuery+"&mode=llm&provider=anthropic", nil))
			body, logged := rec.Body.String(), logBuf.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(body, "talk to jane") || strings.Contains(body, "429") {
				t.Errorf("body = %s, want the summary and no model error", body)
			}
			if test.WantLog == "" && logged != "" {
				t.Errorf("log = %q, want nothing", logged)
			}
			if !strings.Contains(logged, test.WantLog) {
				t.Errorf("log = %q, want it to contain %q", logged, test.WantLog)
			}
			if strings.Contains(logged, askQuery) {
				t.Errorf("log = %q, carries the query", logged)
			}
		})
	}
}

// TestHealthAndReady checks the liveness and readiness probes: /healthz is
// always 200 even behind a token, and /readyz reflects the readiness check
// while also bypassing the token.
func TestHealthAndReady(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	get := func(h http.Handler, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	// Liveness is 200 even behind a token and with no readiness check.
	tokened, err := Handler(Config{Ask: ask, AuthToken: "sekret"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if got := get(tokened, "/healthz"); got != http.StatusOK {
		t.Errorf("/healthz behind a token = %d, want 200", got)
	}
	if got := get(tokened, "/readyz"); got != http.StatusOK {
		t.Errorf("/readyz with no check = %d, want 200", got)
	}

	// Readiness reflects the check.
	notReady, _ := Handler(Config{Ask: ask, Ready: func() bool { return false }})
	if got := get(notReady, "/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz not ready = %d, want 503", got)
	}
	ready, _ := Handler(Config{Ask: ask, Ready: func() bool { return true }})
	if got := get(ready, "/readyz"); got != http.StatusOK {
		t.Errorf("/readyz ready = %d, want 200", got)
	}
}

// TestSearchAPI checks /api/search returns ranked results and rejects a missing
// query.
func TestSearchAPI(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	search := func(q string, _ int) []resolve.SearchResult {
		if q != "ada" {
			return nil
		}
		return []resolve.SearchResult{{Kind: "person", ID: "ada@x.io", Name: "Ada", Score: 40, Matched: []string{"name"}}}
	}
	h, err := Handler(Config{Ask: ask, Search: search})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=ada", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/search?q=ada = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ada@x.io") {
		t.Errorf("body missing the result: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("/api/search with no q = %d, want 400", rec.Code)
	}
}

// TestQueryLengthIsBounded checks an over-long question is refused rather than
// answered. Without the bound one request buys an unbounded amount of ranking
// work and an answer just as large, which a public instance pays for.
func TestQueryLengthIsBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("kafka ", 200)
	h, err := Handler(Config{
		Ask: func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
			return resolve.Answer{}, nil
		},
		Search: func(_ string, _ int) []resolve.SearchResult { return nil },
		Recall: func(_ context.Context, _, _ string, _ int) (recall.Answer, error) {
			return recall.Answer{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	tests := []struct {
		Name     string
		Target   string
		WantCode int
	}{{ // Test 0: A question anybody would actually type.
		Name: "normal ask", Target: "/api/ask?q=" + url.QueryEscape("who knows kafka"),
		WantCode: http.StatusOK,
	}, { // Test 1: Past the bound on ask.
		Name: "long ask", Target: "/api/ask?q=" + url.QueryEscape(long), WantCode: http.StatusBadRequest,
	}, { // Test 2: Past the bound on search.
		Name: "long search", Target: "/api/search?q=" + url.QueryEscape(long), WantCode: http.StatusBadRequest,
	}, { // Test 3: Past the bound on recall, which otherwise allows an empty query.
		Name: "long recall", Target: "/api/recall?me=jane@x.com&q=" + url.QueryEscape(long),
		WantCode: http.StatusBadRequest,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.Target, nil))
			if rec.Code != test.WantCode {
				t.Errorf("%s = %d, want %d (body %s)", test.Name, rec.Code, test.WantCode, rec.Body.String())
			}
		})
	}
}

// TestBriefEndpointServesAForwardableFile checks the served brief is the same
// self-contained artifact the CLI writes. The whole point of the page is that
// it survives being forwarded, so it must not depend on the server that made it.
func TestBriefEndpointServesAForwardableFile(t *testing.T) {
	t.Parallel()
	risks := []resolve.TopicRisk{{
		Topic: "billing", Level: "critical", Concentration: 1, BusFactor: 1,
		Experts: []resolve.RiskExpert{{ID: "ada@x.io", Name: "Ada", Share: 1}},
	}}
	h, err := Handler(Config{
		Ask: func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
			return resolve.Answer{}, nil
		},
		Brief: func() report.Brief {
			exposed := report.Exposures(risks)
			return report.Brief{
				Generated: time.Now(), People: 3, Scored: len(risks), Sources: []string{"git"},
				Risks: risks, Totals: report.Count(risks, exposed), Exposed: exposed,
			}
		},
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/report/risk.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("brief = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content type = %q, want html", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("content disposition = %q, want it offered as a download", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "billing") || !strings.Contains(body, "Ada") {
		t.Error("the brief does not contain the finding it was built from")
	}
	for _, external := range []string{"src=\"http", "href=\"http", "fetch("} {
		if strings.Contains(body, external) {
			t.Errorf("the served brief reaches back to the server with %q", external)
		}
	}
}

// TestBriefRouteAbsentWithoutABriefFunc checks the route is not registered when
// the server was not given a way to build one, rather than answering with a
// broken page.
func TestBriefRouteAbsentWithoutABriefFunc(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	testHandler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/report/risk.html", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("brief without a Brief function = %d, want 404", rec.Code)
	}
}

// TestExposureCarriesJoinedWork checks the web view is shown the same finding
// the command line and the brief report. Joined work is the heaviest thing in
// the report, and the exposure view is where somebody looks first, so leaving
// it out of one surface and not the others is how the three drift apart.
func TestExposureCarriesJoinedWork(t *testing.T) {
	t.Parallel()
	h, err := Handler(Config{
		Ask: func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
			return resolve.Answer{}, nil
		},
		Exposure: func() Exposure {
			return Exposure{Regions: []resolve.Region{{
				Lead: "Ada", LeadID: "ada@x.com", Topics: []string{"dlna", "dmr", "dms"},
			}}}
		},
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/exposure", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("exposure = %d, want 200", rec.Code)
	}
	var got Exposure
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Regions) != 1 {
		t.Fatalf("regions = %+v, want the joined work carried through", got.Regions)
	}
	if got.Regions[0].Size() != 3 || got.Regions[0].Lead != "Ada" {
		t.Errorf("region = %+v, want Ada with three joined subjects", got.Regions[0])
	}
}

// TestExposureCarriesOnePersonConnections checks the web view is shown the
// connections that rest on one person, the same as the command line and the
// brief. This was missing from the web view alone for a while: the finding was
// computed, printed, and written into the report, and the one surface most
// people actually look at never mentioned it.
func TestExposureCarriesOnePersonConnections(t *testing.T) {
	t.Parallel()
	h, err := Handler(Config{
		Ask: func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
			return resolve.Answer{}, nil
		},
		Exposure: func() Exposure {
			return Exposure{Spans: []resolve.Span{{
				Topics: []string{"billing", "ledger"}, Person: "Ada", PersonID: "ada@x.com",
				Together: 0.25, Experts: 6,
			}}}
		},
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/exposure", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("exposure = %d, want 200", rec.Code)
	}
	var got Exposure
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want the connection carried through", got.Spans)
	}
	if got.Spans[0].Person != "Ada" || got.Spans[0].Experts != 6 {
		t.Errorf("span = %+v, want Ada named with the six who hold the two subjects", got.Spans[0])
	}
}

// TestExposureViewHasSomewhereToShowEveryFinding checks the page carries a
// container for each thing the exposure payload can report. A finding that
// reaches the browser with nothing to draw it into is invisible, and that reads
// exactly like the finding not existing.
func TestExposureViewHasSomewhereToShowEveryFinding(t *testing.T) {
	t.Parallel()
	h, err := Handler(Config{
		Ask: func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
			return resolve.Answer{}, nil
		},
		Exposure: func() Exposure { return Exposure{} },
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, id := range []string{"exp-risk", "exp-drift", "exp-regions", "exp-spans"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("the page has nowhere to draw %s", id)
		}
	}
}

// TestEveryDirectoryViewCanBeSortedAndExported checks each browsable view knows
// how to order itself and what to put in a file.
//
// readStatic returns a shipped static file, so a test asserts against what the
// browser is actually served rather than against a copy that can drift.
func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// readTemplate returns a shipped page template.
func readTemplate(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.ReadFile("templates/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// Everything whotoask shows is on its way somewhere else: into a ticket, a channel,
// a review document. A view that can only be looked at is half an answer, and the
// failure is silent, since a missing entry here just means the button quietly
// does not appear. This reads the shipped script rather than trusting a comment.
func TestEveryDirectoryViewCanBeSortedAndExported(t *testing.T) {
	t.Parallel()
	app, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(app)

	// The views the directory offers, taken from the script itself.
	block := func(name string) string {
		i := strings.Index(src, "const "+name+" = {")
		if i < 0 {
			t.Fatalf("%s is missing from app.js", name)
		}
		depth, start := 0, strings.Index(src[i:], "{")+i
		for j := start; j < len(src); j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return src[start : j+1]
				}
			}
		}
		t.Fatalf("%s is not closed in app.js", name)
		return ""
	}
	views, sorts, columns := block("DIR_VIEWS"), block("DIR_SORTS"), block("DIR_COLUMNS")

	// The key has to be the whole key. Matching a substring passes on "xtopics"
	// and would have made this whole test decorative.
	declares := func(block, view string) bool {
		return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(view) + `:`).MatchString(block)
	}
	for _, view := range []string{"people", "channels", "teams", "topics"} {
		if !declares(views, view) {
			t.Errorf("%s is not a directory view any more; fix this test", view)
			continue
		}
		if !declares(sorts, view) {
			t.Errorf("the %s view has no sort orders, so it can only be read one way", view)
		}
		if !declares(columns, view) {
			t.Errorf("the %s view cannot be exported, so what it shows cannot leave the page", view)
		}
	}
	// The mechanisms themselves have to be there for any of that to work. They
	// live in portable.js because the org chart needs them too, and two copies
	// would drift apart the first time one of them was fixed.
	portable := readStatic(t, "portable.js")
	for _, fn := range []string{"function el(", "function copyButton(", "function csvCell(",
		"function downloadCSV(", "function exportButton("} {
		if !strings.Contains(portable, fn) {
			t.Errorf("portable.js has no %s", fn)
		}
		if strings.Contains(src, fn) {
			t.Errorf("app.js redeclares %s; it belongs only in portable.js", fn)
		}
		if strings.Contains(readStatic(t, "orgchart.js"), fn) {
			t.Errorf("orgchart.js redeclares %s; it belongs only in portable.js", fn)
		}
	}
	// The CSV export must neutralize a leading formula character. A git author
	// named "=HYPERLINK(...)" executes the moment the export opens in a
	// spreadsheet, and quoting alone does not stop it.
	if !strings.Contains(portable, `/^[=+\-@\t\r]/`) {
		t.Error("csvCell no longer guards leading formula characters; " +
			"a cell starting with = + - @ runs as a formula when the export opens")
	}
	// A shared file nobody loads is worse than no shared file, since every call
	// through it is a ReferenceError the moment someone clicks.
	for _, page := range []string{"index.html", "orgchart.html"} {
		tpl := readTemplate(t, page)
		if !strings.Contains(tpl, "/static/portable.js") {
			t.Errorf("%s does not load portable.js, so copy and export are broken on that page", page)
		}
	}
}

// TestTheOrgChartCanBeTakenWithYou checks the chart offers a way out: the seats
// on screen as a spreadsheet, and a person as a block of text. The chart is
// where the knowledge-risk finding surfaces, and a finding nobody can carry into
// a meeting is a finding nobody acts on.
func TestTheOrgChartCanBeTakenWithYou(t *testing.T) {
	t.Parallel()
	js, tpl := readStatic(t, "orgchart.js"), readTemplate(t, "orgchart.html")

	if !strings.Contains(tpl, `id="oc-export"`) {
		t.Error("the org chart has no export control")
	}
	if !strings.Contains(js, `byId("oc-export"`) {
		t.Error("the export control is not wired to anything")
	}
	if !strings.Contains(js, "function exportVisible(") {
		t.Error("orgchart.js has no exportVisible")
	}
	// The export is only worth having if it carries the risk findings, which are
	// the reason to draw this chart rather than any other org chart.
	for _, col := range []string{"Holds alone", "Gone quiet", "Reports to"} {
		if !strings.Contains(js, `"`+col+`"`) {
			t.Errorf("the org chart export has no %q column", col)
		}
	}
	// The panel has to say what the bar on the seat means, not leave it to a
	// tooltip that never appears on a touch screen.
	for _, want := range []string{"Only person holding", "gone quiet on"} {
		if !strings.Contains(strings.ToLower(js), strings.ToLower(want)) {
			t.Errorf("the detail panel never shows %q", want)
		}
	}
	if !strings.Contains(js, "copyButton(") {
		t.Error("a person in the org chart cannot be copied")
	}
}

// TestEveryInsightCardCanBeCopied covers the four findings that exist to be
// carried into a room: a body of work resting on one person, a crossing only one
// person has made, a subject with a low bus factor, and an area whose declared
// owner is not the one doing the work. They are the least useful findings to be
// able only to look at, and were the last four cards in the app with no way out.
func TestEveryInsightCardCanBeCopied(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")

	// checkDeparture is not a card, but it renders the finding most likely to be
	// written down somewhere else: what leaves with a person, which goes into a
	// handover note or a backfill request.
	for _, card := range []string{"regionCard", "spanCard", "riskCard", "driftCard", "checkDeparture"} {
		start := strings.Index(src, "function "+card+"(")
		if start < 0 {
			start = strings.Index(src, "async function "+card+"(")
		}
		if start < 0 {
			t.Errorf("%s is gone; fix this test", card)
			continue
		}
		// The card body runs to the next top-level close brace.
		end := strings.Index(src[start:], "\n}")
		if end < 0 {
			t.Errorf("%s is not closed", card)
			continue
		}
		if !strings.Contains(src[start:start+end], "copyButton(") {
			t.Errorf("%s has no copy control, so the finding cannot leave the screen", card)
		}
	}
}

// TestTheNavSaysWhichQuestionEachGroupAnswers covers the sidebar's grouping.
// whotoask answers three different questions for three different people, and a
// flat list of views hides that: who holds what, how something was done last
// time, and where the organization is thin.
//
// The failure worth guarding is an orphan. Two of the three groups only exist
// when the feature behind them is configured, so a heading has to disappear
// with its section rather than sit above nothing.
func TestTheNavSaysWhichQuestionEachGroupAnswers(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	render := func(t *testing.T, cfg Config) string {
		t.Helper()
		cfg.Ask, cfg.Version = ask, "test"
		h, err := Handler(cfg)
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		return rec.Body.String()
	}

	full := render(t, Config{
		Recall:   func(context.Context, string, string, int) (recall.Answer, error) { return recall.Answer{}, nil },
		Exposure: func() Exposure { return Exposure{} },
	})
	for _, group := range []string{"Who knows what", "How it was done", "Where it is thin"} {
		if !strings.Contains(full, group) {
			t.Errorf("the sidebar never says %q, so the three questions are invisible again", group)
		}
	}
	// The org chart belongs under the third question: what it now shows that no
	// other org chart does is which seats hold something alone.
	thin := strings.Index(full, "Where it is thin")
	if chart := strings.Index(full, `href="/orgchart"`); thin < 0 || chart < thin {
		t.Error("the org chart is not under the group about where the organization is thin")
	}

	// With recall off, its heading has to go with it.
	bare := render(t, Config{})
	if strings.Contains(bare, "How it was done") {
		t.Error("the recall heading renders with no recall view under it")
	}
	if !strings.Contains(bare, "Who knows what") {
		t.Error("the directory heading vanished; it does not depend on anything optional")
	}
}

// TestEveryNamedPersonIsAWayToThatPerson covers the deep link from a result. A
// name is the most useful thing on the screen and was the most often dead: a
// channel listed its active members as plain text, and each knowledge-risk
// finding named the person it rests on with no way to find out who they are.
func TestEveryNamedPersonIsAWayToThatPerson(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")

	if !strings.Contains(src, "function personLink(") {
		t.Fatal("app.js has no personLink; names cannot be reached")
	}
	// One helper, so a card added later gets the behavior by using it rather
	// than by remembering to wire a click handler.
	for _, card := range []string{"channelCard", "regionCard", "spanCard", "driftCard"} {
		start := strings.Index(src, "function "+card+"(")
		if start < 0 {
			t.Errorf("%s is gone; fix this test", card)
			continue
		}
		end := strings.Index(src[start:], "\n}")
		if end < 0 {
			t.Errorf("%s is not closed", card)
			continue
		}
		if !strings.Contains(src[start:start+end], "personLink(") {
			t.Errorf("%s names a person but offers no way to reach them", card)
		}
	}
	// A name with no identifier behind it has to stay plain text. A button that
	// opens nothing is worse than a label, since it invites the click.
	link := src[strings.Index(src, "function personLink("):]
	link = link[:strings.Index(link, "\n}")]
	if !strings.Contains(link, "if (!id)") {
		t.Error("personLink does not fall back to plain text without an id")
	}
}

// TestRecallCanBeTakenWithYou covers the trenches half of the product. "How did
// we solve this last time" is an answer that goes into a runbook or a reply, so
// a whole session has to leave, not one card at a time.
func TestRecallCanBeTakenWithYou(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")
	if !strings.Contains(src, `exportButton("Export"`) {
		t.Error("a recall session cannot be exported")
	}
	if !strings.Contains(src, "whotoask-recall.csv") {
		t.Error("the recall export writes no file")
	}
	// The export has to match the filters on screen. Dumping everything fetched
	// would quietly contradict what the reader is looking at.
	if !strings.Contains(src, "currentRecallEpisodes()") {
		t.Error("the recall export does not follow the filters on screen")
	}
}

// TestEmptyViewsSayWhatWouldFillThem checks a view with nothing in it names the
// source that would fill it. An empty screen is nearly always a setup problem,
// and the person looking at it is the one who can fix it.
func TestEmptyViewsSayWhatWouldFillThem(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")
	for _, want := range []string{
		"whotoask index --source git",   // people
		"whotoask index --source slack", // channels
		"a source of record",            // teams
		"index a CODEOWNERS file",       // ownership drift
	} {
		if !strings.Contains(src, want) {
			t.Errorf("no empty state mentions %q, so a blank view says nothing about how to fill it", want)
		}
	}
	// The old bare form said only that there was nothing.
	if strings.Contains(src, `"No people indexed yet."`) {
		t.Error("the people view still has the bare empty state")
	}
}

// TestKeyboardShortcutsAreDiscoverable checks the page says what keys it answers
// to. A shortcut nobody can find is a shortcut nobody uses.
func TestKeyboardShortcutsAreDiscoverable(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")
	if !strings.Contains(src, "function showShortcuts(") {
		t.Fatal("there is no shortcuts list")
	}
	// A shortcut must never eat a character while somebody is typing a question.
	if !strings.Contains(src, "function typing(") || !strings.Contains(src, "if (typing()") {
		t.Error("shortcuts do not check whether the caret is in a field")
	}
	// A modifier chord belongs to the browser or the operating system.
	if !strings.Contains(src, "event.metaKey || event.ctrlKey || event.altKey") {
		t.Error("shortcuts fire with a modifier held, stealing browser chords")
	}
}

// TestTheMobileTopBarDropsTheNavGroups checks the sidebar's group headings are
// solid bar with a real menu button, and the nav as a solid sheet the button
// opens. The two earlier shapes both failed by eye: a wrapping wall of links,
// then a rail of outlined pills the user called noisy. The contract now: the
// nav is hidden until the menu opens, the sheet is solid (an explicit
// background, never translucent over content), the group headings return
// inside the sheet, and the header cannot stretch past the viewport.
func TestTheMobileTopBarKeepsItsShape(t *testing.T) {
	t.Parallel()
	css := readStatic(t, "style.css")
	i := strings.Index(css, "@media (max-width: 720px)")
	if i < 0 {
		t.Fatal("the small-screen breakpoint is gone; fix this test")
	}
	block := css[i:]
	if end := strings.Index(block, "\n}\n"); end > 0 {
		block = block[:end]
	}
	for _, want := range []string{
		"#side-nav { display: none; }",
		"body.menu-open #side-nav",
		"body.menu-open .nav-group",
		"background: var(--bg)",
		"min-width: 0",
		"max-width: 100%",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("mobile header block lost %q, part of the bar-and-sheet contract", want)
		}
	}
	html := readTemplate(t, "index.html")
	if !strings.Contains(html, `id="side-menu-btn"`) {
		t.Error("the menu button is gone from the template")
	}
	js := readStatic(t, "app.js")
	if !strings.Contains(js, "menu-open") {
		t.Error("nothing toggles the menu sheet")
	}
}

// TestModalsAreRealDialogs covers what opening a panel does to the keyboard.
// Without dialog semantics the caret stays behind the modal: Tab walks the page
// underneath, closing starts again from the top, and nothing announces that
// anything opened. Both panels go through one pair of helpers so a third cannot
// be added that quietly skips it.
func TestModalsAreRealDialogs(t *testing.T) {
	t.Parallel()
	src := readStatic(t, "app.js")

	for _, want := range []string{
		`setAttribute("role", "dialog")`,
		`setAttribute("aria-modal", "true")`,
		"function trapTab(",
		"function openModal(",
		"function closeModal(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js is missing %s", want)
		}
	}
	// Focus has to go back where it came from, or the keyboard restarts at the
	// top of the page every time a profile is closed.
	if !strings.Contains(src, "modalReturn") || !strings.Contains(src, "modalReturn.focus()") {
		t.Error("closing a dialog does not return focus to whatever opened it")
	}
	// No panel may be appended straight to the body, which is how one ends up
	// without any of the above.
	if n := strings.Count(src, "document.body.appendChild(backdrop)"); n != 1 {
		t.Errorf("%d places append a backdrop directly; only openModal may", n)
	}
}

// TestTheStoryWalksAllThreeQuestions covers the guided walk: the product's
// whole thesis as five steps over live data. Reviews of the demo kept landing
// on the same gap: the capabilities are all there and the visitor has to
// assemble the narrative alone. The story is that assembly, so it has to reach
// each of the three questions, run on live data rather than a script, and be
// reachable from the page.
func TestTheStoryWalksAllThreeQuestions(t *testing.T) {
	t.Parallel()
	js := readStatic(t, "story.js")
	tpl := readTemplate(t, "index.html")

	if !strings.Contains(tpl, `id="story-btn"`) || !strings.Contains(tpl, "story.js") {
		t.Error("the story is not reachable from the page")
	}
	// One step per question, plus the why and the close.
	for _, want := range []string{
		"#people .card",      // ask: a ranked person
		".chips",             // why: the reasons are the ranking
		"#recall-list .card", // how it was solved last time
		"#exp-dep-result",    // what leaves if they leave
		"go install",         // the close points at installing
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the story never reaches %q", want)
		}
	}
	// Live data, not a script: the walk reads the ask API for its cast.
	if !strings.Contains(js, "/api/ask?q=") {
		t.Error("the story hardcodes its cast instead of reading the live answer")
	}
	// It must be leavable: Escape and a skip control.
	if !strings.Contains(js, `"Escape"`) || !strings.Contains(js, `"skip"`) {
		t.Error("the story cannot be escaped")
	}
}

// TestListsScanBeforeTheyRead pins the density decisions a directory and a
// recall list depend on. Both regressed into walls once: a roster of hundreds
// with two copy buttons per row, and conversation cards where thirty
// participant names buried the conversation itself.
func TestListsScanBeforeTheyRead(t *testing.T) {
	t.Parallel()
	js := readStatic(t, "app.js")

	// Test 0: People carry a face everywhere they are listed, so the answer
	// list and the roster read as the same product.
	if !strings.Contains(js, "function personAvatar") {
		t.Error("the avatar helper is gone")
	}
	for _, fn := range []string{"function personCard", "function dirPersonRow"} {
		i := strings.Index(js, fn)
		if i < 0 {
			t.Fatalf("%s is gone", fn)
		}
		body := js[i:min(i+1400, len(js))]
		if !strings.Contains(body, "personAvatar") {
			t.Errorf("%s no longer shows a face", fn)
		}
	}

	// Test 1: The roster row keeps one copy control, not one per field.
	i := strings.Index(js, "function dirPersonRow")
	row := js[i:min(i+1400, len(js))]
	if n := strings.Count(row, "copyButton("); n != 1 {
		t.Errorf("roster row has %d copy buttons, want exactly 1", n)
	}

	// Test 2: A long participant list is summarized, with the rest one click
	// away rather than gone.
	if !strings.Contains(js, "recallPeopleLine") || !strings.Contains(js, "others") {
		t.Error("recall no longer clamps its participant list")
	}
	if !strings.Contains(js, "show fewer") {
		t.Error("the clamped participant list cannot be expanded back")
	}
}

// TestTheProfileReadsAsAPerson pins what a profile panel owes a reader: a
// face like every other person surface, and the cross-tool identity join shown
// as named sources rather than as a raw comma-separated dump, since that join
// is the product working and not debug output.
func TestTheProfileReadsAsAPerson(t *testing.T) {
	t.Parallel()
	js := readStatic(t, "app.js")
	i := strings.Index(js, "function showProfile")
	if i < 0 {
		t.Fatal("showProfile is gone")
	}
	body := js[i:min(i+3000, len(js))]
	if !strings.Contains(body, "personAvatar") {
		t.Error("the profile panel shows no face")
	}
	if strings.Contains(body, `p.identities.join(", ")`) {
		t.Error("identities render as a raw comma run again")
	}
	if !strings.Contains(body, "chip-id") {
		t.Error("identities no longer render as source-named chips")
	}
	if strings.Contains(body, `p.channels.map((c) => "#" + c).join(", ")`) {
		t.Error("channels render as a raw comma run again")
	}
}

// TestVotingFollowsTheFeedbackRoute verifies the page only offers voting when
// the server actually records votes, so a public demo shows no dead buttons.
func TestVotingFollowsTheFeedbackRoute(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	tests := []struct {
		Vote       FeedbackFunc
		WantMarker bool
	}{{ // Test 0: No feedback route, no voting marker.
		Vote: nil, WantMarker: false,
	}, { // Test 1: A feedback route stamps the marker.
		Vote: func(feedback.Entry) error { return nil }, WantMarker: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			h, err := Handler(Config{Ask: ask, Version: "test", Feedback: test.Vote})
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := strings.Contains(rec.Body.String(), "data-voting"); got != test.WantMarker {
				t.Errorf("data-voting present = %v, want %v", got, test.WantMarker)
			}
		})
	}
}

// TestRelatedClampsTheLimit verifies an oversized limit falls back to the
// default instead of reaching the resolver.
func TestRelatedClampsTheLimit(t *testing.T) {
	t.Parallel()
	ask := func(_ context.Context, _, _, _ string, _ int) (resolve.Answer, error) {
		return resolve.Answer{}, nil
	}
	got := 0
	h, err := Handler(Config{Ask: ask, Version: "test",
		Related: func(_ string, limit int) []resolve.TopicRelation {
			got = limit
			return nil
		}})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/related?topic=x&limit=999999", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got != 8 {
		t.Errorf("resolver limit = %d, want the default 8", got)
	}
}
