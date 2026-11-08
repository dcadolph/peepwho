package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/resolve"
	"github.com/dcadolph/peepwho/internal/simorg"
)

// serveLog is the stderr of a server under test. It tells announced the address from the
// first serving line, since the server writes it from another goroutine.
type serveLog struct {
	// mu guards buf.
	mu sync.Mutex
	// buf holds everything written.
	buf bytes.Buffer
	// announced receives the address the server says it bound.
	announced chan string
	// once sends announced at most one address.
	once sync.Once
}

// Write keeps p and looks for the serving line.
func (l *serveLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.buf.Write(p)
	const mark = "serving on http://"
	if _, rest, found := strings.Cut(l.buf.String(), mark); found {
		if addr, _, whole := strings.Cut(rest, " "); whole {
			l.once.Do(func() { l.announced <- addr })
		}
	}
	return n, err
}

// String returns everything written so far.
func (l *serveLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// serveCmd runs the root command with args in the background on a port the system picks, waits
// until it serves, and returns the address it bound and a stop func, which cancels it and
// returns its stderr and error. A port picked here and handed over would be free only until
// another parallel test took it, so the system picks during the bind itself.
func serveCmd(t *testing.T, args ...string) (addr string, stop func() (string, error)) {
	t.Helper()
	var out bytes.Buffer
	errLog := &serveLog{announced: make(chan string, 1)}
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(errLog)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append(args, "--addr", "127.0.0.1:0"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case addr = <-errLog.announced:
	case err := <-done:
		t.Fatalf("server exited before serving: %v\n%s", err, errLog.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("server did not start:\n%s", errLog.String())
	}
	return addr, func() (string, error) {
		cancel()
		select {
		case err := <-done:
			return errLog.String(), err
		case <-time.After(30 * time.Second):
			t.Fatal("server did not stop")
			return "", nil
		}
	}
}

// getOK fetches path from the server at addr and returns the body of a 200 response.
func getOK(t *testing.T, addr, path string) []byte {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	return body
}

// TestDemoServesAndStops runs the simulated-company demo on a free port, reads
// every surface the demo wires, and checks it shuts down cleanly on cancel.
func TestDemoServesAndStops(t *testing.T) {
	t.Parallel()
	addr, stop := serveCmd(t, "demo", "--open=false", "--data-dir", t.TempDir())
	if strings.HasSuffix(addr, ":0") {
		t.Errorf("serving line names port zero: %s", addr)
	}

	var ans resolve.JSONAnswer
	body := getOK(t, addr, "/api/ask?q="+url.QueryEscape("billing retries"))
	if err := json.Unmarshal(body, &ans); err != nil {
		t.Fatalf("decode ask: %v\n%s", err, body)
	}
	if len(ans.People) == 0 {
		t.Errorf("ask billing retries returned no people: %s", body)
	}
	if got := string(getOK(t, addr, "/api/cli?cmd=risk")); !strings.Contains(got, "RISK") &&
		!strings.Contains(strings.ToLower(got), "risk") {
		t.Errorf("cli risk view does not mention risk: %q", got)
	}
	for _, name := range []string{"ask", "ownership", "related", "attest"} {
		if got := getOK(t, addr, "/api/cli?cmd="+name); len(got) == 0 {
			t.Errorf("cli %s view is empty", name)
		}
	}
	if got := string(getOK(t, addr, "/report/risk.html")); !strings.Contains(got, "<html") {
		t.Errorf("brief is not HTML: %.80q", got)
	}
	if got := getOK(t, addr, "/api/exposure"); !json.Valid(got) {
		t.Errorf("exposure is not JSON: %.80q", got)
	}
	recall := getOK(t, addr, "/api/recall?q="+url.QueryEscape("payment webhook retries")+
		"&me="+url.QueryEscape(demoPerson))
	if !json.Valid(recall) {
		t.Errorf("recall is not JSON: %.80q", recall)
	}

	stderr, err := stop()
	if err != nil {
		t.Fatalf("demo: %v\n%s", err, stderr)
	}
	for _, want := range []string{"serving on http://" + addr, "shutting down"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

// TestServeBindFailure verifies a port already held is reported as a serve
// error naming the address, not a server that started and died.
func TestServeBindFailure(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	_, stderr, err := runCmd(t, "demo", "--open=false", "--addr", addr, "--data-dir", t.TempDir())
	if !errors.Is(err, ErrServe) {
		t.Fatalf("demo on a held port: err = %v, want ErrServe", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error %q does not name %s", err, addr)
	}
	if strings.Contains(string(stderr), "serving on") {
		t.Errorf("announced serving before the bind failed:\n%s", stderr)
	}
}

// TestServeRefusesOpenBindWithoutToken verifies a bind beyond localhost needs
// the serve token unless the index is public sample data.
func TestServeRefusesOpenBindWithoutToken(t *testing.T) {
	t.Parallel()
	err := serveWeb(newRootCmd(), &options{}, nil, nil, webConfig{addr: "0.0.0.0:0"})
	if !errors.Is(err, ErrBadArgs) {
		t.Fatalf("open bind without token: err = %v, want ErrBadArgs", err)
	}
}

// TestWebVoteFn verifies votes are recorded on a private server and switched
// off on a public one or when there is no store.
func TestWebVoteFn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ix, err := simorg.BuildIndex(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	store, err := feedback.Load(dir+"/feedback.json", nil)
	if err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	eng := engine.New(ix, policy.Policy{})
	tests := []struct {
		Store    *feedback.Store
		Public   bool
		WantVote bool
	}{
		{Store: store, WantVote: true},                // Test 0: Private server with a store.
		{Store: store, Public: true, WantVote: false}, // Test 1: Public server takes no votes.
		{Store: nil, WantVote: false},                 // Test 2: No store, no votes.
	}
	for testNum, test := range tests {
		vote := webVoteFn(eng, test.Store, webConfig{public: test.Public})
		if got := vote != nil; got != test.WantVote {
			t.Errorf("test %d: vote set = %t, want %t", testNum, got, test.WantVote)
		}
	}
	vote := webVoteFn(eng, store, webConfig{})
	if err := vote(feedback.Entry{Query: "billing retries", Person: demoPerson, Vote: 1}); err != nil {
		t.Fatalf("vote: %v", err)
	}
	if len(store.All()) != 1 {
		t.Errorf("store holds %d entries after one vote, want 1", len(store.All()))
	}
}

// TestWebAskFnFallsBack verifies a request with no mode uses the server's mode
// and that a bad mode is reported as a bad request.
func TestWebAskFnFallsBack(t *testing.T) {
	t.Parallel()
	ix, err := simorg.BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ask := webAskFn(engine.New(ix, policy.Policy{}), webConfig{mode: "keyword"})
	ans, err := ask(context.Background(), "billing retries", "", "", 5)
	if err != nil {
		t.Fatalf("ask with no mode: %v", err)
	}
	if len(ans.People) == 0 {
		t.Error("ask with the server's keyword mode found nobody")
	}
	if _, err := ask(context.Background(), "billing retries", "nosuch", "", 5); err == nil {
		t.Error("ask with an unknown mode: want error, got nil")
	}
}
