package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/policy"
)

// TestGuardLLMHost verifies the non-redacting model paths (semantic and Ollama)
// only reach a non-loopback host under the open policy. Redacted must not admit
// a known provider here: those paths ship full profiles, so redacted's
// known-provider allowance would leak PII to a third party.
func TestGuardLLMHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Want error
		Mode policy.Mode
		URL  string
	}{{ // Test 0: Loopback name under strict.
		Mode: policy.Strict, URL: "http://localhost:11434",
	}, { // Test 1: Loopback IP under strict.
		Mode: policy.Strict, URL: "http://127.0.0.1:11434",
	}, { // Test 2: Alternate loopback IP.
		Mode: policy.Strict, URL: "http://127.0.0.2:11434",
	}, { // Test 3: IPv6 loopback.
		Mode: policy.Strict, URL: "http://[::1]:11434",
	}, { // Test 4: Loopback under redacted.
		Mode: policy.Redacted, URL: "http://localhost:11434",
	}, { // Test 5: Loopback under open.
		Mode: policy.Open, URL: "http://localhost:11434",
	}, { // Test 6: Remote under strict.
		Mode: policy.Strict, URL: "https://api.openai.com", Want: policy.ErrEgressDenied,
	}, { // Test 7: Known provider under redacted.
		Mode: policy.Redacted, URL: "https://api.openai.com", Want: policy.ErrEgressDenied,
	}, { // Test 8: Arbitrary host under redacted.
		Mode: policy.Redacted, URL: "https://evil.example.com", Want: policy.ErrEgressDenied,
	}, { // Test 9: Remote under open.
		Mode: policy.Open, URL: "https://api.openai.com",
	}, { // Test 10: Arbitrary host under open.
		Mode: policy.Open, URL: "https://evil.example.com",
	}, { // Test 11: No scheme has no host.
		Mode: policy.Open, URL: "api.openai.com", Want: ErrBadArgs,
	}, { // Test 12: Opaque URL has no host.
		Mode: policy.Open, URL: "http:api.openai.com", Want: ErrBadArgs,
	}, { // Test 13: Unparseable URL.
		Mode: policy.Open, URL: "http://[::1", Want: ErrBadArgs,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			err := GuardLLMHost(policy.New(test.Mode, false), test.URL)
			if !errors.Is(err, test.Want) {
				t.Errorf("GuardLLMHost(%s, %q) = %v, want %v", test.Mode, test.URL, err, test.Want)
			}
		})
	}
}

