# Personal digest (planned)

A second way to ask WhoToAsk a question. Today WhoToAsk answers "who do I talk to
about X." The digest answers "what did I miss that matters to me." It scans the
channels the running user can see, scores each message for personal relevance,
and returns a ranked, actionable roll-up on demand, on a schedule, or live.

This is a new mode beside the existing pipeline, not a new resolver. Resolvers
answer a topic query and return people and channels. The digest reads raw
messages for one person over a time window and returns messages and suggested
actions. It consumes the Slack client directly, before the connector aggregates
messages into the people graph.

## Why it belongs here

Every hard piece already exists. The Slack Web API client reads users, channels,
and history. The messaging client sends direct messages and opens a Socket Mode
connection. The LLM layer supports a local model through Ollama and a cloud model
through Anthropic, OpenAI, or Gemini. The policy layer governs egress with strict,
redacted, and open modes and an org lock. The bot frontend runs over Socket Mode.
The digest reuses all of it and adds only a per-message relevance path.

## Data flow

The digest sits beside source to connector to index. It reads the Slack client's
history for each visible channel since a stored watermark, scores each message,
buckets the survivors, and renders or delivers them.

    slack client -> messages -> relevance -> buckets -> render or DM

## Relevance

Signals evaluate cheapest first. Direct signals are deterministic: a mention of
the running user, a reply in a thread the user is in, a direct message, and
registered keywords such as project names or topics. Heuristics add weight from
reaction volume, messages from key people drawn from the identity graph, embedded
links, and action words like need, must, deadline, or broken. An optional LLM
pass runs only on messages that clear the rules threshold, classifies whether the
message is actionable for the user, and suggests a next step. The backend is the
existing LLM layer, local or cloud by the user's choice.

The score maps to a bucket. Alert means act now. FYI means read it. Digest is
low-priority context for the roll-up. Each item appears once, ranked, with the
signals that produced it, a permalink, and a suggested action.

## Governance

The cloud LLM pass must call the policy egress check before sending any message
text off the machine. Strict mode denies it and the digest falls back to
rules-only or the local model. Private channel scanning respects the existing
private-channel policy toggle. The token is read from the OS keychain and never
logged.

## Faces

The command line mode is first: a digest subcommand that prints a ranked report,
top items first. Scheduled delivery runs the same engine on a cron and sends the
roll-up as a direct message through the existing messaging client. Live alerts
reuse the Socket Mode bot to react to messages as they arrive and ping the user
when one clears the alert threshold.

## Extras WhoToAsk needs

These are the additive changes. None alter the existing query pipeline.

The Slack message type carries type, subtype, user, bot ID, text, and timestamp
today. The digest also needs the thread timestamp, reaction totals, and a
permalink. Extend the message type and the history call to include reactions,
add a replies call to follow threads the user is in, and add a permalink lookup.

Self identity resolves through the existing auth test call, which returns the
running user's ID for mention and thread matching. Direct-message delivery needs
a conversations-open call to open the user's own IM channel before posting.

The token model differs from the bot. The digest needs a user token so it sees
every channel the user sees, with scopes for channel history, group history, IM
history, user read, and search read. Document this alongside the bot token.

A new internal digest package holds the relevance scorer, the bucketer, and the
renderer. The scorer is a single-method interface with a rules implementation and
an optional LLM implementation, chained so a nil LLM scorer means rules only.

A watermark store under the data directory records the last seen timestamp per
channel so a run reads only new messages. It starts file-backed and can move to
SQLite behind the same interface for history and a scoring audit trail.

A new digest command wires fetch, score, bucket, and render, with flags for the
lookback window, channel scope, minimum score, LLM backend, delivery target, and
output shape. The cloud backend is gated by the policy egress check.

## Build order

Extend the Slack message type and history for threads, reactions, and permalinks.
Add self IM open for delivery. Add the watermark store. Add the digest package
with the rules scorer, bucketer, and renderer. Add the digest command for the
command line face. Add the optional LLM scorer. Add scheduled DM delivery, then
live alerts over the existing bot. The first five deliver a working rules-only
command line digest.
