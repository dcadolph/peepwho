# Architecture

WhoToAsk turns scattered work data into a queryable map of who knows what and which
channel to ask in. It is built in layers, each with one job, so a new data source
or a new way to ask is a small, isolated addition.

## Data flow

A connector reads a source and emits records. The index merges records into a
graph of people, teams, orgs, topics, and channels, and builds keyword postings
and optional embeddings. A resolver answers a query against the index. A frontend
calls a resolver and presents the answer.

    source -> connector -> records -> index -> resolver -> frontend

## Layers

Connectors implement one method, Fetch, returning normalized records. Ten
exist today: org-CSV, Slack, GitHub, Jira, Confluence, PagerDuty, git history,
CODEOWNERS, Microsoft Graph, and a JSON import. Each new source is one connector
and changes nothing else.

The model is the normalized graph: people, teams, orgs, topics, and channels,
with weighted edges. People merge across sources by email, so one human is one
entry.

The index holds the graph plus a keyword posting list and, when built with
embeddings, a vector per person and channel. It ranks people and channels for a
query and explains why each matched.

Resolvers answer a query and share one Answer shape. The keyword resolver needs
no model. The semantic resolver ranks by embedding similarity. The LLM resolver
retrieves candidates, ranks and summarizes with a local model, and stays grounded
in the real candidates.

Policy governs model egress. The default is strict: answers never leave the
machine. Redacted admits only known providers and only anonymized numbered
candidates. An organization can pin the policy from a locked system file that
user flags and environment variables cannot loosen.

Frontends are thin and share the engine: a CLI, a localhost web UI, a Slack bot
over Socket Mode or the Events API, and an MCP server over stdio for agent
clients such as Claude Code and Claude Desktop.

## Adding a source

Implement the connector Source interface, returning records for people or
channels, and add a case to the index command. The index, resolvers, web UI, and
bot then work with the new data without change. Every source after the first
was added this way.

## Measured scale

The index and the episode store are each one JSON file, read whole by every
command and written whole by every index run. Measured on an M-series laptop
with the opt-in scale suite
(`WHOTOASK_SCALE=1 go test ./internal/simorg/ -run TestScale`):

| Size       | People | Conversations | Ingest | Index file | Episode file | Save  | Cold start | Ask  | Heap  |
| ---------- | ------ | ------------- | ------ | ---------- | ------------ | ----- | ---------- | ---- | ----- |
| Team       | 50     | 520           | 0.3s   | 146KB      | 784KB        | 86ms  | 8ms        | 1ms  | 4MB   |
| Department | 250    | 3,660         | 1.7s   | 892KB      | 4MB          | 283ms | 41ms       | 1ms  | 24MB  |
| Company    | 1,000  | 15,150        | 5.9s   | 4MB        | 13MB         | 1.0s  | 166ms      | 3ms  | 95MB  |
| Enterprise | 5,000  | 48,400        | 20s    | 17MB       | 40MB         | 3.7s  | 580ms      | 12ms | 318MB |
| Huge       | 10,000 | 151,000       | 57s    | 44MB       | 119MB        | 9.4s  | 1.7s       | 29ms | 902MB |

Answering stays in tens of milliseconds throughout, so the query path is not
what limits size. Three other costs are.

**Writing is the worst of them, and it is not I/O.** Saving ten thousand
people takes 9.4 seconds for a 44MB file, while the larger 119MB episode file
writes in under a second. The difference is that `Save` rebuilds the entire
posting index from scratch every time, re-encoding every term for every person.
Save runs about six times the cold start at every size measured.

That has a consequence worth stating plainly, because the incremental refresh
section below is easy to over-read. Fetching is bounded by the delta: Jira,
Confluence, GitHub, and Slack query for what changed since the stored
watermark, and git resumes from the last commit read per repository. The write
is not. A refresh that reads three new commits still re-encodes and rewrites
the whole index, and pays the full save cost in the table above. Reading a
delta and writing a corpus is the current shape.

For the work WhoToAsk is pointed at today this is affordable. An assessment is a
batch run where the save is ten seconds of a minute-long job, and an
organization of a few hundred people saves in under a third of a second. It
becomes worth fixing for a long-running `serve` deployment at a large
organization doing scheduled refreshes, and the fix is to reuse the encoding of
terms that did not change rather than to change the storage format.

**Holding the graph in memory** is the second cost: 902MB at ten thousand
people, and every process holds its whole graph, so several open at once
multiply it.

**Changing the storage format is not the cheap answer**, despite the file being
the obvious suspect. The at-rest encryption works precisely because the file is
one opaque blob: the vault seals plaintext bytes with AES-256-GCM and writes
the result. An embedded database would mean either giving up whole-database
encryption or taking a cgo dependency, and cgo would break the cross-compiled
release that ships six architecture and platform pairs. Both of those are
load-bearing, so the format stays until something forces the trade.