// TestNewResolver verifies mode and provider selection under each policy. The
// remote Ollama and semantic cases reproduce an exploit: under a locked
// redacted policy they would build a non-redacting resolver against a remote
// host and ship full profiles.
func TestNewResolver(t *testing.T) {
	t.Setenv(AnthropicKeyEnv, "sk-test")
	t.Setenv(OpenAIKeyEnv, "")
	t.Setenv(GeminiKeyEnv, "")
	ix := index.New()
	tests := []struct {
		Want     error
		Pol      policy.Policy
		Cfg      ResolverConfig
		WantType string
	}{{ // Test 0: Empty mode is keyword.
		Pol: policy.Default(), Cfg: ResolverConfig{}, WantType: "*resolve.Keyword",
	}, { // Test 1: Unknown mode.
		Pol: policy.Default(), Cfg: ResolverConfig{Mode: "bogus"}, Want: ErrBadArgs,
	}, { // Test 2: Semantic with a cloud provider.
		Pol: policy.New(policy.Redacted, false),
		Cfg: ResolverConfig{Mode: "semantic", Provider: "anthropic"}, Want: ErrBadArgs,
	}, { // Test 3: Semantic on an index with no embeddings.
		Pol: policy.Default(),
		Cfg: ResolverConfig{Mode: "semantic", OllamaURL: "http://localhost:11434"}, Want: ErrBadArgs,
	}, { // Test 4: Strict denies a cloud provider.
		Pol: policy.Default(), Cfg: ResolverConfig{Mode: "llm", Provider: "anthropic"},
		Want: policy.ErrEgressDenied,
	}, { // Test 5: Redacted policy admits a cloud provider with a key.
		Pol: policy.New(policy.Redacted, false), Cfg: ResolverConfig{Mode: "llm", Provider: "anthropic"},
		WantType: "*resolve.LLM",
	}, { // Test 6: Open policy admits a cloud provider with a key.
		Pol: policy.New(policy.Open, false), Cfg: ResolverConfig{Mode: "llm", Provider: "anthropic"},
		WantType: "*resolve.LLM",
	}, { // Test 7: Missing Gemini key.
		Pol: policy.New(policy.Redacted, false), Cfg: ResolverConfig{Mode: "llm", Provider: "gemini"},
		Want: ErrBadArgs,
	}, { // Test 8: Missing OpenAI key with no local URL.
		Pol: policy.New(policy.Redacted, false), Cfg: ResolverConfig{Mode: "llm", Provider: "openai"},
		Want: ErrBadArgs,
	}, { // Test 9: Local OpenAI-compatible server needs no key or opt-in.
		Pol:      policy.Default(),
		Cfg:      ResolverConfig{Mode: "llm", Provider: "openai", OpenAIURL: "http://localhost:1234/v1"},
		WantType: "*resolve.LLM",
	}, { // Test 10: Unknown provider.
		Pol: policy.New(policy.Redacted, false), Cfg: ResolverConfig{Mode: "llm", Provider: "carrier-pigeon"},
		Want: ErrBadArgs,
	}, { // Test 11: Locked redacted denies a remote Ollama at a known provider host.
		Pol:  policy.New(policy.Redacted, true),
		Cfg:  ResolverConfig{Mode: "llm", Provider: "ollama", OllamaURL: "https://api.openai.com"},
		Want: policy.ErrEgressDenied,
	}, { // Test 12: Locked redacted denies a remote Ollama at an arbitrary host.
		Pol:  policy.New(policy.Redacted, true),
		Cfg:  ResolverConfig{Mode: "llm", OllamaURL: "https://evil.example.com"},
		Want: policy.ErrEgressDenied,
	}, { // Test 13: Open accepts a remote Ollama host.
		Pol:      policy.New(policy.Open, false),
		Cfg:      ResolverConfig{Mode: "llm", Provider: "ollama", OllamaURL: "https://api.openai.com"},
		WantType: "*resolve.LLM",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			res, err := NewResolver(ix, test.Pol, test.Cfg)
			if !errors.Is(err, test.Want) {
				t.Fatalf("NewResolver(%+v) error = %v, want %v", test.Cfg, err, test.Want)
			}
			if test.Want != nil {
				return
			}
			if got := fmt.Sprintf("%T", res); got != test.WantType {
				t.Errorf("resolver type = %s, want %s", got, test.WantType)
			}
		})
	}
}

// TestIsLoopbackHost verifies the names and addresses treated as this machine.
func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Host string
		Want bool
	}{
		{Host: "localhost", Want: true}, // Test 0: The loopback name.
		{Host: "127.0.0.1", Want: true}, // Test 1: IPv4 loopback.
		{Host: "127.9.9.9", Want: true}, // Test 2: Anywhere in 127/8.
		{Host: "::1", Want: true},       // Test 3: IPv6 loopback.
		{Host: "", Want: false},         // Test 4: Empty binds everything.
		{Host: "10.0.0.1", Want: false}, // Test 5: Private but not loopback.
		{Host: "localhost.example.com"}, // Test 6: A name that only starts like it.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := IsLoopbackHost(test.Host); got != test.Want {
				t.Errorf("IsLoopbackHost(%q) = %t, want %t", test.Host, got, test.Want)
			}
		})
	}
}
