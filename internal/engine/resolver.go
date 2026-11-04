package engine

import (
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/llm"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// Cloud provider credentials come only from the environment, never flags.
const (
	// AnthropicKeyEnv holds the Claude API key.
	AnthropicKeyEnv = "PEEPWHO_ANTHROPIC_KEY"
	// OpenAIKeyEnv holds the OpenAI-compatible API key.
	OpenAIKeyEnv = "PEEPWHO_OPENAI_KEY"
	// GeminiKeyEnv holds the Gemini API key.
	GeminiKeyEnv = "PEEPWHO_GEMINI_KEY"
)

// Gemini speaks the OpenAI-compatible protocol at Google's endpoint, so the
// openai client serves it with a base URL and a Gemini model name.
const (
	// geminiHost is the egress destination checked against the policy.
	geminiHost = "generativelanguage.googleapis.com"
	// geminiBaseURL is Google's OpenAI-compatible API root.
	geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	// defaultGeminiModel is used when no model is set.
	defaultGeminiModel = "gemini-2.5-flash"
)

// ResolverConfig selects how a question is answered.
type ResolverConfig struct {
	// Mode is keyword, semantic, or llm. Empty means keyword.
	Mode string
	// Model is the chat model for llm mode.
	Model string
	// EmbedModel is the Ollama embed model for semantic and llm mode.
	EmbedModel string
	// OllamaURL is the Ollama base URL.
	OllamaURL string
	// Provider is the llm-mode provider: ollama, anthropic, openai, or gemini.
	Provider string
	// OpenAIURL is an OpenAI-compatible base URL for the openai provider.
	OpenAIURL string
}

// NewResolver builds the resolver for cfg. Semantic mode and the default
// ollama provider target a local server; anything non-local is gated by the
// egress policy. Cloud providers additionally run redacted under the redacted
// policy, so no names or emails leave the machine.
func NewResolver(ix *index.Index, pol policy.Policy, cfg ResolverConfig) (resolve.Resolver, error) {
	switch cfg.Mode {
	case "", "keyword":
		return resolve.NewKeyword(ix), nil
	case "semantic":
		if cfg.Provider != "" && cfg.Provider != "ollama" {
			return nil, fmt.Errorf("%w: semantic mode needs local embeddings; use --provider ollama", ErrBadArgs)
		}
		if !ix.HasEmbeddings() {
			return nil, fmt.Errorf(
				"%w: this index has no embeddings, so semantic mode has nothing to match against. "+
					"Re-index with --embed, or ask with --mode keyword", ErrBadArgs)
		}
		if err := GuardLLMHost(pol, cfg.OllamaURL); err != nil {
			return nil, err
		}
		return resolve.NewSemantic(ix, NewOllama(cfg.Model, cfg.EmbedModel, cfg.OllamaURL)), nil
	case "llm":
		switch cfg.Provider {
		case "", "ollama":
			if err := GuardLLMHost(pol, cfg.OllamaURL); err != nil {
				return nil, err
			}
			client := NewOllama(cfg.Model, cfg.EmbedModel, cfg.OllamaURL)
			return resolve.NewLLM(ix, client, client), nil
		case "anthropic", "openai", "gemini":
			chat, err := CloudChatter(pol, cfg.Provider, cfg.Model, cfg.OpenAIURL)
			if err != nil {
				return nil, err
			}
			if pol.Mode() == policy.Redacted {
				return resolve.NewRedactedLLM(ix, chat, nil), nil
			}
			return resolve.NewLLM(ix, chat, nil), nil
		default:
			return nil, fmt.Errorf(
				"%w: provider %q (want ollama, anthropic, openai, or gemini)", ErrBadArgs, cfg.Provider)
		}
	default:
		return nil, fmt.Errorf("%w: mode %q (want keyword, semantic, or llm)", ErrBadArgs, cfg.Mode)
	}
}

// CloudChatter builds a chat client for the anthropic, openai, or gemini
// provider, gated by the egress policy. Strict denies anything non-local.
// Redacted permits only the known provider hosts, with redaction applied by
// the resolver. A local OpenAI-compatible URL, such as LM Studio, counts as
// local and needs no opt-in; a remote one needs open. Keys come only from the
// environment.
func CloudChatter(pol policy.Policy, provider, model, openaiURL string) (resolve.Chatter, error) {
	switch provider {
	case "anthropic":
		if err := pol.AllowEgress("api.anthropic.com"); err != nil {
			return nil, cloudDenied(provider, err)
		}
		key := os.Getenv(AnthropicKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%w: set %s", ErrBadArgs, AnthropicKeyEnv)
		}
		return llm.NewAnthropic(key,
			llm.WithAnthropicModel(model), llm.WithAnthropicTransport(policy.Transport(nil, pol))), nil
	case "gemini":
		if err := pol.AllowEgress(geminiHost); err != nil {
			return nil, cloudDenied(provider, err)
		}
		key := os.Getenv(GeminiKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%w: set %s", ErrBadArgs, GeminiKeyEnv)
		}
		if model == "" {
			model = defaultGeminiModel
		}
		return llm.NewOpenAI(key, llm.WithOpenAIModel(model),
			llm.WithOpenAIBaseURL(geminiBaseURL), llm.WithOpenAITransport(policy.Transport(nil, pol))), nil
	}

	key := os.Getenv(OpenAIKeyEnv)
	clientOpts := []llm.OpenAIOption{llm.WithOpenAITransport(policy.Transport(nil, pol))}
	if model != "" {
		clientOpts = append(clientOpts, llm.WithOpenAIModel(model))
	}
	if openaiURL != "" {
		if err := GuardLLMHost(pol, openaiURL); err != nil {
			return nil, err
		}
		clientOpts = append(clientOpts, llm.WithOpenAIBaseURL(openaiURL))
	} else {
		if err := pol.AllowEgress("api.openai.com"); err != nil {
			return nil, cloudDenied(provider, err)
		}
		if key == "" {
			return nil, fmt.Errorf("%w: set %s (or point --openai-url at a local server)", ErrBadArgs, OpenAIKeyEnv)
		}
	}
	return llm.NewOpenAI(key, clientOpts...), nil
}

// cloudDenied explains a policy denial with the way to opt in.
func cloudDenied(provider string, err error) error {
	return fmt.Errorf(
		"cloud provider %s: %w (use --policy redacted to send anonymized candidates, or --policy open)",
		provider, err)
}

// NewOllama builds an Ollama client for the chat and embed models.
func NewOllama(model, embedModel, ollamaURL string) *llm.Ollama {
	return llm.New(model, llm.WithBaseURL(ollamaURL), llm.WithEmbedModel(embedModel))
}

// NewDocOllama builds the client indexing embeds with. Stored items and the
// questions asked of them carry different task prefixes on models trained
// asymmetrically, so the index-time client is marked as the document side.
func NewDocOllama(embedModel, ollamaURL string) *llm.Ollama {
	return llm.New("", llm.WithBaseURL(ollamaURL), llm.WithEmbedModel(embedModel),
		llm.WithEmbedTask(llm.EmbedDocuments))
}

// GuardLLMHost permits a loopback model host unconditionally and requires the
// open policy for any other host. The semantic and Ollama paths send full
// profile text with no redaction, so a non-loopback host would leak names,
// emails, and titles. Redacted's known-provider allowance is sound only on the
// cloud path, which anonymizes candidates, so it must not apply here: only open,
// where the operator accepts unrestricted egress, admits a remote host.
func GuardLLMHost(pol policy.Policy, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid model url %q: %w", ErrBadArgs, raw, err)
	}
	host := u.Hostname()
	if u.Opaque != "" || host == "" {
		return fmt.Errorf("%w: model url %q has no host", ErrBadArgs, raw)
	}
	if IsLoopbackHost(host) {
		return nil
	}
	if pol.Mode() != policy.Open {
		return fmt.Errorf(
			"%w: model host %s needs --policy open; it receives unredacted profiles",
			policy.ErrEgressDenied, host)
	}
	return nil
}

// IsLoopbackHost reports whether host names the loopback interface: the
// literal "localhost" or any loopback IP such as 127.0.0.1, 127.0.0.2, or ::1.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
