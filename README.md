<p align="center">
  <img src="docs/banner.svg" width="100%" alt="Socrates, the WhoToAsk owl">
</p>

<h1 align="center">WhoToAsk</h1>

<p align="center">
  <a href="https://whotoask.app"><img src="https://img.shields.io/badge/whotoask.app-Get_started-8B4A1C?style=for-the-badge" alt="whotoask.app: Get started"></a>
</p>

<p align="center">
  <a href="https://github.com/dcadolph/whotoask/actions/workflows/ci.yml"><img src="https://github.com/dcadolph/whotoask/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/dcadolph/whotoask/releases/latest"><img src="https://img.shields.io/github/v/release/dcadolph/whotoask?color=8B4A1C" alt="Latest release"></a>
  <a href="https://github.com/dcadolph/homebrew-tap"><img src="https://img.shields.io/badge/homebrew-dcadolph%2Ftap%2Fwhotoask-FBB040?logo=homebrew&logoColor=white" alt="Homebrew: dcadolph/tap/whotoask"></a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-8B4A1C" alt="License: MIT"></a>
</p>

<p align="center">
  <img src="docs/demo.gif" width="100%" alt="whotoask ask answering who knows about payments retry and kafka consumer lag, with people, channels, match strength, and reasons">
</p>

Ask a question about your work. WhoToAsk tells you who on your team knows the answer, why, and
which channel to post it in. It also shows where only one person knows how something works, and
when the person who fixed something has moved on, it finds the conversation where they fixed it.

It runs on your machine, with or without an AI model.

## What this is not for

WhoToAsk describes where knowledge and systems are at risk. It never ranks, scores, or labels
people, and its findings must not be used for performance reviews, layoffs, or any other
employment decision. A person who shows up as the only one who knows something is a sign the
team needs another person to know it.

## Install

With Homebrew on macOS:

    brew install --cask dcadolph/tap/whotoask

With a Go toolchain:

    go install github.com/dcadolph/whotoask@latest

