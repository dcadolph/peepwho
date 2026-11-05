# Apps

The Mac and iPhone apps call the Go core in `core/` through gomobile, bound as the
`PeepwhoCore` framework. The core takes every secret from the host: the index key is an
argument to `CoreOpen`, and source tokens come through a `CoreSecrets` the host sets on the
handle. `make core-deps` fails if the core ever links the command line or a keychain.

## Layout

`mac/` is one Swift package. `PeepwhoKit` is the library both apps share: the Keychain,
security-scoped bookmarks, the `CoreSecrets` that reads the Keychain, and `Core`, which
runs every core call off the main thread and decodes its JSON into Swift types. `Host` is
the test host below. `Peepwho` is the menu bar app. The `PeepwhoKitTests` decode one saved
answer per core call from `core/testdata/swift`, which the Go test `TestSwiftFixtures`
writes and checks, so a field renamed in Go fails in Go first and then in Swift:

```sh
go test ./core -run TestSwiftFixtures -update-swift
make -C mac test
```

The two apps share nothing at run time. Each keeps its Keychain items under its own bundle
id and its index in its own container, and neither can read the command line's
`~/.peepwho`.

## The test host

`mac/Sources/Host` is a small sandboxed SwiftUI app that proves, against the real framework, the
things a sandboxed app needs before it can ship:

- A folder or file the user picked survives a relaunch as a security-scoped bookmark, and
  the core can index `git` and `org-csv` through it.
- A token the app saved in its Keychain is read back after a relaunch and reaches a core
  refresh through `CoreSecrets`.
- `CoreWipe` empties the data directory in the app's container.

It runs with four entitlements: `app-sandbox`, `files.user-selected.read-only`,
`files.bookmarks.app-scope`, and `network.client`. The data directory is
`~/Library/Containers/com.dcadolph.peepwho.host/Data/Library/Application Support/peepwho`,
separate from the command line's `~/.peepwho`.

## Build

```sh
make mac
```

This binds the core into `mac/Frameworks/` and builds `mac/.build/Host.app` and
`mac/.build/Peepwho.app`, signed with the personal Developer ID so Keychain items keep their
access across rebuilds. `make host` is the same target. Pass `SIGN_ID=-` to `make -C mac host
app` for an ad hoc build; Keychain checks need the Developer ID build.

## Run the checks

The host logs every step to the unified log under the subsystem
`com.dcadolph.peepwho.host`. Tokens and keys are never logged, and person names stay in the
window.

```sh
log show --last 2m --style compact --predicate 'subsystem == "com.dcadolph.peepwho.host"'
```

Grant a repository folder and a CSV once, through the window's buttons or by opening them
with the app, which grants the same access:

```sh
open -g -a mac/.build/Host.app ~/src/some-repo examples/people.csv
```

Every launch then resolves both bookmarks, indexes them, asks a fixed question, and
refreshes. Scripted steps run after that, in the order given:

| Argument                                            | Step                                              |
|-----------------------------------------------------|---------------------------------------------------|
| `--save-jira URL EMAIL TOKEN PROJECT server\|cloud` | Save the Jira site, email, and token, index once. |
| `--refresh`                                         | Refresh every recorded scope.                     |
| `--forget-token`                                    | Delete the Jira token from the Keychain.          |
| `--wipe`                                            | Wipe the data directory through the core.         |
| `--quit`                                            | Quit.                                             |

For the token check, `go run ./mac/Host/fakejira -addr 127.0.0.1:18080` serves a fake Jira
Server site that accepts only the bearer token `good`. Then:

```sh
open -g -a mac/.build/Host.app --args \
  --save-jira http://127.0.0.1:18080 me@example.invalid good KAFKA server --quit
open -g -a mac/.build/Host.app --args --quit
open -g -a mac/.build/Host.app --args --forget-token --refresh --quit
open -g -a mac/.build/Host.app --args --wipe --quit
```

The second launch refreshes Jira with the token read back from the Keychain. The third
refreshes Jira without it and fails with Jira's 401. The fourth logs that the data directory
is absent. A sandbox denial shows up as a kernel line naming the host:

```sh
log show --last 2m --style compact \
  --predicate 'process == "kernel" AND eventMessage CONTAINS "Sandbox: Host("'
```
