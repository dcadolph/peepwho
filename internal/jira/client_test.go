package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestSearch verifies issue fields decode, including the assignee email.
func TestSearch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":1,"startAt":0,"issues":[{"key":"SEC-1","fields":{`+
			`"summary":"Wiz scan flaky",`+
			`"assignee":{"accountId":"a1","displayName":"Jane Roe","emailAddress":"jane@x.com"},`+
			`"components":[{"name":"scanning"}],"labels":["wiz"],`+
			`"project":{"key":"SEC","name":"Security"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "me@x.com", "token")
	issues, err := c.Search(context.Background(), "project = SEC", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	got := issues[0]
	if got.Key != "SEC-1" || got.Fields.Assignee == nil ||
		got.Fields.Assignee.EmailAddress != "jane@x.com" {
		t.Errorf("issue = %+v", got)
	}
	if len(got.Fields.Components) != 1 || got.Fields.Components[0].Name != "scanning" {
		t.Errorf("components = %v", got.Fields.Components)
	}
}

// TestSearchPagination verifies Cloud issues accumulate across pages by
// following the nextPageToken until a page reports it is the last, which is how
// the enhanced-search endpoint pages.
func TestSearchPagination(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("nextPageToken")
		mu.Lock()
		gotToken = token
		mu.Unlock()
		if token == "" {
			io.WriteString(w, `{"issues":[{"key":"A-1","fields":{}}],"nextPageToken":"tok2","isLast":false}`)
			return
		}
		io.WriteString(w, `{"issues":[{"key":"A-2","fields":{}}],"isLast":true}`)
	}))
	t.Cleanup(srv.Close)

	issues, err := New(srv.URL, "me@x.com", "token").Search(context.Background(), "order by updated", 0)
	if err != nil || len(issues) != 2 {
		t.Fatalf("issues = %d, err %v; want 2", len(issues), err)
	}
	mu.Lock()
	got := gotToken
	mu.Unlock()
	if got != "tok2" {
		t.Errorf("second page token = %q, want the client to send back tok2", got)
	}
}

// TestSearchTokenTruncated verifies the Cloud path reports a capped read: the
// endpoint carries no total, so the error says more matched without a count,
// and the issues read so far come back alongside it.
func TestSearchTokenTruncated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"issues":[{"key":"A-1","fields":{}}],"nextPageToken":"more","isLast":false}`)
	}))
	t.Cleanup(srv.Close)

	issues, err := New(srv.URL, "me@x.com", "token").Search(context.Background(), "x", 1)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Search err = %v, want ErrTruncated when the cap cut a longer result", err)
	}
	if len(issues) != 1 {
		t.Errorf("issues = %d, want the capped page returned alongside the truncation", len(issues))
	}
}

// TestNewCloudUsesEnhancedSearch verifies the Cloud client calls the token-paged
// /search/jql endpoint, not the offset /search that Atlassian removed from
// Cloud and that now returns 410 Gone there.
func TestNewCloudUsesEnhancedSearch(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"issues":[],"isLast":true}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL, "me@x.com", "token").Search(context.Background(), "", 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := apiBaseCloud + "/search/jql"; gotPath != want {
		t.Errorf("path = %q, want the enhanced-search endpoint %q", gotPath, want)
	}
}

// TestRetryAfter verifies a 429 is retried and then succeeds.
func TestRetryAfter(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := calls
		calls++
		mu.Unlock()
		if i == 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"total":0,"issues":[]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL, "me@x.com", "token").Search(context.Background(), "x", 0); err != nil {
		t.Fatalf("Search after 429: %v", err)
	}
}

// TestNewEmptyPanics verifies the constructor guards empty arguments.
func TestNewEmptyPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New with empty args did not panic")
		}
	}()
	New("", "e", "t")
}

// TestSearchRequestsEveryFieldItDecodes guards the bug class that made whotoask
// produce no Jira episodes at all: Jira returns only the fields the query
// names, so a field the Issue struct decodes but the query omits is silently
// always empty, and nothing in a mock-backed test reveals it. Reflection keeps
// the query honest as fields are added.
func TestSearchRequestsEveryFieldItDecodes(t *testing.T) {
	t.Parallel()
	asked := make(map[string]bool)
	for _, name := range strings.Split(searchFields, ",") {
		asked[strings.TrimSpace(name)] = true
	}
	fields := reflect.TypeOf(Issue{}.Fields)
	for i := range fields.NumField() {
		name, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !asked[name] {
			t.Errorf("Issue decodes %q but Search does not request it, so it is always empty", name)
		}
	}
}

