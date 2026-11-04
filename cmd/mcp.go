package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/mcp"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// mcpAskLimit bounds results per section for MCP answers.
const mcpAskLimit = 25

// newMCPCmd builds the mcp command, which serves peepwho's index to MCP
// clients such as Claude Code and Claude Desktop over stdio.
func newMCPCmd(opts *options) *cobra.Command {
	var (
		embedModel string
		ollamaURL  string
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve peepwho to MCP clients over stdio",
		Long: `Serve the index over the Model Context Protocol, so agent clients can ask
who knows what mid-conversation. Register it once:

  Claude Code:     claude mcp add peepwho -- peepwho mcp
  Claude Desktop:  add {"peepwho": {"command": "peepwho", "args": ["mcp"]}}
                   under mcpServers in claude_desktop_config.json

Tools: peepwho_ask (keyword or semantic ranking), peepwho_recall (past
conversations one person took part in), peepwho_person (full
profile), and peepwho_directory (people, channels, teams, topics). There is
no llm mode here on purpose: the calling agent is already a model, so it
gets ranked candidates with reasons and does its own reading.

Answers flow to whichever agent you wire this into, and to that agent's
model. Wiring it in is the opt-in.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			eng := engine.New(ix, opts.pol)
			applyFeedback(eng, opts, cmd.ErrOrStderr())

			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			srv := mcp.New("peepwho", version, cmd.ErrOrStderr())
			registerMCPTools(srv, eng, embedModel, ollamaURL)
			registerRecallTool(srv, eng.Recaller(store))
			fmt.Fprintln(cmd.ErrOrStderr(), "peepwho mcp: serving on stdio")
			return srv.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&embedModel, "embed-model", "",
		"Ollama embed model for semantic mode (default nomic-embed-text).")
	f.StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama base URL.")
	return cmd
}

// registerRecallTool wires the recall tool, which answers what one person
// worked through before. The caller must name that person, and the answer is
// scoped to conversations they took part in, so an agent cannot use it to read
// somebody else's history.
func registerRecallTool(srv *mcp.Server, res *recall.Resolver) {
	srv.AddTool(mcp.Tool{
		Name: "peepwho_recall",
		Description: "Find the past conversation where a person worked something out, and who " +
			"was in it. Returns people, place, date, and a link back to the conversation, " +
			"never the messages themselves. Every answer covers only the named person's own " +
			"conversations. Ask about the person running peepwho unless told otherwise.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {"type": "string", "description": "What was worked out, e.g. certificate renewal."},
				"person": {"type": "string", "description": "Whose history to search: an email, or a source identifier such as slack:U123."},
				"limit": {"type": "integer", "minimum": 1, "maximum": 25,
					"description": "Maximum conversations to return, default 5."}
			},
			"required": ["question", "person"],
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, args json.RawMessage) (string, error) {
		var in struct {
			// Question is what the caller is trying to remember.
			Question string `json:"question"`
			// Person is whose conversations to search.
			Person string `json:"person"`
			// Limit caps conversations returned.
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(args, &in); err != nil || in.Question == "" {
			return "", fmt.Errorf("peepwho_recall needs a question")
		}
		person := res.Who(in.Person)
		if person == "" {
			return "", fmt.Errorf("peepwho_recall needs a person, such as an email or slack:U123")
		}
		if in.Limit <= 0 || in.Limit > mcpAskLimit {
			in.Limit = 5
		}
		return marshalMCP(res.Resolve(ctx,
			recall.Query{Text: in.Question, Person: person, Limit: in.Limit}))
	})
}

// registerMCPTools wires the ask, person, and directory tools over the engine.
func registerMCPTools(srv *mcp.Server, eng *engine.Engine, embedModel, ollamaURL string) {
	dir := resolve.BuildDirectory(eng.Index())

	srv.AddTool(mcp.Tool{
		Name: "peepwho_ask",
		Description: "Find who to talk to and which channel to ask in about a topic, " +
			"ranked with reasons and strength from the organization's indexed work tools.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {"type": "string", "description": "What you need help with, e.g. billing retries."},
				"mode": {"type": "string", "enum": ["keyword", "semantic"],
					"description": "keyword (default) matches words with typo tolerance; semantic matches meaning and needs an index built with --embed."},
				"limit": {"type": "integer", "minimum": 1, "maximum": 25,
					"description": "Maximum results per section, default 5."}
			},
			"required": ["question"],
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, args json.RawMessage) (string, error) {
		var in struct {
			// Question is what the caller needs help with.
			Question string `json:"question"`
			// Mode selects keyword or semantic ranking.
			Mode string `json:"mode"`
			// Limit caps results per section.
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(args, &in); err != nil || in.Question == "" {
			return "", fmt.Errorf("peepwho_ask needs a question")
		}
		if in.Mode == "llm" {
			return "", fmt.Errorf("no llm mode here: you are the model; use keyword or semantic")
		}
		if in.Limit <= 0 || in.Limit > mcpAskLimit {
			in.Limit = 5
		}
		ans, err := eng.Ask(ctx, in.Question, in.Limit, engine.ResolverConfig{
			Mode: in.Mode, EmbedModel: embedModel, OllamaURL: ollamaURL, Provider: "ollama",
		})
		if err != nil {
			return "", err
		}
		return marshalMCP(ans.View(in.Question))
	})

	srv.AddTool(mcp.Tool{
		Name: "peepwho_person",
		Description: "Everything peepwho knows about one person: title, team, org, manager, " +
			"merged identities, channels, and expertise topics. Use the id from peepwho_ask results.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "The person's canonical identifier, usually an email."}
			},
			"required": ["id"],
			"additionalProperties": false
		}`),
	}, func(_ context.Context, args json.RawMessage) (string, error) {
		var in struct {
			// ID is the person's canonical identifier.
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &in); err != nil || in.ID == "" {
			return "", fmt.Errorf("peepwho_person needs an id")
		}
		profile, ok := eng.Person(in.ID)
		if !ok {
			return "", fmt.Errorf("unknown person: %s", in.ID)
		}
		return marshalMCP(profile)
	})

	srv.AddTool(mcp.Tool{
		Name: "peepwho_directory",
		Description: "Browse everything indexed: people with teams and topics, channels with " +
			"member counts, teams with sizes, or topics with how many people carry each.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"kind": {"type": "string", "enum": ["people", "channels", "teams", "topics"],
					"description": "Which directory to list."}
			},
			"required": ["kind"],
			"additionalProperties": false
		}`),
	}, func(_ context.Context, args json.RawMessage) (string, error) {
		var in struct {
			// Kind names the directory to list.
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("peepwho_directory needs a kind")
		}
		switch in.Kind {
		case "people":
			return marshalMCP(dir.People)
		case "channels":
			return marshalMCP(dir.Channels)
		case "teams":
			return marshalMCP(dir.Teams)
		case "topics":
			return marshalMCP(dir.Topics)
		default:
			return "", fmt.Errorf("unknown kind %q (want people, channels, teams, or topics)", in.Kind)
		}
	})
}

// marshalMCP renders a tool payload as compact JSON text.
func marshalMCP(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(raw), nil
}
