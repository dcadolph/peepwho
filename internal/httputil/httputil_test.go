package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// doerFunc adapts a function to the Doer interface for tests.
type doerFunc func(*http.Request) (*http.Response, error)

// Do calls the wrapped function.
func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// mkResp builds a response with the given status, one header, and a body.
func mkResp(status int, headerKey, headerVal, body string) *http.Response {
	h := http.Header{}
	if headerKey != "" {
		h.Set(headerKey, headerVal)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// TestRetryAfter covers the integer-seconds and HTTP-date forms plus rejects.
func TestRetryAfter(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(120 * time.Second).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-120 * time.Second).UTC().Format(http.TimeFormat)

	tests := []struct {
		Name    string
		Header  string
		WantOK  bool
		WantPos bool
	}{ // WantPos means the returned duration is strictly positive.
		{Name: "seconds", Header: "5", WantOK: true, WantPos: true},
		{Name: "zero seconds", Header: "0", WantOK: true, WantPos: false},
		{Name: "http date future", Header: future, WantOK: true, WantPos: true},
		{Name: "http date past", Header: past, WantOK: true, WantPos: false},
		{Name: "absent", Header: "", WantOK: false, WantPos: false},
		{Name: "garbage", Header: "soon", WantOK: false, WantPos: false},
		{Name: "negative", Header: "-1", WantOK: false, WantPos: false},
	}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			key := ""
			if test.Header != "" {
				key = "Retry-After"
			}
			d, ok := RetryAfter(mkResp(http.StatusTooManyRequests, key, test.Header, ""))
			if ok != test.WantOK {
				t.Fatalf("ok = %v, want %v", ok, test.WantOK)
			}
			if (d > 0) != test.WantPos {
				t.Errorf("duration = %v, want positive=%v", d, test.WantPos)
			}
		})
	}
}

// TestDoRetriesThenSucceeds verifies a 429 is retried and the later body wins.
func TestDoRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls int
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return mkResp(http.StatusTooManyRequests, "Retry-After", "0", "later"), nil
		}
		return mkResp(http.StatusOK, "", "", "ok-body"), nil
	})

	resp, body, err := Do(context.Background(), d, 3, nil, buildReq(t))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok-body" {
		t.Errorf("resp = %d body = %q, want 200 ok-body", resp.StatusCode, body)
	}
}

// TestDoExhaustsBudget verifies a persistent 429 maps to ErrRateLimited after
// the retry budget is spent.
func TestDoExhaustsBudget(t *testing.T) {
	t.Parallel()
	var calls int
	d := doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return mkResp(http.StatusTooManyRequests, "Retry-After", "0", "slow"), nil
	})

	_, _, err := Do(context.Background(), d, 2, nil, buildReq(t))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (initial plus two retries)", calls)
	}
}

// buildReq returns a request factory for a throwaway endpoint.
func buildReq(t *testing.T) func() (*http.Request, error) {
	t.Helper()
	return func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid", nil)
	}
}

// TestGetJSON checks the three failures every API client has to tell apart come
// back distinctly, since each client maps them onto its own sentinels and a
// wrong mapping turns an expired token into an unreachable server.
func TestGetJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Status     int
		Body       string
		WantCode   int
		WantResult string
		Want       error
	}{{ // Test 0: A good response decodes.
		Status: http.StatusOK, Body: `{"name":"kim"}`, WantResult: "kim",
	}, { // Test 1: A non-200 is a status error carrying the code.
		Status: http.StatusUnauthorized, Body: `{}`, WantCode: http.StatusUnauthorized,
	}, { // Test 2: A body that is not JSON is a decode error, not a status one.
		Status: http.StatusOK, Body: `not json`, Want: errBadDecode,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.Status)
				io.WriteString(w, test.Body)
			}))
			t.Cleanup(srv.Close)

			var out struct {
				Name string `json:"name"`
			}
			err := GetJSON(context.Background(), srv.Client(), 0, srv.URL, &out)

			var status *StatusError
			switch {
			case test.WantCode != 0:
				if !errors.As(err, &status) || status.Code != test.WantCode {
					t.Fatalf("err = %v, want a status error with code %d", err, test.WantCode)
				}
			case test.Want != nil:
				if err == nil || errors.As(err, &status) {
					t.Fatalf("err = %v, want a decode error and not a status one", err)
				}
			default:
				if err != nil {
					t.Fatalf("GetJSON: %v", err)
				}
				if out.Name != test.WantResult {
					t.Errorf("name = %q, want %q", out.Name, test.WantResult)
				}
			}
		})
	}
}

// errBadDecode marks the decode case in the table above.
var errBadDecode = errors.New("decode")

// TestNewClientOwnsItsTransport verifies NewClient does not hand back a
// client on the shared default transport. Anything can purge that one:
// every httptest.Server.Close calls CloseIdleConnections on it, which broke
// an unrelated client's request and failed CI at random. The race itself
// cannot be forced in a test, since the transport silently retries most
// dropped connections, so the invariant is what gets held here.
func TestNewClientOwnsItsTransport(t *testing.T) {
	t.Parallel()
	client := NewClient(time.Second)
	if client.Transport == nil {
		t.Fatal("client has no transport of its own")
	}
	if client.Transport == http.DefaultTransport {
		t.Error("client rides the shared default transport")
	}
	if other := NewClient(time.Second); other.Transport == client.Transport {
		t.Error("two clients share one transport")
	}
	if client.Timeout != time.Second {
		t.Errorf("timeout = %v, want 1s", client.Timeout)
	}

	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	}))
	t.Cleanup(served.Close)
	resp, err := client.Get(served.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