// TestSearchResolvedFieldsRoundTrip verifies an issue that a real Jira reports
// as resolved decodes as resolved, through both the resolution date and the
// status category a custom workflow uses.
func TestSearchResolvedFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	var gotFields string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFields = r.URL.Query().Get("fields")
		io.WriteString(w, `{"total":3,"startAt":0,"issues":[`+
			`{"key":"OPS-1","fields":{"summary":"Retries storm",`+
			`"resolutiondate":"2026-06-20T09:30:00.000-0500",`+
			`"status":{"name":"Done","statusCategory":{"key":"done"}}}},`+
			`{"key":"OPS-2","fields":{"summary":"Shipped",`+
			`"status":{"name":"Released","statusCategory":{"key":"done"}}}},`+
			`{"key":"OPS-3","fields":{"summary":"Still open",`+
			`"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}}}]}`)
	}))
	t.Cleanup(srv.Close)

	issues, err := New(srv.URL, "me@x.com", "token").Search(context.Background(), "", 0)
	if err != nil || len(issues) != 3 {
		t.Fatalf("Search = %d issues, err %v", len(issues), err)
	}
	if !strings.Contains(gotFields, "resolutiondate") || !strings.Contains(gotFields, "status") {
		t.Fatalf("fields param = %q, missing what Resolved reads", gotFields)
	}
	if !issues[0].Resolved() {
		t.Error("issue with a resolution date did not report resolved")
	}
	if !issues[1].Resolved() {
		t.Error("issue in a done status category did not report resolved")
	}
	if issues[2].Resolved() {
		t.Error("in-progress issue reported resolved")
	}
}

// TestUserIdentity verifies the identity fallback across deployments: Cloud
// keys on the account id, Server and Data Center on the username or key.
func TestUserIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   User
		Want string
	}{
		{User{AccountID: "acc-1", Name: "jdoe", Key: "jdoe-key"}, "acc-1"}, // Test 0: Cloud prefers account id.
		{User{Name: "jdoe", Key: "jdoe-key"}, "jdoe"},                      // Test 1: Server uses username.
		{User{Key: "jdoe-key"}, "jdoe-key"},                                // Test 2: older Server uses key.
		{User{}, ""},                                                       // Test 3: nothing to key on.
	}
	for testNum, test := range tests {
		if got := test.In.Identity(); got != test.Want {
			t.Errorf("test %d: Identity() = %q, want %q", testNum, got, test.Want)
		}
	}
}

// TestNewServerSpeaksV2Anonymously verifies the Server client hits the v2 API,
// sends no auth header when the token is empty, and still reads issues. This is
// the shape of a public tracker such as an open-source project's Jira.
func TestNewServerSpeaksV2Anonymously(t *testing.T) {
	t.Parallel()
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		// Server/DC shape: a username, no account id, no email, string description.
		io.WriteString(w, `{"total":1,"startAt":0,"issues":[{"key":"KAFKA-1","fields":{`+
			`"summary":"rebalance storm","description":"fixed the coordinator",`+
			`"assignee":{"name":"jdoe","displayName":"Jane Doe"},`+
			`"resolutiondate":"2026-06-20T09:30:00.000+0000",`+
			`"status":{"statusCategory":{"key":"done"}},`+
			`"project":{"key":"KAFKA","name":"Kafka"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := NewServer(srv.URL, "")
	issues, err := c.Search(context.Background(), "project=KAFKA", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.HasPrefix(gotPath, apiBaseServer) {
		t.Errorf("path = %q, want the v2 API base %q", gotPath, apiBaseServer)
	}
	if gotAuth != "" {
		t.Errorf("anonymous client sent an Authorization header: %q", gotAuth)
	}
	if len(issues) != 1 || !issues[0].Resolved() {
		t.Fatalf("issues = %+v, want one resolved", issues)
	}
	if got := issues[0].Fields.Assignee.Identity(); got != "jdoe" {
		t.Errorf("assignee identity = %q, want the username jdoe", got)
	}
	if got := issues[0].Description(); got != "fixed the coordinator" {
		t.Errorf("description = %q, want the string form read", got)
	}
}

// TestNewServerBearerToken verifies a token is sent as a bearer, which is how
// Server and Data Center personal access tokens authenticate.
func TestNewServerBearerToken(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"total":0,"startAt":0,"issues":[]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := NewServer(srv.URL, "PAT123").Search(context.Background(), "", 10); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer PAT123" {
		t.Errorf("Authorization = %q, want Bearer PAT123", gotAuth)
	}
}