Or download an archive for Linux, macOS, or Windows from the
[releases page](https://github.com/dcadolph/whotoask/releases).

### Verifying a release

`checksums.txt` carries a keyless Sigstore signature made by this repository's release
workflow, and every archive carries SLSA build provenance. Check a download before you run it:

```
id='^https://github.com/dcadolph/whotoask/\.github/workflows/release\.yml@refs/tags/v'
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c checksums.txt --ignore-missing
gh attestation verify whotoask_<version>_linux_amd64.tar.gz --repo dcadolph/whotoask
```

On macOS without GNU coreutils, use `shasum -a 256 -c checksums.txt --ignore-missing` for the
second line.

## Quickstart

Start with a repository you already have. WhoToAsk reads its git history and opens on what it
found: the directories the work rests on, and which of them rest on one person. Nothing is
fetched and no credentials are needed.

    whotoask demo --repo .

Every name it gives you can be checked against the same history:

    git log --format='%an' -- path/it/named | sort | uniq -c | sort -rn

Without `--repo`, the demo builds a simulated company across every source, which shows what a
git-only run cannot, and the sidebar has a two-minute guided tour.

Then index an org chart and ask. The example chart lives in this repository, so fetch it first:

    curl -sLO https://raw.githubusercontent.com/dcadolph/whotoask/main/examples/people.csv
    whotoask index --source org-csv --file people.csv
    whotoask ask --pretty "who do I talk to about billing retries"

When the person who knew has left, ask what happened instead of who to ask. `recall` finds the
conversation where something was worked out and answers with a pointer: the people, the place,
the date, and a link back to the tool it happened in. It covers conversations you took part in,
so it needs to know who you are, from `--me`, `WHOTOASK_ME`, or your git email:

    whotoask recall "certificate renewal"

`whotoask connect` walks through the rest of your tools one at a time. It explains each source,
reads the token without echoing it, checks it, and runs the first index. Each source also has a
copy-paste recipe in [docs/CONNECT.md](docs/CONNECT.md).

## Sources

| Source         | Reads                                          | Credentials                |
| -------------- | ---------------------------------------------- | -------------------------- |
| `org-csv`      | Org chart CSV: names, titles, teams, topics    | none                       |
| `matters`      | Time-entry or matter CSV from a billing system | none                       |
| `codeowners`   | CODEOWNERS paths per owner                     | none                       |
| `git`          | Commit authors per changed path                | none                       |
| `slack`        | Users, channels, and message history           | `WHOTOASK_SLACK_TOKEN`     |
| `slack-export` | A Slack workspace export zip                   | none                       |
| `github`       | Repos, contributors, PRs, issues, CODEOWNERS   | `WHOTOASK_GITHUB_TOKEN`    |
| `jira`         | Issue assignees and reporters                  | `WHOTOASK_JIRA_*`          |
| `confluence`   | Page creators and editors                      | `WHOTOASK_CONFLUENCE_*`    |
| `pagerduty`    | Services and current on-calls                  | `WHOTOASK_PAGERDUTY_TOKEN` |
| `graph`        | Microsoft Graph users and reporting lines      | `WHOTOASK_GRAPH_TOKEN`     |
| `json`         | A JSON array of records from a file or stdin   | none                       |

Every source feeds one graph of people, teams, topics, and channels. One human stays one node:
sources join by email, GitHub noreply logins fold in on their own, measured rules link handles
and second mailboxes, and an alias file covers the rest.

## Asking and ranking

Explicit signals such as ownership files and on-call rotations stay strong, while repetition
saturates, so the person who owns a thing outranks the person who talks about it most. Recent
work counts for more. Every answer carries its match strength and says which words matched
where. Confirm or correct an answer and later rankings move with it.

Keyword answers need no model and always work. Semantic search and written answers run on a
local Ollama model, or on Claude, Gemini, or OpenAI once you opt in. The CLI, the local web UI,
a Slack bot, and an MCP server for coding agents all share one engine.

## Sealing a finding

A knowledge-risk finding sometimes has to travel: into a reorganization plan, a due diligence,
or the case for a new hire. WhoToAsk seals one into a directory that anyone can check offline
without trusting the machine that made it:

    whotoask attest --out finding
    whotoask verify finding

`attest` writes the finding to `risk.json`, then `manifest.json` listing every file by size and
SHA-256, then `manifest.sig`, an Ed25519 signature over the exact bytes of the manifest.
`verify` checks the signature, then every file, and names each file that was altered or is
missing. A file in the directory that the seal does not cover is reported as unlisted and fails
the check. A failed check exits non-zero. `whotoask attest key` prints the public key to share
through some other channel, and `whotoask verify --key` refuses a seal made by any other key.

`whotoask assess` seals its whole deliverable the same way, over the bytes it already wrote, so a
reader checks their own copy with `whotoask verify .` from inside the directory.

## Data governance

Work data is sensitive, so WhoToAsk decides what a model may see. The default policy is strict:
answers are computed locally and nothing goes to any model beyond this machine. The redacted
policy admits only the known cloud providers and sends your question plus numbered candidates
described by title, team, and matched terms, never names, emails, channel names, or message
text. The open policy sends full candidate detail wherever you point it.

Indexing talks only to the sources you name, with your own tokens. The index on disk is readable
only by your user and can be encrypted at rest with `whotoask vault`. Serving the web UI beyond
localhost requires a bearer token on every request. An organization can pin the policy with a
locked file that flags and environment variables cannot override. `whotoask forget` purges a
person from everything WhoToAsk stores on the machine, except the words of a question or comment
on a vote cast for somebody else. [docs/PRIVACY.md](docs/PRIVACY.md) lists what is kept, where,
and for how long.

## Docs

- [Getting started](docs/GETTING_STARTED.md): install, index each source, ask, serve, and run
  the bot.
- [Connect your tools](docs/CONNECT.md): the `whotoask connect` wizard and a recipe per source.
- [Reference](docs/REFERENCE.md): every command, flag, source, and environment variable.
- [Privacy](docs/PRIVACY.md): what WhoToAsk stores and what can leave the machine.
- [Accuracy](docs/ACCURACY.md): how the findings were measured against real repositories.
- [Architecture](docs/ARCHITECTURE.md), [deploying](docs/DEPLOY.md),
  [roadmap](docs/ROADMAP.md), and [contributing](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE).
