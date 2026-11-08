# Connect your tools

A copy-paste recipe for every source peepwho can read. Each one is self-contained:
what you get, the exact credential to create, the command to run, how to verify it
worked, and the fixes for the errors you are most likely to hit.

New to peepwho? Read [GETTING_STARTED.md](GETTING_STARTED.md) first for the big
picture, then come back here to wire in each tool.

## The pattern

Every source works the same way. You index it, then you ask.

    peepwho index --source SOURCE [scope flags] --merge
    peepwho ask "who owns billing retries"

Three things hold for all of them:

- **`--merge` adds a source onto what you already have.** Leave it off only on the
  first source, when you want a clean index. People join across every source by
  email, so one human stays one entry.
- **Credentials are read only from the environment, never a flag.** Nothing is
  logged, and no token is written to disk. The index itself lives at
  `~/.peepwho/index.json`, created readable only by you (mode `0600`), and is never
  uploaded. Everything peepwho learns about your coworkers stays on your machine,
  so you can wire it into work tools and keep that data private. Encrypt the index
  at rest with a key; see [PRIVACY.md](PRIVACY.md).
- **Verify the same way every time.** After any index, run a `peepwho ask` and look
  for the people you expect. `peepwho serve` opens the same data in a browser.

A good order is org chart first, then everything else merged on top:

    peepwho index --source org-csv --file people.csv
    peepwho index --source slack --merge
    peepwho index --source github --github-org your-org --merge

## The guided way: `peepwho connect`

Rather be walked through it? `peepwho connect` is an interactive wizard that does
everything on this page for you, one source at a time. It explains the source,
shows how to create the credential, reads the token without echoing it, validates
it against the API before indexing, runs the first index, and prints the `export`
line to save.

    peepwho connect            # menu of every source, marked configured or not
    peepwho connect slack      # set up one source
    peepwho connect --status   # report what is configured, without prompting

connect keeps the same privacy promise as everything else. The token you type is
held in memory for that one run and never logged. To keep it for later, connect
offers to store it in your OS keychain, the encrypted secret store your other
credentials live in, so future runs read it with no environment variable set.
Decline and it prints the `export` line for your shell profile instead. Either
way it never edits your dotfiles. An environment variable, when set, always wins
over the keychain, so a one-off run can override a stored value. connect needs a
terminal, so scripts and CI keep setting the variables and using `peepwho index`
directly.

The rest of this page is the reference connect automates, and the copy-paste path
for when you would rather not use the wizard.

## No credentials needed

Start here. These read files or local clones and need no tokens.

### Org chart (CSV)

What you get: the backbone of the graph. Names, titles, teams, and topics that
every other source joins onto by email.

    peepwho index --source org-csv --file people.csv

The CSV needs a header row. Column order does not matter and names are matched
loosely, so `Job Title` and `role` both map to title. A minimal file:

    name,email,title,team,topics
    Angela Malone,angela@example.com,Staff Engineer,Payments,billing;retries

