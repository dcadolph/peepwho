package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeDoer serves canned bodies in order, one per call.
type fakeDoer struct {
	bodies []string
	calls  int
}

func (f *fakeDoer) Do(*http.Request) (*http.Response, error) {
	body := f.bodies[f.calls]
	f.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// TestUsersPaginatesAndMapsManager confirms Users follows the nextLink and reads
// the expanded manager email, with the sign-in name as an email fallback.
func TestUsersPaginatesAndMapsManager(t *testing.T) {
	t.Parallel()
	page1 := `{"value":[
	  {"id":"1","displayName":"Alice","mail":"alice@x.com","jobTitle":"Eng","department":"Payments","manager":{"mail":"boss@x.com"}}
	],"@odata.nextLink":"https://graph.microsoft.com/v1.0/users?page=2"}`
	page2 := `{"value":[{"id":"2","displayName":"Bob","userPrincipalName":"bob@x.com"}]}`
	c := New("tok", WithHTTPClient(&fakeDoer{bodies: []string{page1, page2}}))

	users, err := c.Users(context.Background())
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d, want 2", len(users))
	}
	if got := users[0].Email(); got != "alice@x.com" {
		t.Errorf("user0 email = %q, want alice@x.com", got)
	}
	if got := users[0].ManagerEmail(); got != "boss@x.com" {
		t.Errorf("user0 manager = %q, want boss@x.com", got)
	}
	if got := users[1].Email(); got != "bob@x.com" {
		t.Errorf("user1 email = %q, want bob@x.com (UPN fallback)", got)
	}
	if got := users[1].ManagerEmail(); got != "" {
		t.Errorf("user1 manager = %q, want empty", got)
	}
}

// TestUsersRefusesForeignNextLink verifies a nextLink pointing off the Graph
// host fails the read instead of being followed: the request carries the
// bearer token, and the link is server-supplied, so following it verbatim
// would send the credential wherever the value pointed.
func TestUsersRefusesForeignNextLink(t *testing.T) {
	t.Parallel()
	page1 := `{"value":[{"id":"1","displayName":"Alice","mail":"alice@x.com"}],
		"@odata.nextLink":"https://evil.example/v1.0/users?page=2"}`
	leaked := false
	doer := &fakeDoer{bodies: []string{page1, `{"value":[]}`}}
	c := New("tok", WithHTTPClient(hookDoer{inner: doer, hook: func(r *http.Request) {
		if r.URL.Host == "evil.example" {
			leaked = true
		}
	}}))

	_, err := c.Users(context.Background())
	if !errors.Is(err, ErrPagination) {
		t.Fatalf("Users err = %v, want ErrPagination", err)
	}
	if !strings.Contains(err.Error(), "evil.example") {
		t.Errorf("error does not name the host refused: %v", err)
	}
	if leaked {
		t.Error("a request was sent to the foreign host; the token went with it")
	}
}

// hookDoer calls hook with each request before delegating, so a test can see
// where requests were about to go.
type hookDoer struct {
	// inner performs the request.
	inner *fakeDoer
	// hook observes each request.
	hook func(*http.Request)
}

// Do observes the request and delegates to the inner doer.
func (h hookDoer) Do(r *http.Request) (*http.Response, error) {
	h.hook(r)
	return h.inner.Do(r)
}

// TestUsersStopsARunawayCursorChain verifies a listing whose nextLink never
// ends fails loudly at the page bound rather than looping forever.
func TestUsersStopsARunawayCursorChain(t *testing.T) {
	t.Parallel()
	bodies := make([]string, maxUserPages+1)
	for i := range bodies {
		bodies[i] = fmt.Sprintf(`{"value":[{"id":"%d"}],
			"@odata.nextLink":"https://graph.microsoft.com/v1.0/users?page=%d"}`, i, i+1)
	}
	c := New("tok", WithHTTPClient(&fakeDoer{bodies: bodies}))
	_, err := c.Users(context.Background())
	if !errors.Is(err, ErrPagination) {
		t.Fatalf("Users err = %v, want ErrPagination after the page bound", err)
	}
}
