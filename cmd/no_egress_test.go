package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recordingTransport fails every HTTP request and remembers what was asked
// for, so a test can prove a code path attempted no egress at all.
type recordingTransport struct {
	// mu guards requests.
	mu sync.Mutex
	// requests are the URLs something tried to reach.
	requests []string
}

// RoundTrip records the attempt and refuses it.
func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req.URL.String())
	r.mu.Unlock()
	return nil, fmt.Errorf("egress blocked by test: %s", req.URL)
}

// attempts returns what was reached for, if anything.
func (r *recordingTransport) attempts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// TestLocalCommandsMakeNoNetworkRequests proves the local-by-default claim
// for the paths that promise it: indexing local sources and asking questions
// with no LLM configured must attempt zero HTTP requests. Every client in the
// program rides http.DefaultTransport, which this test replaces with a
// recorder that refuses everything.
func TestLocalCommandsMakeNoNetworkRequests(t *testing.T) {
	// This test swaps a process-global and must not run beside tests that
	// legitimately talk to local fixture servers.
	rec := &recordingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = rec
	t.Cleanup(func() { http.DefaultTransport = old })

	// Prove the recorder is actually in the path before trusting its silence.
	if _, err := http.Get("http://egress-canary.invalid/"); err == nil {
		t.Fatal("canary request succeeded; the recorder is not installed")
	}
	if got := rec.attempts(); len(got) != 1 {
		t.Fatalf("canary not recorded (%v); a zero later would be vacuous", got)
	}
	rec.mu.Lock()
	rec.requests = nil
	rec.mu.Unlock()

	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Jane Roe,jane@x.com,Staff Engineer,Billing,retries;idempotency\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	steps := [][]string{
		{"index", "--data-dir", dir, "--source", "org-csv", "--file", csv},
		{"index", "--data-dir", dir, "--source", "slack-export", "--file", exportZip,
			"--episodes", "--since-days", "36500", "--merge"},
		{"ask", "--data-dir", dir, "who knows about billing retries"},
		{"status", "--data-dir", dir},
	}
	for _, args := range steps {
		if _, stderr, err := runCmd(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
		if got := rec.attempts(); len(got) > 0 {
			t.Fatalf("%v attempted network requests: %v", args, got)
		}
	}
}