Recognized columns: `name`, `email`, `title`, `team`, `org`, `manager`, and
`topics` (semicolon-separated). A row needs at least a name or an email. See the
full column table in [GETTING_STARTED.md](GETTING_STARTED.md#index-your-own-org-chart).

Verify:

    peepwho ask --pretty "who do I talk to about billing retries"

### CODEOWNERS

What you get: who owns which paths, straight from a repository's CODEOWNERS file.

    peepwho index --source codeowners --file ~/src/your-repo --merge

Point `--file` at a repo root or directly at a CODEOWNERS file. Owners that are
GitHub handles join real people through the [alias file](REFERENCE.md#identity-aliases).

### Git history

What you get: who actually commits to what. Reads local clones directly, so it
works for any repo you can clone, including ones with no CODEOWNERS.

    peepwho index --source git --repo-path ~/src/billing --repo-path ~/src/infra --merge

Each author gets the topics of the paths they touch, weighted by how often. Authors
join other sources by their commit email. Bot accounts like dependabot are skipped.
`--git-since-days` bounds the window (default 365) and `--max-commits` caps each repo
(default 2000).

### Slack export (zip)

What you get: everything the live Slack connector reads, from the export zip a
workspace admin downloads at Settings, Import/Export Data. No token, no bot, no
network. This is also the path for indexing a data-room export you were handed.

    peepwho index --source slack-export --file export.zip --episodes --merge

Point `--file` at the zip itself or a folder it was unzipped into. Public
channels are read by default. Pass `--include-private` to read the private
channels a corporate export contains; direct and group messages are never
read, and the run names whatever it skipped. Standard exports omit member
emails unless the admin enabled them, and without emails Slack activity will
not merge with the same people from other sources.

## JSON import  ·  anything else

Any system that can emit JSON can feed peepwho without a dedicated connector.
Produce a JSON array of records and pipe it in:

    curl -s "$CATALOG/people" \
      | jq '[.items[] | {name, email, title, team}]' \
      | peepwho index --source json --file -

Each object may set name, email, title, team, org, manager, topics, and a source
label. It needs no credentials.

## Slack (index)  ·  5 minutes

What you get: the strongest single source. Which channels exist, what they are
about, and who is active on each topic.

**1. Create the app.** Go to https://api.slack.com/apps, choose **Create New App**,
then **From scratch**, and pick your workspace.

**2. Add bot scopes.** Open **OAuth & Permissions**. Under **Bot Token Scopes** add:

    channels:read      channels:history      users:read      users:read.email

To index private channels too, also add `groups:read` and `groups:history`.

**3. Install and copy the token.** Click **Install to Workspace**, then copy the
**Bot User OAuth Token**. It starts with `xoxb-`.

**4. Connect:**

    export PEEPWHO_SLACK_TOKEN=xoxb-your-token
    peepwho index --source slack --merge

A bot only reads channels it has joined, so invite it to the ones that matter:
`/invite @peepwho`. Unreadable channels are skipped with a warning, never fatal.

Tune the depth (defaults: public channels, last 180 days, 5000 messages per channel):

    peepwho index --source slack --merge --since-days 90 --max-messages 2000
    peepwho index --source slack --merge --include-private

Verify:

    peepwho ask "who owns billing retries"

Common fixes:

| Error                              | Fix                                                  |
| ---------------------------------- | ---------------------------------------------------- |
| `api error: invalid_auth`          | Token is wrong or missing a scope. Recreate it with the four scopes above and re-export. |
| A channel you expected is missing  | Invite the bot: `/invite @peepwho` in that channel.   |
| Private channels not showing       | Add `groups:read` + `groups:history`, then `--include-private`. Denied if org policy pins `private_channels: deny`. |

## GitHub  ·  5 minutes

What you get: contributors, PR authors, reviewers, assignees, labels and titles,
issues, repo topics, and CODEOWNERS, weighted by how much each person works on a topic.

**1. Create a token.** Go to https://github.com/settings/tokens and create a token
with read access to the repositories you want. A classic token needs the `repo`
scope; a fine-grained token needs **Read-only** on **Contents**, **Metadata**,
**Pull requests**, and **Issues** for the target repos. Copy it (`ghp_...` or
`github_pat_...`).

**2. Connect** a single repo, or a whole org:

    export PEEPWHO_GITHUB_TOKEN=ghp_your-token
    peepwho index --source github --repo your-org/your-repo --merge
    peepwho index --source github --github-org your-org --github-emails --merge

`--repo` is repeatable. `--github-org` indexes every repo in the org; cap it with
`--max-repos N`. `--github-emails` resolves user emails so GitHub people merge with
Slack and the org chart.

Verify:

    peepwho ask "who knows the payments service"

Common fixes:

| Error                    | Fix                                                          |
| ------------------------ | ------------------------------------------------------------ |
| `401` / `Bad credentials`| Token is wrong or expired. Recreate and re-export.           |
| `404` on a private repo  | Token lacks read access to it. Add the repo (fine-grained) or the `repo` scope (classic). |
| People show as handles   | Add `--github-emails`, or join handles with an [alias file](REFERENCE.md#identity-aliases). |

## Jira  ·  5 minutes

What you get: issue assignees and reporters, weighted by components, labels, summary
words, and project.

**1. Create an API token.** Go to https://id.atlassian.com/manage-profile/security/api-tokens,
click **Create API token**, and copy it.

**2. Connect.** Jira uses your site URL, your account email, and the token:

    export PEEPWHO_JIRA_URL=https://your-site.atlassian.net
    export PEEPWHO_JIRA_EMAIL=you@example.com
    export PEEPWHO_JIRA_TOKEN=your-api-token
    peepwho index --source jira --jira-project SEC --jira-project OPS --merge

`--jira-project` is repeatable. For anything more specific, use `--jira-jql` with a
raw JQL query. `--max-issues` caps how many issues are read (default 1000).

Verify:

    peepwho ask "who works on the OPS project"

Common fixes:

| Error                    | Fix                                                          |
| ------------------------ | ------------------------------------------------------------ |
| `401` / `403`            | Check `PEEPWHO_JIRA_EMAIL` matches the account that owns the token, and the URL has no trailing path. |
| No people returned       | The project key is wrong or you lack access. Confirm the key in Jira and try `--jira-jql`. |

## Confluence  ·  2 minutes (if Jira is already set up)

What you get: page creators and last editors, weighted by labels, title words, and space.

Confluence uses the **same Atlassian site and token as Jira**, so if you set up Jira
above, the credentials already work:

    peepwho index --source confluence --confluence-space ENG --confluence-space OPS --merge

To use a different site or token, set the Confluence-specific variables (they fall
back to the Jira ones when unset):

    export PEEPWHO_CONFLUENCE_URL=https://your-site.atlassian.net
    export PEEPWHO_CONFLUENCE_EMAIL=you@example.com
    export PEEPWHO_CONFLUENCE_TOKEN=your-api-token

`--confluence-space` is repeatable. Use `--confluence-cql` for a raw CQL query, and
`--max-pages` to cap pages read (default 2000).

Verify:

    peepwho ask "who wrote the onboarding docs"

## PagerDuty  ·  2 minutes

What you get: every service and the people currently on call, so each on-call person
gets the topics of the services they answer for. This source describes the present,
so it never decays.

**1. Create a token.** In PagerDuty, go to **Integrations > API Access Keys** and
create a read-only API key.

**2. Connect:**

    export PEEPWHO_PAGERDUTY_TOKEN=your-api-token
    peepwho index --source pagerduty --merge

Verify:

    peepwho ask "who is on call for search"

## Matters (billing or practice-management CSV)  ·  2 minutes

What you get: who billed time to which matter, in which practice area, doing
what. Billing narratives are the richest expertise signal a professional-
services firm has: people describe their own work, per matter, dated. Every
practice-management and billing system exports this as CSV, so no API
integration is needed.

**1. Export time entries.** Any range works; the last year or two is plenty.
Recognized columns, matched case-insensitively in any order: timekeeper email,
timekeeper or attorney name, matter, practice area, narrative, date.

**2. Connect:**

    peepwho index --source matters --file time-entries.csv --merge

Try it on the sample first:

    peepwho index --source matters --file examples/matters.csv

Practice areas and matter names count as stated expertise; narrative words are
mined and only corroborate. Entries from the last 180 days feed recency, so
current work outranks history.

## Microsoft Graph (org chart)  ·  2 minutes

What you get: the live org chart, every person and their reporting line, read from
Microsoft Graph (Entra ID). It is the org-chart source that stays current without
re-exporting a CSV.

**1. Get a token.** Obtain a bearer token that can read `User.Read.All`.

**2. Connect:**

    peepwho connect graph

Or by hand:

    export PEEPWHO_GRAPH_TOKEN=your-graph-token
    peepwho index --source graph --merge

For a sovereign or national cloud, point `PEEPWHO_GRAPH_URL` at that tenant's Graph
root.

## Slack bot  ·  let your team ask from Slack

Once the index is built, run the bot so teammates can ask peepwho in Slack directly,
by mentioning it, sending a direct message, or using `/peepwho`. This is separate from
indexing Slack: it needs a few more scopes on the same app.

**1. Add bot scopes.** On top of the read scopes above, add:

    chat:write      app_mentions:read      im:history      im:read

**2. Subscribe to events.** Under **Event Subscriptions**, subscribe the bot to
`app_mention` and `message.im`.

**3. Add the slash command (optional).** Under **Slash Commands**, create `/peepwho`.
Over Socket Mode the request URL can be any placeholder; over the Events API, point it
at `https://your-host/slack/commands`.

**4. Run it.** Pick a transport.

Socket Mode needs no public URL, best for a laptop or internal host. Enable Socket
Mode, create an app-level token (`xapp-`) with the `connections:write` scope, then:

    export PEEPWHO_SLACK_TOKEN=xoxb-...
    export PEEPWHO_SLACK_APP_TOKEN=xapp-...
    peepwho bot --transport socket

Events API serves a public HTTP endpoint, best for a hosted deployment. Point the
Slack request URL at `https://your-host/slack/events`, then:

    export PEEPWHO_SLACK_TOKEN=xoxb-...
    export PEEPWHO_SLACK_SIGNING_SECRET=...
    peepwho bot --transport events --addr 0.0.0.0:8766

In Slack, `--llm` or `--keyword` at the end of a message picks the mode for that one
answer. See [GETTING_STARTED.md](GETTING_STARTED.md#slack-bot) for the answer-mode and
rate-limit details.

## Microsoft Teams

On the roadmap, not yet available. peepwho has no Teams connector today. If you want it,
open an issue so it gets prioritized. In the meantime, the Slack bot and the web UI
cover the same ask-from-chat need.

## Connect everything

A full internal setup, org chart first, then every tool merged onto it:

    export PEEPWHO_SLACK_TOKEN=xoxb-...
    export PEEPWHO_GITHUB_TOKEN=ghp_...
    export PEEPWHO_JIRA_URL=https://your-site.atlassian.net
    export PEEPWHO_JIRA_EMAIL=you@example.com
    export PEEPWHO_JIRA_TOKEN=...
    export PEEPWHO_PAGERDUTY_TOKEN=...

    peepwho index --source org-csv --file people.csv
    peepwho index --source slack      --merge
    peepwho index --source github     --github-org your-org --github-emails --merge
    peepwho index --source jira       --jira-project ENG --merge
    peepwho index --source confluence --confluence-space ENG --merge
    peepwho index --source pagerduty  --merge
    peepwho index --source git        --repo-path ~/src/billing --merge

    peepwho ask "who knows the billing service"
    peepwho serve

Each run prints what joined and left since the last index, like `+3 people, -1 people,
+1 channels`. Re-running any single source with `--merge` refreshes just that slice.

## Where to go next

- [REFERENCE.md](REFERENCE.md): every command, flag, and environment variable.
- [GETTING_STARTED.md](GETTING_STARTED.md): the narrative walkthrough, ask modes,
  the web UI, MCP, and organization policy.
- Joining one person across sources: [identity aliases](REFERENCE.md#identity-aliases).
