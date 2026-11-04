# Privacy and encryption

peepwho treats the people graph it builds as sensitive by construction. It runs on
your machine, keeps the index on your disk, can encrypt that index at rest, and
controls exactly what any AI model is allowed to see. This page collects those
guarantees in one place.

## Where your data lives

- The index lives at `~/.peepwho/index.json`, on your machine, created readable only
  by you (mode `0600`). It is never uploaded. Stated plainly, it holds: each person's
  name, email, title, team, and linked identities, their topic weights, an alias
  table joining one person's identifiers across sources, channel names with member
  lists, and an inverted search index of stemmed word terms keyed to people and
  channels. A stemmed term index is not message text, but it still says who used
  which words where, and you should treat the file with the same care as the
  conversations it was built from. Message bodies themselves are not kept.
- Past conversations live beside it at `~/.peepwho/episodes.json`, same permissions,
  written only when you index with `--episodes`. A conversation record holds who
  took part, where, when, a link back to it, and the words it matched on. Who spoke
  with whom is information the index alone does not hold, so this is a real
  addition: index without `--episodes` and none of it is written. `peepwho archive
  prune` deletes what is already there.
- The words of a conversation are kept only when you pass `--archive` (see
  [the archive](#the-archive) below). Without it, peepwho stores a pointer and
  nothing more.
- Credentials are read from the OS keychain, when `peepwho connect` saved them
  there, or from the environment, never from a flag, and nothing is logged. The
  keychain keeps a token out of your shell profile and history; an environment
  variable still wins when it is set.
- Indexing talks only to the sources you name, with your own tokens. With no AI
  model configured, `index`, `ask`, and `serve` make no network requests at all,
  and a test in the suite proves it by refusing every request the process tries
  to make.
- Credentials pasted into messages and tickets are scrubbed at ingest. Keys,
  tokens, passwords, and private key blocks are replaced with `[redacted]`
  before anything reaches the index, the search terms, or retained
  conversation notes.

## The archive

`peepwho index --source slack --archive` keeps the words of each Slack
conversation it records, so `peepwho recall --how` can still show how something
was solved after the messages are gone from Slack. That means the archive holds
your coworkers' messages on your disk: the text of every thread and run of
channel conversation the token could read in the indexed window, with each
message's author and time, up to `--max-archive-messages` (default 50) per
conversation. Credentials in the text are scrubbed to `[redacted]` first, and
nothing else is filtered.

Because it holds readable text, the archive refuses to run without an
encryption key (see below) and is written into `episodes.json` sealed with that
key. It is off unless you ask for it:

- To stop keeping words, index without `--archive`. Nothing new is kept.
- To delete what is already kept, `peepwho archive prune --content-only` drops
  the words and keeps the links, and `peepwho archive prune --older-than-days N`
  forgets old conversations entirely. `peepwho archive status` shows what is
  kept and how far back.
- To forbid it for everyone on a machine, put `"archive": "deny"` in the
  organization policy file. Flags cannot override it.

## Purging a person

`peepwho forget <email or name>` removes one person from everything peepwho
stores: their records under every identity they were known by, the alias
entries joining those identities, their entries in channel member lists, their
retained conversation notes, and any conversation where they were the only
participant. A conversation with other participants keeps its pointer and
loses its searchable words.

Two caveats it prints and that bear repeating: `feedback.json` holds questions
askers typed and is pruned by deleting the file, and re-indexing a source that
still contains the person brings them back, so remove them at the source too.

## Deleting everything

`peepwho wipe` removes every file peepwho writes under the data directory and
every credential it saved in the OS keychain, after you type `wipe` to confirm.
It leaves files it did not write, and it names any credential still set in your
environment and any scheduled refresh, since those are out of its reach.

## Encrypt the index at rest

File permissions alone do not protect the index from a stolen disk, a stray backup,
or another process running as you. Configure a key and peepwho encrypts the index on
every write and decrypts it on every read.

Two ways to supply the key:

    # a base64 32-byte key, best for automation and servers
    export PEEPWHO_INDEX_KEY=$(peepwho vault keygen | sed 's/^export [^=]*=//')

    # or a passphrase, prompted if unset when you run on a terminal
    export PEEPWHO_INDEX_PASSPHRASE='a long passphrase'

With a key set, `peepwho index` writes an encrypted file and every read decrypts it.
The contents are sealed with AES-256-GCM, which authenticates the data so tampering
is detected. A passphrase is stretched into a key with Argon2id and a per-file salt.

Manage it with the `vault` command:

    peepwho vault keygen     # print a fresh export line to save
    peepwho vault status     # is a key configured, is the index encrypted
    peepwho vault encrypt    # encrypt an existing plain index in place
    peepwho vault decrypt    # rewrite it back to plain JSON

Three things to know:

- Reading an encrypted index without the key fails cleanly. Nothing is exposed. On a
  terminal peepwho prompts for the passphrase; in a script it points at the key
  variables and stops.
- The key is the only way back in. Losing it makes an encrypted index unrecoverable,
  so store it as carefully as any other secret and keep a backup.
- The vault covers the index, its per-source sidecar, the conversations,
  `feedback.json` (your queries and votes), and `facts.json`. Only
  `index.state.json` stays plain JSON, by design: it holds per-source re-index
  timestamps and scope names, nothing about people.

## Control what a model sees

Answers are computed locally by default. When you opt into an AI model, an egress
policy governs exactly what leaves the machine. The default is strict, and an
administrator can pin the policy in a file that user flags and environment variables
cannot loosen.

| Mode       | What leaves the machine                                                        |
| ---------- | ------------------------------------------------------------------------------ |
| `strict`   | Nothing. Only the keyword engine and a local model are allowed.                |
| `redacted` | The question and anonymized numbered candidates: title, team, and matched terms. Never names, emails, channel names, or message text. peepwho re-maps the numbers to real people locally. |
| `open`     | Full candidate detail, for teams that accept it and choose their own model.    |

Local models through Ollama need no opt-in, since they run on hardware you control.
Cloud models (Claude, Gemini, OpenAI) run only when you turn them on and only to
their known hosts.

## What findings are for

Knowledge-risk findings describe where knowledge and systems rest on too few
people. They never rank, score, or label people, and they must not be used for
performance reviews or any other employment decision.
