# Contributing

## Build and test

    make build
    make test
    make vet

Format with gofmt before committing. The project follows the Go standard library
style.

## Adding a data source

A source is one connector. Implement the Source interface in internal/connector,
returning records for people or channels, then add a case to the index command in
cmd/index.go. Nothing else needs to change: the index, resolvers, web UI, and bot
work with the new records automatically. See docs/ARCHITECTURE.md for the layers
and docs/ROADMAP.md for sources planned next.

## Tests

Table-driven tests live alongside the code. Cover the happy path, the error
paths, and edge cases. Run the full suite with make test before opening a change.

internal/simorg simulates a whole company and serves each tool's wire format
from in-process HTTP servers, so the full pipeline runs end to end with no
credentials: every source, identity joins, recency, match strength, and feedback.
When you add a source, add it to the simulation and its assertions.

## Measuring a change

Nothing about ranking or identity is obvious enough to change on judgment.
Several plausible improvements to this codebase were measured and turned out to
make things worse, and without a number every one of them would have shipped.

`peepwho eval` scores the current index and compares it against a measurement
saved earlier:

```sh
peepwho index --source git --repo-path ~/src/some-repo
peepwho eval --save before.json
# make the change, rebuild the index
peepwho eval --baseline before.json
```

The score is agreement: how often the owner an organization declared for an area
is also the person peepwho says leads it. It is reported with the reasons it could
be wrong, because the same score can mean three different things. An owner with
no recorded work anywhere is usually an identity that was never joined to their
commits. An owner who works elsewhere but never in the area they own is paper
ownership, and peepwho is probably right to disagree. An owner merely out-worked
in their own area is the arguable case, and the only one that is really about
ranking.

Read **On the same questions** first. Every score here is conditioned on what
was indexed, so a change that alters coverage makes the totals move for reasons
that are not about quality. That section scores only the areas both runs could
answer, and names which ones were won and lost, because a change that wins ten
and loses ten has improved nothing.

The trap is not hypothetical. Widening the history window on a real project
moved the overall Ranked score from 72.1% to 57.1%, which reads as a serious
regression. On the areas both runs could answer it went from 74.6% to 80.5%: the
wider index was being scored on 224 extra questions the narrower one never had
to face, and they were the hard ones.

Two runs are only compared when they cover the same sources. Adding one moves
nearly every number for reasons that have nothing to do with quality.

Any public repository with a CODEOWNERS file works as a test set, so a change
can be measured against real data without pointing peepwho at anything private.

## Checking a finding against the source, not against peepwho

`peepwho eval` scores ranking against declared ownership. It cannot tell you
whether a knowledge-risk finding is *true*, because it reads the same index the
finding came from. For that, check against the history directly.

`eval/verify_drift.py <repo> <ownership.json>` does it for ownership drift: for
every reported area it asks whether the person peepwho names is also the top
focused committer of that area in the same window, reading raw git and nothing
else. Bots are excluded, and so is any commit touching more than eighteen
components, because a codemod is not ownership.

```sh
peepwho index --source git --repo-path ~/src/some-repo
peepwho index --source codeowners --file ~/src/some-repo/CODEOWNERS --merge
peepwho ownership > drift.json
python3 eval/verify_drift.py ~/src/some-repo drift.json
```

Two things this caught that no amount of reading the code would have. A finding
named somebody with no change in the area at all, because a platform file inside
another integration carried its name. And the naive check is wrong in the other
direction too: counting anyone who touched two areas makes every one-person
connection look false, when the extra names are all thousand-file refactors.
Test the claim the product actually makes.
