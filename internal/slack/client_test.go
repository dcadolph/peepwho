package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// canned is a scripted HTTP response for the test server.
type canned struct {
	// status is the HTTP status code.
	status int
	// retryAfter sets the Retry-After header when non-empty.
	retryAfter string
	// body is the response body.
	body string
}

// newServer returns a test server that replays, per path, the scripted
// responses in order, repeating the last entry once exhausted.
func newServer(t *testing.T, scripts map[string][]canned) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	idx := make(map[string]int)
	mux := http.NewServeMux()
	for path, seq := range scripts {
		mux.HandleFunc("/"+path, func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			i := idx[path]
			if i < len(seq)-1 {
				idx[path]++
			}
			mu.Unlock()
			c := seq[i]
			if c.retryAfter != "" {
				w.Header().Set("Retry-After", c.retryAfter)
			}
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestUsersPaginationFilter verifies paging, that bots drop, and that a
// deactivated member is kept. Dropping the deactivated once made a departed
// person's messages resolve to an opaque slack:UID that joined none of their
// other sources, while the export path kept them, so the same workspace
// indexed differently depending on how it was read.
func TestUsersPaginationFilter(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"users.list": {
			{status: 200, body: `{"ok":true,"response_metadata":{"next_cursor":"c2"},"members":[
				{"id":"U1","profile":{"real_name":"Jane","email":"jane@x.com","title":"Eng"}},
				{"id":"U2","is_bot":true},
				{"id":"U3","deleted":true,"profile":{"real_name":"Dee","email":"dee@x.com"}}]}`},
			{status: 200, body: `{"ok":true,"members":[{"id":"U4","profile":{"real_name":"Bob"}}]}`},
		},
	})
	c := New("xoxb-test", WithBaseURL(srv.URL))
	got, err := c.Users(context.Background())
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(got) != 3 || got[0].ID != "U1" || got[1].ID != "U3" || got[2].ID != "U4" {
		t.Fatalf("users = %+v, want U1, the deactivated U3, and U4", got)
	}
	if !got[1].Deleted || got[1].Profile.Email != "dee@x.com" {
		t.Errorf("deactivated user = %+v, want kept with email; leavers are the point", got[1])
	}
	if got[0].Profile.Email != "jane@x.com" {
		t.Errorf("email = %q, want jane@x.com", got[0].Profile.Email)
	}
}

// loopDoer answers every request with the same body, so a cursor chain can
// run for as long as the client is willing to follow it.
type loopDoer struct {
	// body is the response served to every request.
	body string
}

// Do serves the canned body.
func (l loopDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(l.body)),
		Header:     make(http.Header),
	}, nil
}

// TestUsersStopARunawayCursorChain verifies a users listing whose cursor never
// ends returns what it read alongside ErrTruncated at the page bound, rather
// than looping forever or passing the partial list off as complete.
func TestUsersStopARunawayCursorChain(t *testing.T) {
	t.Parallel()
	c := New("xoxb-test", WithHTTPClient(loopDoer{
		body: `{"ok":true,"response_metadata":{"next_cursor":"again"},` +
			`"members":[{"id":"U1","profile":{"real_name":"Jane"}}]}`,
	}))
	got, err := c.Users(context.Background())
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Users err = %v, want ErrTruncated at the page bound", err)
	}
	if len(got) == 0 {
		t.Error("the pages read before the bound were not returned")
	}
	if _, err := c.Channels(context.Background(), "public_channel"); !errors.Is(err, ErrTruncated) {
		t.Errorf("Channels err = %v, want ErrTruncated on the same endless cursor", err)
	}
}

// TestChannelsPagination verifies channels accumulate across pages.
func TestChannelsPagination(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"conversations.list": {
			{status: 200, body: `{"ok":true,"response_metadata":{"next_cursor":"c2"},
				"channels":[{"id":"C1","name":"billing","topic":{"value":"retries"}}]}`},
			{status: 200, body: `{"ok":true,"channels":[{"id":"C2","name":"infra"}]}`},
		},
	})
	c := New("xoxb-test", WithBaseURL(srv.URL))
	got, err := c.Channels(context.Background(), "public_channel")
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if len(got) != 2 || got[0].Name != "billing" || got[0].Topic.Value != "retries" {
		t.Fatalf("channels = %+v, want billing+infra", got)
	}
}

