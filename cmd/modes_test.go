package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/simorg"
)

// newTagsServer serves Ollama's tag list with status code.
func newTagsServer(t *testing.T, code int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// deadBase is a loopback address nothing listens on. A test cannot take a port below 1024, so
// no parallel test can start answering here the way one can on a port that was picked and freed.
const deadBase = "http://127.0.0.1:1"

// TestOllamaUp verifies a local Ollama counts as up only when its tag list
// answers, and a remote one is trusted without a probe.
func TestOllamaUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Base string
		Want bool
	}{{ // Test 0: The tag list answers.
		Base: newTagsServer(t, http.StatusOK), Want: true,
	}, { // Test 1: The tag list fails.
		Base: newTagsServer(t, http.StatusInternalServerError),
	}, { // Test 2: Nothing is listening.
		Base: deadBase,
	}, { // Test 3: A remote host is trusted without a probe.
		Base: "http://ollama.internal.test:11434", Want: true,
	}, { // Test 4: An unparsable base.
		Base: "://bad url",
	}, { // Test 5: A trailing slash is trimmed before the path.
		Base: newTagsServer(t, http.StatusOK) + "/", Want: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := ollamaUp(context.Background(), test.Base); got != test.Want {
				t.Errorf("ollamaUp(%q) = %t, want %t", test.Base, got, test.Want)
			}
		})
	}
}

// TestModeReadiness verifies the readiness report for a private and a public
// server over an index with no embeddings.
func TestModeReadiness(t *testing.T) {
	t.Parallel()
	ix, err := simorg.BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	up := newTagsServer(t, http.StatusOK)

	priv := modeReadiness(context.Background(), ix, policy.Default(), webConfig{ollamaURL: up})
	if !priv.Modes["keyword"].Ready {
		t.Error("keyword is not ready")
	}
	if priv.Modes["semantic"].Ready {
		t.Error("semantic is ready on an index with no embeddings")
	}
	if priv.Provider != "ollama" || !priv.Modes["llm"].Ready {
		t.Errorf("llm with a live local Ollama: provider %q ready %t, want ollama true",
			priv.Provider, priv.Modes["llm"].Ready)
	}
	if !strings.Contains(priv.Providers["anthropic"].Hint, "strict policy") {
		t.Errorf("anthropic hint under the default policy = %q, want the strict refusal",
			priv.Providers["anthropic"].Hint)
	}

	pub := modeReadiness(context.Background(), ix, policy.Default(),
		webConfig{ollamaURL: deadBase, public: true, provider: "openai"})
	if pub.Provider != "openai" {
		t.Errorf("provider = %q, want openai", pub.Provider)
	}
	if !strings.Contains(pub.Modes["semantic"].Hint, "public demo stays keyword only") {
		t.Errorf("public semantic hint = %q", pub.Modes["semantic"].Hint)
	}
	if !strings.Contains(pub.Providers["openai"].Hint, "carries no provider key") {
		t.Errorf("public openai hint = %q", pub.Providers["openai"].Hint)
	}
	if strings.Contains(pub.Providers["ollama"].Hint, "carries no provider key") {
		t.Errorf("public ollama hint names a provider key: %q", pub.Providers["ollama"].Hint)
	}
	if pub.Modes["llm"].Hint != pub.Providers["openai"].Hint {
		t.Errorf("public llm hint %q does not follow the openai provider", pub.Modes["llm"].Hint)
	}
}

// TestDemoRepoServes runs the demo over a real repository and stops it.
func TestDemoRepoServes(t *testing.T) {
	t.Parallel()
	repo := newOwnersRepo(t)
	addr, stop := serveCmd(t, "demo", "--open=false", "--repo", repo, "--data-dir", t.TempDir())
	if got := getOK(t, addr, "/api/exposure"); !strings.Contains(string(got), "billing") {
		t.Errorf("exposure over the repository does not name billing: %.200s", got)
	}
	stderr, err := stop()
	if err != nil {
		t.Fatalf("demo --repo: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "reading "+repo) {
		t.Errorf("stderr does not name the repository:\n%s", stderr)
	}
}
