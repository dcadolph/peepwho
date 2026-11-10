# Getting started with WhoToAsk

This guide takes you from nothing to a working setup: install the tool, index a
source, and ask "who do I talk to about X" from the terminal or a browser.

In a hurry to wire in one tool? [CONNECT.md](CONNECT.md) has a short copy-paste
recipe for each source (Slack, GitHub, Jira, Confluence, PagerDuty, git), with
the exact credential to create and how to verify it worked. This guide is the
fuller walkthrough with the concepts behind it.

## What WhoToAsk does

You give WhoToAsk your work data (an org chart, a Slack workspace) and it builds a
local, searchable map of who knows what and which channel to ask in. You then
ask a plain-language question and get a ranked list of people and channels, with
a short reason for each. It works without any model. If you have a local model
through Ollama, it can also write a one-line recommendation.

Everything stays on your machine by default. Nothing is uploaded unless you
explicitly change the policy.

## Requirements

- Go 1.26 or newer to build from source.
- Optional: a Slack bot token if you want to index Slack.
- Optional: Ollama if you want the LLM answer mode.

## Install

With Homebrew on macOS:

    brew install --cask dcadolph/tap/whotoask

With Go 1.26 or newer:

    go install github.com/dcadolph/whotoask@latest

Or download an archive for your platform from the
[releases page](https://github.com/dcadolph/whotoask/releases) and put the
`whotoask` binary on your `PATH`.

Check it runs:

    whotoask version

The examples below use `whotoask`. From a source checkout without installing, use
`go run .` from the repository instead.

## Verify a download

Every release is signed with a keyless Sigstore signature from this repository's
release workflow, and every archive carries SLSA build provenance. Download
`checksums.txt`, `checksums.txt.sig`, and `checksums.txt.pem` alongside your
archive, then:

    id='^https://github.com/dcadolph/whotoask/\.github/workflows/release\.yml@refs/tags/v'
    cosign verify-blob checksums.txt \
      --signature checksums.txt.sig \
      --certificate checksums.txt.pem \
      --certificate-identity-regexp "$id" \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com
    sha256sum -c checksums.txt --ignore-missing
    gh attestation verify whotoask_<version>_linux_amd64.tar.gz --repo dcadolph/whotoask

The first command proves this repository's release workflow signed the checksum
file, the second proves your archive matches it, and the third proves the
archive was built by that workflow. On macOS without GNU coreutils, use
`shasum -a 256 -c checksums.txt --ignore-missing` for the second.

## Try it in sixty seconds

The fastest look is the demo, run against a repository you already have:

    whotoask demo --repo .

It reads that repository's git history and opens on the exposure view: each
directory with enough work to claim about, the people that work rests on, and
the ones where a single person covers most of it. Nothing is fetched from the
network and nothing is written outside a temporary directory.

Check any row it gives you against the history it was counted from:

    git log --format='%an' -- path/it/named | sort | uniq -c | sort -rn

That is the point of running it on your own code. A simulated company can show
more sources, but you cannot check it against anything.

If you would rather see those other sources, leave `--repo` off:

    whotoask demo

That builds a simulated company across all eight sources and opens on an
answered question. Click a name for details, try "who owns terraform", vote on
a result. Sample data only; it is discarded when the demo stops.

For the command-line loop, the repository ships a small example org chart:

    whotoask index --source org-csv --file examples/people.csv
    whotoask ask --pretty "who do I talk to about billing retries"

You should see a ranked list of people, each with a score and the reason it
matched, such as "retries (topic)" or "billing (team)". That is the whole loop:
index a source, then ask.

Adding the rest of your tools is the same loop. For a guided setup that validates
the credential and runs the first index, run `whotoask connect`; the sections below
are the manual path.

## Index your own org chart

WhoToAsk reads a CSV with a header row. Column order does not matter and the
header names are matched loosely, so "Job Title" and "role" both map to title.
Recognized columns:

| Column  | Also accepts                         | Meaning                         |
| ------- | ------------------------------------ | ------------------------------- |
| name    | full name, employee, person          | Display name                    |
| email   | mail, email address                  | Used to merge with other sources |
| title   | job title, role                      | Job title                       |
| team    | department, dept, group              | Team name                       |
| org     | organization, division, business unit | Organization name              |
| manager | manager email, reports to            | Manager identifier              |
| topics  | skills, tags, expertise              | Semicolon-separated topics      |

A row needs at least a name or an email. Topics are split on semicolons by
default. A minimal file looks like this:

    name,email,title,team,topics
    Angela Malone,angela@example.com,Staff Engineer,Payments,billing;retries

Index it:

    whotoask index --source org-csv --file /path/to/your/people.csv

## Index Slack

Slack is the strongest source, because it shows which channels exist, what they
are about, and who is active on each topic. There are two ways in: a workspace
export file, which needs no app and no token, or a bot token that can also
re-read history on a schedule.

### The no-token way: a workspace export

A workspace admin can download an export zip at Settings and administration,
Workspace settings, Import/Export Data. Index it directly:

    whotoask index --source slack-export --file export.zip --episodes

Nothing touches the network. Public channels are read by default,
`--include-private` reads the private channels a corporate export contains,
and direct messages are never read. This is also the path when someone hands
you an export to analyze rather than access to the workspace itself.

### Create a Slack app and token

1. Go to https://api.slack.com/apps and choose Create New App, then From
   scratch. Pick your workspace.
2. Open OAuth and Permissions. Under Bot Token Scopes add: `channels:read`,
   `channels:history`, `users:read`, and `users:read.email` (add `channels:join`
   to let `--slack-join` self-join public channels). To index private
   channels as well, also add `groups:read` and `groups:history`.
3. Install the app to the workspace and copy the Bot User OAuth Token. It starts
   with `xoxb-`.
4. Export it. WhoToAsk reads the token only from the environment, never a flag:

       export WHOTOASK_SLACK_TOKEN=xoxb-your-token

### Run the index

    whotoask index --source slack

By default this reads public channels and the last 180 days of history, capped
at 5000 messages per channel. A bot can only read the history of channels it has
joined. Invite it by hand (`/invite @whotoask`) to the channels that matter, or
add the `channels:join` scope and pass `--slack-join` to have it join every
public channel itself, so a whole workspace indexes without manual invites; it
posts a join notice in each channel it enters, and private channels still need
an invite. Unreadable channels are skipped with a warning, not fatal. Tune the
depth:

    whotoask index --source slack --since-days 90 --max-messages 2000

To include private channels the token can read:

    whotoask index --source slack --include-private

### What gets stored

Message text is tokenized and kept in the local index only, alongside who posted
about what. The token itself is never written to disk and never logged. Nothing
is sent to any third party.

## Ask questions

    whotoask ask "who owns billing retries"

At a terminal you get a readable, colored answer. Piped or redirected, you get
JSON on stdout, so you can feed it to other tools. Add `--pretty` to indent that
JSON, `--json` to force JSON at a terminal, or `--human` to force the readable
form through a pipe. Useful flags:

- `--limit N` caps results per section (default 5).
- `--mode keyword` (default) ranks with no model.
- `--mode llm` adds a local model. See the next section.

The answer has two sections. People are who to talk to. Channels are where to
ask, each with its most active members for that topic.

Each keyword-mode result carries a `strength` from zero to one: how much of
the question matched, scaled by how strong the match is. It is deterministic
scoring, not a probability, which is why it is not called a confidence. An explicit topic
counts as proof, a job title slightly less, a passing mention in chat half.
The web UI and the Slack bot show it as strong, moderate, or weak, so a
least-bad answer never dresses up as a sure one.

## Confirm or correct answers

When an answer is right, say so; when it is wrong, say that too. Votes adjust
future rankings for that question and its close variants:

    whotoask feedback record "billing retries" --person alice@example.com --helpful
    whotoask feedback record "billing retries" --channel payments --not-helpful \
      --comment "bot answers there now"

Review or undo what has been taught, and tune how hard votes move ranking:

    whotoask feedback list --pretty
    whotoask feedback clear --person alice@example.com
    whotoask ask --feedback high "billing retries"

The web UI has the same buttons on every result. Votes live in
`feedback.json` next to the index, separate on purpose: re-indexing rebuilds
the graph but keeps what people taught it. Boosted or lowered results say so
in their reasons, and a few votes adjust ranking without ever burying the
underlying evidence.

## LLM mode

LLM mode retrieves candidates with the index, then asks a local model to rank
them and write a short recommendation. The model only ever sees the retrieved
candidates, so it cannot invent a person or a channel.

1. Install Ollama from https://ollama.com and start it.
2. Pull a model:

       ollama pull llama3.1

3. Ask:

       whotoask ask --mode llm "who do I talk to about billing retries"
       whotoask ask --mode llm --model qwen2.5 "where do I ask about kafka"

Ollama runs on your machine, so LLM mode is allowed under the default strict
policy. Pointing `--ollama-url` at a non-local host counts as leaving the
machine and is refused unless the policy permits it.

## Cloud models (Claude, Gemini, OpenAI, and compatible servers)

By default nothing leaves the machine. If you explicitly opt in, llm mode can
use a cloud model instead of Ollama:

    export WHOTOASK_ANTHROPIC_KEY=...
    whotoask ask --mode llm --provider anthropic --policy redacted "who owns billing"

    export WHOTOASK_GEMINI_KEY=...
    whotoask ask --mode llm --provider gemini --policy redacted "who owns billing"

    export WHOTOASK_OPENAI_KEY=...
    whotoask ask --mode llm --provider openai --policy redacted "who owns billing"

The policy decides what the model sees. Under `--policy redacted`, the
question goes out as you typed it, people leave as anonymized numbered roles
(title, team, and matched query terms), and channels leave as numbered
matched terms, with no names, no emails, no channel names, and no message
text. The question is the one part you control: if you type a person's name
into it, that name goes to the model. The model returns numbers, WhoToAsk maps
them back, and the summary is written locally. Redacted egress is limited to
the known provider hosts, so a remote `--openai-url` needs `--policy open`.
Under `--policy open`, candidates go as-is. Under the default strict policy,
cloud providers are refused, and a locked org policy can pin that permanently.

The `openai` provider speaks the common chat-completions format, so
`--openai-url` also points it at local servers like LM Studio or vLLM; a local
URL needs no policy opt-in at all. Keys are read only from the environment and
are never logged or stored.

## Semantic search (Meaning mode in the web UI)

Keyword search matches words. Semantic search matches meaning, so "who handles
failed payments" can find the person tagged with "billing retries" even with no
shared word. It uses a local embedding model through Ollama.

1. Pull an embedding model:

       ollama pull nomic-embed-text

2. Build the index with embeddings:

       whotoask index --source org-csv --file examples/people.csv --embed

3. Search by meaning, or let the LLM use semantic retrieval:

       whotoask ask --mode semantic "who handles failed payments"
       whotoask ask --mode llm "who handles failed payments"

Embeddings are stored in the index. The semantic mode ranks by them directly,
and the llm mode uses them to retrieve candidates before the model ranks and
summarizes. Set the model with --embed-model. The embedder runs locally, so it
is allowed under the strict policy.

## Web UI

Prefer a search box to the terminal:

    whotoask serve

Open http://127.0.0.1:8765, type a question, and pick Keyword, Meaning, or
AI. Picking AI reveals a provider choice (local Ollama, Claude, ChatGPT, or
Gemini) with live readiness hints saying what each needs. The sidebar also
browses everything indexed: people, channels, teams, and topics, each
filterable, and clicking a topic asks about it.

### Serving with AI enabled

An amber dot on a provider means it needs something; its tooltip says what.
The recipes:

Ollama (private, everything stays on this machine, allowed under the default
strict policy):

    # install from ollama.com, then:
    ollama pull llama3.1
    whotoask serve

Claude, ChatGPT, or Gemini (cloud): export the provider's key and start the
server with a policy that permits cloud egress, since the default strict
policy keeps everything local:

    export WHOTOASK_ANTHROPIC_KEY=...   # or WHOTOASK_OPENAI_KEY / WHOTOASK_GEMINI_KEY
    whotoask serve --policy redacted

Redacted sends the model only your question and anonymized numbered
candidates; `--policy open` sends full candidate detail. Keys are read only
from the environment and are never typed into the browser. The provider
choice applies per question, and keyword mode keeps working no matter what.
The same flags work on `whotoask demo` if you want to try cloud AI against the
sample company first.

### Meaning mode

Meaning mode matches by meaning instead of exact words, so "failed payments"
can find the person tagged "billing retries". It needs the index built once
with `--embed`; see the Semantic search section below for the two commands. The
server binds to localhost only, so it is not reachable from the network. Stop
it with Ctrl-C; it shuts down cleanly. Change the address with `--addr`;
binding beyond localhost requires `WHOTOASK_SERVE_TOKEN`, and every request
must then carry the token. See docs/DEPLOY.md for the token flow.

## Claude Code and other agents (MCP)

Let an agent ask WhoToAsk mid-conversation. The MCP server speaks stdio, so
registration is one line:

    claude mcp add whotoask -- whotoask mcp

For Claude Desktop, add this under `mcpServers` in
`claude_desktop_config.json`:

    {"whotoask": {"command": "whotoask", "args": ["mcp"]}}

The agent gets four tools: `whotoask_ask` (ranked people and channels with
reasons and match strength, keyword or semantic), `whotoask_recall` (the past
conversations one person took part in), `whotoask_person` (a full profile),
and `whotoask_directory` (browse people, channels, teams, or topics). There is no
llm mode over MCP on purpose: the calling agent is already a model, so it reads
the ranked candidates itself.

One thing to be clear-eyed about: answers flow to whichever agent you wire
this into, and on to that agent's model. Registering the server is the
opt-in.

## Slack bot

Let your team ask WhoToAsk from Slack directly. They mention the bot in a channel
or send it a direct message, and the bot replies in place. Adding `--llm` to a
message uses the model for that answer, and `--keyword` forces the fast path.

### Scopes and events

In addition to the read scopes from the Slack section, add the bot scopes
`chat:write`, `app_mentions:read`, `im:history`, and `im:read`. Under Event
Subscriptions, subscribe the bot to `app_mention` and `message.im`.

### The /whotoask slash command

Ask from anywhere without mentioning the bot:

    /whotoask who owns billing retries

In the app config, open Slash Commands and create `/whotoask`. Over Socket
Mode it just works; the request URL field can hold any placeholder. Over the
Events API, point the request URL at `https://your-host/slack/commands`; the
same signing secret verifies it. The answer arrives through Slack's response
URL, visible in the channel, and the `--llm`/`--keyword` hints work in the
command text too.

### Socket Mode (no public URL)

Best for a laptop or an internal host. Enable Socket Mode, create an app-level
token that starts with `xapp-` with the `connections:write` scope, then run:

    export WHOTOASK_SLACK_TOKEN=xoxb-...
    export WHOTOASK_SLACK_APP_TOKEN=xapp-...
    whotoask bot --transport socket

### Events API (public endpoint)

Best for a hosted deployment. Point the Slack request URL at
https://your-host/slack/events and run:

    export WHOTOASK_SLACK_TOKEN=xoxb-...
    export WHOTOASK_SLACK_SIGNING_SECRET=...
    whotoask bot --transport events --addr 0.0.0.0:8766

The events transport verifies the Slack request signature and rejects requests
with an old timestamp.

### Default answer mode

By default the bot answers with the keyword resolver. Set `--mode llm` to make
the model the default, in which case Ollama must run on the bot's host. Either
way, a user overrides per message with a trailing `--llm` or `--keyword`.

## GitHub, Jira, and Confluence

Index code, tickets, and wiki pages to learn who works on what.

GitHub needs a token in WHOTOASK_GITHUB_TOKEN with repository read access:

    export WHOTOASK_GITHUB_TOKEN=ghp-...
    whotoask index --source github --repo your-org/your-repo
    whotoask index --source github --github-org your-org --github-emails

It reads contributors, pull request authors, reviewers, assignees, labels and
titles, non-pull-request issues, repository topics, and CODEOWNERS, weighted by
how much each person works on a topic.

Jira needs a site URL and an API token, created at id.atlassian.com:

    export WHOTOASK_JIRA_URL=https://your-site.atlassian.net
    export WHOTOASK_JIRA_EMAIL=you@example.com
    export WHOTOASK_JIRA_TOKEN=...
    whotoask index --source jira --jira-project SEC --jira-project OPS

It reads issue assignees and reporters, weighted by components, labels, summary
words, and project. Use --jira-jql for a custom query. When emails are visible,
these people merge with Slack and the org chart by email.

Confluence uses the same Atlassian site and token as Jira, so the Jira
credentials work, or set WHOTOASK_CONFLUENCE_URL, WHOTOASK_CONFLUENCE_EMAIL, and
WHOTOASK_CONFLUENCE_TOKEN:

    whotoask index --source confluence --confluence-space ENG --confluence-space OPS

It reads page creators and last editors, weighted by labels, title words, and
space. Use --confluence-cql for a custom query.

## PagerDuty

Index services and on-call schedules to learn who answers for what. Create a
read-only API token in PagerDuty and export it:

    export WHOTOASK_PAGERDUTY_TOKEN=...
    whotoask index --source pagerduty --merge

It reads every service and the people currently on call, giving each on-call
person the topics of the services they answer for.

## Git history

Index who actually commits to what. No tokens, no API: it reads local clones
directly, so it works for any repository you can `git clone`, including ones
with no CODEOWNERS file.

    whotoask index --source git --repo-path ~/src/billing --repo-path ~/src/infra --merge

Each author gets the topics of the paths they touch, weighted by how often
they touch them, so the person doing the work outranks a drive-by. Authors
join other sources by commit email. Bot accounts such as dependabot are
skipped. `--git-since-days` bounds the window (default 365) and
`--max-commits` caps each repository (default 2000).

## Where your data lives

The index is written to `~/.whotoask/index.json` by default, readable only by
your user. Override the location with `--data-dir`. This file holds the
indexed text, so treat it like the source data. It is never uploaded.

## Organization policy

The policy governs model egress and is enforced, not advisory. The default is
strict: answers are computed locally, and only the keyword resolver and a
local model are allowed. Indexing is separate from the policy: it talks only
to the sources you name, with your tokens, when you run it.

An organization pins behavior with a policy file at `/etc/whotoask/policy.json`.
When that file sets `locked`, it wins over both the `--policy` flag and the
`WHOTOASK_POLICY_FILE` environment variable, so a user cannot point WhoToAsk at a
looser file. `WHOTOASK_POLICY_FILE` remains useful on unmanaged machines and in
tests. See `examples/policy.json`:

    {
      "mode": "strict",
      "locked": true,
      "private_channels": "deny"
    }

With the file above, `--policy open` is ignored and `--include-private` is
refused. The lock constrains the installed binary for regular users; it is
not a security boundary against an administrator. This is how a cautious
organization keeps a managed install locked down while an individual running
their own copy stays free to opt in.

## Updating the index

By default a new run replaces the index, so people who left the org or channels
that went away drop out and new ones appear. To combine sources instead, add
`--merge` and the run adds onto the existing index. People merge by email, so one
human stays one entry across the org chart, Slack, GitHub, Jira, Confluence,
PagerDuty, and code ownership.

Start with the org chart, then merge every other source onto it:

    whotoask index --source org-csv --file people.csv
    whotoask index --source slack --merge
    whotoask index --source github --github-org your-org --merge
    whotoask index --source jira --jira-project PROJ --merge
    whotoask index --source confluence --confluence-space ENG --merge
    whotoask index --source pagerduty --merge
    whotoask ask "who knows the billing service"

Each run prints what joined and left since the last index, for example
"+3 people, -1 people, +1 channels". Add `--changes-file changes.json` to write
the full diff as JSON for a script or a report.

Re-indexing Jira, Confluence, GitHub, or Slack with `--merge` is incremental: it
fetches only what changed since the last run and folds it in, so a scheduled
refresh stays fast on a large tracker or workspace. A per-source watermark is
kept beside the index. Add `--full` to re-read everything and recompact, which
also picks up the few things an incremental run skips, such as a Slack message
edited after the window. Other sources always read in full.

## Joining one person across sources

People merge by email. When a source only knows a handle or an account id, such
as a GitHub login without a public email or a CODEOWNERS `@handle`, the same
human shows up as two entries. An alias file declares which identifiers belong
to the same person:

    {
      "alice@example.com": ["github:alice", "codeowners:alice"]
    }

Pass it once with `--aliases` and the index joins the entries, including ones
indexed before the file existed:

    whotoask index --source github --github-org your-org --merge --aliases aliases.json

The mapping is saved in the index, so later runs keep joining without the flag.
Joined identifiers appear in answers under `identities`, and a person's email
always wins as the display identifier. See `examples/aliases.json`.

Most of it needs no alias file. Indexing infers the merges it can defend and
records the evidence for each one, which `whotoask identity` prints: a handle
matching exactly one person's name or mailbox, a handle matching a domain only
one person writes from, one distinctive mailbox name at two domains, the case
where somebody commits from a GitHub noreply address under their real name
and from an ordinary mailbox signing with their login, and two records of
the same name whose mailboxes spell it differently. That last one refuses
mailboxes that differ only by a number, since a company tells two
same-named colleagues apart exactly that way. Anything a rule
cannot pin to exactly one person is left alone and listed as ambiguous, since
a wrong merge is far more expensive than a missed one, and the alias file is
the override for those.

## Recent activity counts more

Activity ages. Someone who owned a topic three years ago is usually the wrong
person to ask today, so dated records decay: a record loses half its weight per
half-life, 180 days by default. Slack messages, GitHub pull requests and
issues, Jira issues, and Confluence pages all carry their activity date. The
org chart, CODEOWNERS, and PagerDuty on-call describe the present and never
decay.

Tune it at index time:

    whotoask index --source slack --merge --half-life-days 90

A shorter half-life favors the people active right now; `--half-life-days 0`
turns decay off entirely.

## Sealing a finding for someone outside the team

When a knowledge-risk finding has to hold up in front of a board, an auditor,
or an acquirer, seal it:

    whotoask attest --out finding

That writes `risk.json` with a signed manifest beside it. Anyone can check it
offline, with no account and nothing sent anywhere:

    whotoask verify finding

It names any file that was altered or is missing and exits non-zero if so. To
prove the seal came from your install, publish the output of `WhoToAsk attest
key`, and have the reader pass that key to `WhoToAsk verify --key`.

## Troubleshooting

| Message                                             | Cause                              | Fix                                          |
| --------------------------------------------------- | ---------------------------------- | -------------------------------------------- |
| no index: run `whotoask index` first                  | No index built yet                 | Run a `whotoask index` command                 |
| invalid arguments: set WHOTOASK_SLACK_TOKEN           | Slack token not exported           | `export WHOTOASK_SLACK_TOKEN=xoxb-...`         |
| private-channel ingest is disabled by policy        | Policy denies private channels     | Drop `--include-private` or adjust the policy |
| policy: egress denied: model host ... needs --policy open | A non-local model under strict or redacted | Use a local URL, or `--policy open` to send profiles |
| llm: model error: cannot reach a local model ...    | Ollama is not running              | Start it with `ollama serve` and pull a model |
| slack ...: api error: invalid_auth                  | Bad token or missing scope         | Recreate the token with the listed scopes    |
| --policy ignored; pinned by org policy              | A locked org policy is in effect   | Expected. Ask your administrator             |

## Command reference

- `whotoask index --source org-csv --file FILE` builds the index from a CSV.
- `whotoask index --source slack [--include-private] [--since-days N] [--max-messages N]`
  builds the index from Slack.
- `whotoask index --source github (--repo owner/name | --github-org ORG)` indexes GitHub.
- `whotoask index --source jira (--jira-project KEY | --jira-jql JQL)` indexes Jira.
- `whotoask index --source confluence (--confluence-space KEY | --confluence-cql CQL)`
  indexes Confluence.
- `whotoask index --source pagerduty` indexes PagerDuty services and on-call.
- `whotoask index --source git --repo-path DIR [--git-since-days N] [--max-commits N]`
  indexes local git history.
- `whotoask demo --repo .` runs the web UI over a repository you already have and
  opens on what rests on one person; without `--repo` it explores a simulated
  company across all eight sources. Either way, no credentials.
- `whotoask connect [source]` walks through setting up a source's credentials, and
  `whotoask connect --status` reports what is configured.
- `whotoask index ... --merge` adds the source to the existing index instead of replacing it.
- `whotoask index --source slack ... --episodes [--archive]` records the
  conversations behind the messages; `--archive` retains their text and needs an
  encryption key.
- `whotoask ask [--mode keyword|semantic|llm] [--limit N] [--pretty] QUESTION`
  answers who to talk to.
- `whotoask recall [--me EMAIL] [--meaning] QUESTION` finds the past conversation
  where something was worked out, and who was in it.
- `whotoask directory [people|channels|teams|topics]` lists what is indexed.
- `whotoask status` shows the counts, per-source sizes, build time, whether
  embeddings and encryption are set.
- `whotoask index ... --embed` adds embeddings for semantic and llm retrieval.
- `WhoToAsk feedback record QUESTION (--person ID | --channel NAME)
  (--helpful | --not-helpful) [--comment TEXT]` records a vote; `WhoToAsk feedback list`
  and `whotoask feedback clear` review and undo votes.
- `whotoask serve [--addr HOST:PORT] [--mode keyword|semantic|llm]` runs the web UI.
- `whotoask bot [--transport socket|events] [--mode keyword|semantic|llm] [--addr HOST:PORT]`
  runs the Slack bot.
- `whotoask mcp` serves the index to an MCP client such as an editor assistant.
- `whotoask vault` and `whotoask archive` manage the encrypted store.
- `whotoask attest`, `whotoask assess`, and `whotoask verify` seal a finding and check
  a seal offline.
- `whotoask version` prints the version.

Shared flags: `--data-dir` sets the index location, `--policy` sets the egress
mode, `--pretty` indents JSON. Set `WHOTOASK_INDEX_KEY` (base64 of 32 random bytes)
or `WHOTOASK_INDEX_PASSPHRASE` to encrypt the index and episodes at rest.

## Scale

The index and its embeddings load fully into memory to answer a question, so
size scales with people and, when `--embed` is set, with the vector dimension.
A few thousand people is comfortable on a laptop; embeddings roughly multiply the
file size, so a large org that wants semantic search should expect an index in
the hundreds of megabytes and size the serving host accordingly. `whotoask status`
reports the current people and source counts.