// TestHistoryMaxAndOldest verifies the max cap and that oldest is forwarded.
func TestHistoryMaxAndOldest(t *testing.T) {
	t.Parallel()
	var gotOldest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOldest = r.FormValue("oldest")
		io.WriteString(w, `{"ok":true,"has_more":false,"messages":[
			{"text":"a"},{"text":"b"},{"text":"c"},{"text":"d"},{"text":"e"}]}`)
	}))
	t.Cleanup(srv.Close)

	c := New("xoxb-test", WithBaseURL(srv.URL))
	got, err := c.History(context.Background(), "C1", "123.456", 3)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("messages = %d, want 3 (capped)", len(got))
	}
	if gotOldest != "123.456" {
		t.Errorf("oldest = %q, want 123.456", gotOldest)
	}
}

// TestRetryOn429 verifies a 429 is retried and then succeeds.
func TestRetryOn429(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"users.list": {
			{status: 429, retryAfter: "0"},
			{status: 200, body: `{"ok":true,"members":[{"id":"U1","profile":{"real_name":"Jane"}}]}`},
		},
	})
	c := New("xoxb-test", WithBaseURL(srv.URL))
	got, err := c.Users(context.Background())
	if err != nil {
		t.Fatalf("Users after 429: %v", err)
	}
	if len(got) != 1 || got[0].ID != "U1" {
		t.Fatalf("users = %+v, want U1", got)
	}
}

// TestAPIError verifies a logical Slack error maps to ErrAPI.
func TestAPIError(t *testing.T) {
	t.Parallel()
	srv := newServer(t, map[string][]canned{
		"users.list": {{status: 200, body: `{"ok":false,"error":"invalid_auth"}`}},
	})
	c := New("xoxb-test", WithBaseURL(srv.URL))
	if _, err := c.Users(context.Background()); !errors.Is(err, ErrAPI) {
		t.Fatalf("err = %v, want ErrAPI", err)
	}
}

// TestNewEmptyTokenPanics verifies the constructor guards an empty token.
func TestNewEmptyTokenPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(\"\") did not panic")
		}
	}()
	New("")
}

// TestReplies verifies cursor pagination, the limit trim, and the empty-args
// fast path of the one call that reads conversation content.
func TestReplies(t *testing.T) {
	t.Parallel()
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		queries = append(queries, r.PostForm)
		if r.PostFormValue("cursor") == "" {
			io.WriteString(w, `{"ok":true,"has_more":true,
				"response_metadata":{"next_cursor":"c2"},
				"messages":[{"ts":"1.0","user":"U1","text":"parent"},
					{"ts":"2.0","user":"U2","text":"first reply"}]}`)
			return
		}
		io.WriteString(w, `{"ok":true,"has_more":false,
			"messages":[{"ts":"3.0","user":"U3","text":"second reply"},
				{"ts":"4.0","user":"U1","text":"third reply"}]}`)
	}))
	t.Cleanup(srv.Close)
	c := New("xoxb-test", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))

	got, err := c.Replies(context.Background(), "C1", "1.0", 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("Replies = %d messages, err %v, want 3", len(got), err)
	}
	if got[0].Text != "parent" || got[2].Text != "second reply" {
		t.Errorf("messages = %+v", got)
	}
	if len(queries) != 2 || queries[0].Get("ts") != "1.0" || queries[1].Get("cursor") != "c2" {
		t.Errorf("queries = %v", queries)
	}

	if msgs, err := c.Replies(context.Background(), "", "", 10); err != nil || msgs != nil {
		t.Errorf("Replies with empty args = %v, %v, want nil, nil", msgs, err)
	}
}

// scriptDoer replays a list of status codes in order, repeating the last one,
// and counts the requests it served. A 200 carries a body every method accepts.
type scriptDoer struct {
	// each runs before every reply when set.
	each func()
	// retryAfter is the Retry-After value sent with every reply, empty for none.
	retryAfter string
	// statuses is the status code of each reply in order.
	statuses []int
	// calls is the number of requests served.
	calls int
}

