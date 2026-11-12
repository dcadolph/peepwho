package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/fact"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/render"
	"github.com/dcadolph/whotoask/internal/resolve"
	"github.com/dcadolph/whotoask/internal/util"
)

// newAskCmd builds the ask command, which answers a question from the index.
func newAskCmd(opts *options) *cobra.Command {
	var (
		limit      int
		mode       string
		model      string
		embedModel string
		ollamaURL  string
		provider   string
		openaiURL  string
		fbStrength string
	)
	cmd := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask who to talk to about something",
		Long: `Answer a question from the index: the people to talk to and the channels to
ask in, each with reasons and a confidence from zero to one.

Modes:
  keyword   no model, deterministic, always works (default)
  semantic  blend meaning with your words; needs an index built with --embed
  llm       a local Ollama model re-ranks and writes a recommendation

Examples:
  whotoask ask "who do I talk to about billing retries"
  whotoask ask --mode llm "where do I ask about kafka"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			eng := engine.New(ix, opts.pol)
			applyFeedback(eng, opts, cmd.ErrOrStderr())
			if err := eng.SetFeedbackStrength(fbStrength); err != nil {
				return err
			}
			cfg := engine.ResolverConfig{
				Mode: mode, Model: model, EmbedModel: embedModel, OllamaURL: ollamaURL,
				Provider: provider, OpenAIURL: openaiURL,
			}
			query := strings.Join(args, " ")
			ans, err := eng.Ask(cmd.Context(), query, limit, cfg)
			if err != nil {
				return err
			}
			warnEmptyAsk(cmd, ix, query, ans)
			view := ans.View(query)
			if err := opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				render.RenderAsk(w, query, view, s)
			}); err != nil {
				return err
			}
			showRelatedFacts(cmd, opts, query)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&limit, "limit", 5, "Maximum number of results per section.")
	f.StringVar(&mode, "mode", "keyword", "Resolver: keyword, semantic, or llm.")
	f.StringVar(&model, "model", "", "Ollama chat model for --mode llm (default llama3.1).")
	f.StringVar(&embedModel, "embed-model", "", "Ollama embed model for semantic/llm (default nomic-embed-text).")
	f.StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL.")
	f.StringVar(&provider, "provider", "ollama",
		"LLM provider: ollama, anthropic, openai, or gemini. Cloud providers need --policy redacted or open.")
	f.StringVar(&openaiURL, "openai-url", "",
		"OpenAI-compatible base URL including the version path, e.g. http://localhost:1234/v1.")
	f.StringVar(&fbStrength, "feedback", "normal",
		"How hard votes move ranking: off, low, normal, or high.")
	return cmd
}

// warnEmptyAsk explains an empty answer on stderr so it does not read as a
// silent success. An empty index, a query with no term the index knows, and a
// genuine miss are different problems with different fixes, and the JSON on
// stdout cannot tell them apart. An answer holding only teams of record is an
// answer, the one a CODEOWNERS file of team handles gives.
func warnEmptyAsk(cmd *cobra.Command, ix *index.Index, query string, ans resolve.Answer) {
	if len(ans.People) > 0 || len(ans.Departed) > 0 || len(ans.Channels) > 0 || len(ans.Teams) > 0 {
		return
	}
	w := cmd.ErrOrStderr()
	if ix.Graph == nil || len(ix.Graph.People) == 0 {
		fmt.Fprintln(w, "No one is indexed yet: run `whotoask index` against a source first.")
		return
	}
	// Ask ranks by who knows a subject. A question naming a person is answered
	// directly, but a team, a channel, or a half-remembered name still returns
	// nothing while the entity sits in the index, so offer the closest matches
	// by name before the generic hint and point a misdirected question
	// somewhere useful.
	if hits := suggestFromSearch(ix, query, 5); len(hits) > 0 {
		fmt.Fprintln(w, "No expertise match for that. Closest by name:")
		for _, h := range hits {
			fmt.Fprintln(w, "  "+suggestionLabel(h))
		}
		fmt.Fprintln(w, "Use `whotoask search` to explore, or ask with the terms your team would use.")
		return
	}
	fmt.Fprintln(w,
		"No match for that question. Matching is on the words people and channels are described by, "+
			"so try the terms your team would use, or `whotoask directory` to see what is indexed.")
}

// suggestionLabel is one line of the closest-by-name list: the name, a hash on
// a channel, then the title or team and a deactivated mark in parentheses.
func suggestionLabel(h resolve.SearchResult) string {
	label := h.Name
	if h.Kind == "channel" {
		label = "#" + h.Name
	}
	var ctx []string
	if c := firstNonEmpty(h.Title, h.Team); c != "" {
		ctx = append(ctx, c)
	}
	if h.Deactivated {
		ctx = append(ctx, "deactivated")
	}
	if len(ctx) > 0 {
		label += " (" + strings.Join(ctx, ", ") + ")"
	}
	return label
}

// firstNonEmpty returns the first non-empty string, or empty when all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// suggestFromSearch finds the closest entities for an ask that had no expertise
// match. It searches each meaningful word of the question, not the whole phrase,
// since a natural-language question is never a substring of one short field, then
// merges the hits by score. It falls back to the whole query when the question
// has no searchable words.
func suggestFromSearch(ix *index.Index, query string, limit int) []resolve.SearchResult {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return resolve.Search(ix, query, limit)
	}
	var all []resolve.SearchResult
	for _, t := range terms {
		all = append(all, resolve.Search(ix, t, limit)...)
	}
	// One person answering for several terms is still one person, and the
	// strongest term they matched is the one that speaks for them.
	all = util.Distinct(all, func(r resolve.SearchResult) string { return r.ID })
	resolve.SortSearchResults(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// showRelatedFacts prints any recorded facts whose subject, object, or detail
// mentions a query term, so a fact somebody typed appears alongside the crawled
// answer, labeled with its source and date. Facts are supplementary, so a
// failure to load them never fails the answer.
func showRelatedFacts(cmd *cobra.Command, opts *options, query string) {
	// Through the same loader every other reader uses, so a sealed fact file
	// opens here too. Loading it raw returned a decode error on an encrypted
	// install, and because a failure is swallowed by design, the facts simply
	// stopped appearing with nothing said.
	store, err := opts.loadFacts()
	if err != nil {
		return
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return
	}
	var hits []fact.Fact
	for _, f := range store.List(fact.Filter{}) {
		if factMentions(f, terms) {
			hits = append(hits, f)
		}
	}
	if len(hits) == 0 {
		return
	}
	w := cmd.ErrOrStderr()
	fmt.Fprintln(w, "Recorded facts:")
	writeFactLines(w, hits)
}

// queryTerms lowercases the query and returns its words of three or more
// letters or digits, the terms a fact is matched against.
func queryTerms(q string) []string {
	var out []string
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	for _, w := range fields {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// factMentions reports whether any term appears in the fact's subject, object,
// or detail.
func factMentions(f fact.Fact, terms []string) bool {
	hay := strings.ToLower(f.Subject + " " + f.Object + " " + f.Detail)
	for _, t := range terms {
		if strings.Contains(hay, t) {
			return true
		}
	}
	return false
}
