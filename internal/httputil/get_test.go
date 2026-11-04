package httputil

import (
	"context"
	"testing"
)

// TestGetSetsHeadersAndSkipsEmpty checks the shared request builder sets what
// it is given and quietly leaves out what it is not. Clients guarded their own
// credential header with an if, and the guard belongs here so no client has to
// remember it.
func TestGetSetsHeadersAndSkipsEmpty(t *testing.T) {
	t.Parallel()
	req, err := Get(context.Background(), "https://example.test/x",
		"Authorization", "", "Accept", "application/json")()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want it set", got)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Error("an empty credential was sent as a header, want it left out")
	}
	if req.URL.String() != "https://example.test/x" {
		t.Errorf("url = %q, want the one asked for", req.URL)
	}
}

// TestGetIgnoresAnOddTrailingName checks a name with no value cannot crash a
// connector midway through a long read.
func TestGetIgnoresAnOddTrailingName(t *testing.T) {
	t.Parallel()
	req, err := Get(context.Background(), "https://example.test/x",
		"Accept", "application/json", "X-Dangling")()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want the complete pair still set", got)
	}
}

// TestGetRefusesABadURL checks the error a caller already handled still
// arrives, rather than a nil request reaching the transport.
func TestGetRefusesABadURL(t *testing.T) {
	t.Parallel()
	req, err := Get(context.Background(), "://not a url")()
	if err == nil {
		t.Fatalf("a malformed url built a request: %v", req)
		return
	}
}