// Do serves the next scripted status.
func (s *scriptDoer) Do(*http.Request) (*http.Response, error) {
	if s.each != nil {
		s.each()
	}
	status := s.statuses[min(s.calls, len(s.statuses)-1)]
	s.calls++
	header := make(http.Header)
	if s.retryAfter != "" {
		header.Set("Retry-After", s.retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     header,
	}, nil
}

// TestServerErrorRetry verifies which answers the client tries again and what
// a status that outlasts the retry budget ends as. A server error on a call
// that is safe to repeat is retried and never comes back as a rate limit, and a
// message post is sent once, since a repeat could post twice.
func TestServerErrorRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	users := func(c *Client) error {
		_, err := c.Users(ctx)
		return err
	}
	history := func(c *Client) error {
		_, err := c.History(ctx, "C1", "", 10)
		return err
	}
	join := func(c *Client) error { return c.JoinChannel(ctx, "C1") }
	post := func(c *Client) error { return c.PostMessage(ctx, "C1", "", "hi") }
	whisper := func(c *Client) error { return c.PostEphemeral(ctx, "C1", "U1", "hi") }
	tests := []struct {
		Want       error
		Call       func(*Client) error
		Statuses   []int
		WantCalls  int
		WantStatus int
	}{{ // Test 0: A 500 is tried again and the later answer wins.
		Call: users, Statuses: []int{500, 200}, WantCalls: 2,
	}, { // Test 1: A 502 is tried again.
		Call: users, Statuses: []int{502, 200}, WantCalls: 2,
	}, { // Test 2: A 503 is tried again.
		Call: users, Statuses: []int{503, 200}, WantCalls: 2,
	}, { // Test 3: A 504 is tried again.
		Call: users, Statuses: []int{504, 200}, WantCalls: 2,
	}, { // Test 4: A join does no harm twice, so it is tried again too.
		Call: join, Statuses: []int{503, 200}, WantCalls: 2,
	}, { // Test 5: A server error that outlasts the quick budget ends as its status.
		Call: users, Statuses: []int{500}, WantCalls: 4, WantStatus: 500,
	}, { // Test 6: History spends its larger budget before it gives up.
		Call: history, Statuses: []int{502}, WantCalls: 11, WantStatus: 502,
	}, { // Test 7: A 429 that outlasts the budget is still a rate limit.
		Call: users, Statuses: []int{429}, WantCalls: 4, Want: ErrRateLimited,
	}, { // Test 8: A server error followed by lasting 429s ends as a rate limit.
		Call: users, Statuses: []int{503, 429}, WantCalls: 4, Want: ErrRateLimited,
	}, { // Test 9: A 429 followed by lasting server errors ends as the status.
		Call: users, Statuses: []int{429, 503}, WantCalls: 4, WantStatus: 503,
	}, { // Test 10: A 501 is not a passing fault, so it is tried once.
		Call: users, Statuses: []int{501, 200}, WantCalls: 1, WantStatus: 501,
	}, { // Test 11: A 400 is tried once.
		Call: users, Statuses: []int{400, 200}, WantCalls: 1, WantStatus: 400,
	}, { // Test 12: A message post is tried once after a server error.
		Call: post, Statuses: []int{503, 200}, WantCalls: 1, WantStatus: 503,
	}, { // Test 13: An ephemeral post is tried once after a server error.
		Call: whisper, Statuses: []int{502, 200}, WantCalls: 1, WantStatus: 502,
	}, { // Test 14: A message post still waits out a rate limit.
		Call: post, Statuses: []int{429, 200}, WantCalls: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			d := &scriptDoer{statuses: test.Statuses, retryAfter: "0"}
			err := test.Call(New("xoxb-test", WithHTTPClient(d)))
			if d.calls != test.WantCalls {
				t.Errorf("requests = %d, want %d", d.calls, test.WantCalls)
			}
			switch {
			case test.WantStatus != 0:
				want := fmt.Sprintf("unexpected status %d", test.WantStatus)
				if err == nil || !strings.HasSuffix(err.Error(), want) {
					t.Fatalf("err = %v, want one ending in %q", err, want)
				}
				if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrAPI) {
					t.Errorf("err = %v, want neither a rate limit nor an API error", err)
				}
			case test.Want != nil:
				if !errors.Is(err, test.Want) {
					t.Fatalf("err = %v, want %v", err, test.Want)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
			}
		})
	}
}

// TestServerErrorRetryStopsOnCancel verifies a canceled context ends the wait
// that follows a server error, so a stopped run does not sit out the rest of
// the retry budget.
func TestServerErrorRetryStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &scriptDoer{statuses: []int{503}, each: cancel}
	_, err := New("xoxb-test", WithHTTPClient(d)).Users(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d.calls != 1 {
		t.Errorf("requests = %d, want 1", d.calls)
	}
}
