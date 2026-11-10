# Apps

The Mac and iPhone apps call the Go core in `core/` through gomobile, bound as the
`WhoToAskCore` framework. The core takes every secret from the host: the index key is an
argument to `CoreOpen`, and source tokens come through a `CoreSecrets` the host sets on the
handle. `make core-deps` fails if the core ever links the command line or a keychain.

## Layout

`mac/` is one Swift package. `WhoToAskKit` is the library both apps share: the Keychain,
security-scoped bookmarks, the `CoreSecrets` that reads the Keychain, and `Core`, which
runs every core call off the main thread and decodes its JSON into Swift types. `Host` is
the test host below. `WhoToAsk` is the menu bar app. The `WhoToAskKitTests` decode one saved
answer per core call from `core/testdata/swift`, which the Go test `TestSwiftFixtures`
writes and checks, so a field renamed in Go fails in Go first and then in Swift:

```sh
go test ./core -run TestSwiftFixtures -update-swift
make -C mac test
```

The two apps share nothing at run time. Each keeps its Keychain items under its own bundle
id and its index in its own container, and neither can read the command line's
`~/.whotoask`.

## The menu bar app

`mac/Sources/WhoToAsk` is the Mac app, an owl in the menu bar with no Dock icon. The owl is
Socrates. It is a template image drawn from the site's mark, so macOS paints it to suit the menu
bar. Typing in its search field asks who to talk to, and Enter opens a Slack DM with the first
person, or shows them when the index has no Slack link for them. Clicking a person shows what the
index knows about them. Above the results it lists who and what joined or left since the menu last
closed.

Settings holds the tokens, Jira and Confluence sites, and scopes for each source, and the
git folder, org chart CSV, and CODEOWNERS file it reads. Tokens go to the app's Keychain
under the same names the command line reads from the environment. Scopes and picked places
go there too, since they name what a person reads and the preferences file is plain. Index Now reads every
source set up, Refresh Now replays every scope indexed before, and Wipe deletes the index.
While either runs, Settings shows the source it is on and its newest progress line, and Cancel
stops it. A canceled Index saves nothing for the source it was on, and a canceled Refresh keeps
the scopes that finished and records no failed try for the one it stopped.
Tokens and picked folders stay until forgotten one by one.
Forgetting a picked place, or picking another in its slot, stops refreshing the old one, since it
could only fail from then on, and Index Now stops refreshing a scope taken out of Settings. What
they put in the index stays until a wipe. Forgetting a token keeps its scopes, so saving the token
again picks the source back up.

Settings also shows, per source, when it last indexed and why its last try failed, if it did.

With an empty index, Settings offers Load Sample Company: a simulated company of 223 people
across eight sources, so every part of the app can be tried without a token. `make mac` builds
it with `whotoask demo --big --save-index` and bundles only `index.json` and `episodes.json` in
the app as data, so the core links none of the simulator, which `make core-deps` enforces.
Loading seals it under the app's key and writes a `sample.json` marker. Settings and the menu
both say it is sample data. Index and Refresh refuse while it is there, automatic refresh skips
it, and Wipe removes it like any index. A load never replaces real data.

The app refreshes on its own when the index is older than six hours: when the menu opens,
when the Mac wakes, and on a timer while it runs. The menu says which sources failed their last
try until one works again, leaving out a source whose token was forgotten. A failed refresh waits a full interval
before it runs again. The interval is a default in seconds:

```sh
defaults write com.dcadolph.whotoask WhoToAskRefreshIntervalSeconds -int 3600
```

Settings has an Open at login switch, off until a person turns it on, which adds the app to the
system's Login Items. A person who turned the app off in System Settings has to approve it there,
and the switch says so. About WhoToAsk shows the version and copyright.

The index lives in
`~/Library/Containers/com.dcadolph.whotoask/Data/Library/Application Support/whotoask`, sealed
with a key the app makes on first launch and keeps in its Keychain. It is the app's own
index, and the sandbox keeps it from reading the command line's `~/.whotoask`.

## Release the app

The app's version is stamped from the newest `v*` tag when it is built, so cut the release
first and build from its tag: `make -C mac dmg` refuses a tree that is not clean on the tag the
app says it is. `CFBundleVersion` in `mac/Sources/WhoToAsk/Info.plist` is an integer that goes up
with every build shipped. The notarized disk image is attached to that release.
`NOTARY_PROFILE` names a `notarytool store-credentials` profile for the signing team.

The app is universal, one build per architecture joined with `lipo`, with the core's own arm64
and x86_64 slices inside. `make -C mac app` ends by running `verify-bundle.sh`, which fails on a
missing slice, a wrong version or macOS floor, or a missing category or copyright.

```sh
make mac
ditto -c -k --keepParent mac/.build/WhoToAsk.app mac/.build/WhoToAsk.zip
xcrun notarytool submit mac/.build/WhoToAsk.zip --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple mac/.build/WhoToAsk.app
make -C mac dmg
xcrun notarytool submit mac/.build/WhoToAsk.dmg --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple mac/.build/WhoToAsk.dmg
xcrun stapler validate mac/.build/WhoToAsk.app
xcrun stapler validate mac/.build/WhoToAsk.dmg
spctl --assess --type open --context context:primary-signature -v mac/.build/WhoToAsk.dmg
```

`make -C mac dmg` packs the app already built and never rebuilds it, so the stapled app
keeps its ticket inside the image.

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
`~/Library/Containers/com.dcadolph.whotoask.host/Data/Library/Application Support/whotoask`,
separate from the command line's `~/.whotoask`.

## Build

```sh
make mac
```

This binds the core into `mac/Frameworks/` and builds `mac/.build/Host.app` and
`mac/.build/WhoToAsk.app`, signed with the personal Developer ID so Keychain items keep their
access across rebuilds. `make host` is the same target. Pass `SIGN_ID=-` to `make -C mac host
app` for an ad hoc build; Keychain checks need the Developer ID build.

## Run the checks

The host logs every step to the unified log under the subsystem
`com.dcadolph.whotoask.host`. Tokens and keys are never logged, and person names stay in the
window.

```sh
log show --last 2m --style compact --predicate 'subsystem == "com.dcadolph.whotoask.host"'
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
