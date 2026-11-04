# Accuracy: how the named people are checked

Peepwho names individuals. An assessment that names the wrong person is worse
than no assessment, so this page says how the naming is measured, on what, and
what the numbers were. Everything here is reproducible from a public
repository with the commands given.

## What this is not for

The findings measured here describe where knowledge and systems are at risk.
They never rank, score, or label people, and they must not be used for
performance reviews, layoffs, or any other employment decision. Naming the
person a body of work rests on is how the risk is located, and the remedy it
points at is a second person who knows.

## Reproducibility

Every figure on this page comes from a command anyone with the repository can
run, and running it twice gives the same bytes. That is load-bearing rather
than tidy: an assessment is sealed, and two people who run the same one should
be able to compare digests and find them equal.

It was not always true. Three separate accumulations summed floating-point
weights while ranging Go maps, whose iteration order is randomized per process,
and float addition is not associative. Subject shares, career totals, and the
discounted work a directory ranking is ordered by all differed in their last
bits between runs. Nothing visible moved on most runs, and on kubernetes it
moved a scored directory, which is the same size as the effect measurement five
was trying to detect. The sums are taken in identifier order now and the
guards mutation-fail without it.

## The problem with measuring yourself

Two earlier ways of judging peepwho were not good enough to publish.

The simulation gauntlet scores against a synthetic company whose answers
peepwho's own generator planted. It is useful for catching regressions, which is
why it runs in CI, and it is worthless as evidence to somebody outside: the
answer key and the thing being tested come from the same code.

Tuning on the target is worse. A threshold chosen until it agreed with a
repository, and then reported as agreement with that repository, is a
measurement of nothing.

So the checks below use targets peepwho was never tuned against and judges
written independently of it: git itself for the sanity check, and the
ownership files project maintainers wrote for their own governance for the
real one. There is no single public ground truth for organizational
expertise, so separate observable properties are measured separately rather
than pretending one benchmark proves the whole system.

## The method

Pick a public repository. Run an assessment. For each of the heaviest subjects,
ask peepwho who holds it, then ask `git log` who touched that directory most,
and see whether peepwho's name is among git's top three.

The two measures are deliberately different. Peepwho counts a subject once per
commit, saturates repetition, decays by recency, and joins one human's several
identities. Git's file-touch count does none of that. They should broadly
agree and should not agree exactly: perfect agreement would mean peepwho is a
slower `git shortlog`.

Run it yourself:

    git clone https://github.com/prometheus/prometheus.git
    peepwho assess --repo-path ./prometheus --codeowners ./prometheus \
      --out prometheus-assessment

    git -C prometheus log --since="2 years ago" --no-merges \
      --format='@@%aN' --name-only

The second command is the judge. Group its output by author, count directory
segments, and compare the top three per directory against the `experts` field
in `findings.json`.

## Measurement one: agreement with git, on prometheus/prometheus

Measured 2026-09-01 against the tip of main, a two-year window, 2,759 commits,
428 people, 190 subjects scored.

The named person appeared in git's own top three for 17 of 19 subjects heavy
enough to judge. Read this as a sanity check rather than a validation: the
judge is commit frequency, which shares its biases with the thing being
judged. The measurement below is the one that does not.

One correction was applied to git's side, and it matters. Prometheus has no
`.mailmap`, so its history holds one maintainer under two spellings of a given
name and another under a full and a shortened given name, as four people where
there are two. Without folding those, git's own ranking disagrees with itself,
and peepwho scores 3 of 19 rather than 17. The identity join is not a
detail on top of the ranking; on a repository without a mailmap it is most of
what makes the ranking mean anything.

## Measurement two: recovering human-written ownership

The stronger test uses ground truth humans wrote for their own reasons: the
per-directory OWNERS files projects in the Kubernetes ecosystem maintain for
real governance, aliases expanded to individuals. The harness ships in the
repository as `peepwho eval owners`; the index is built from git history alone,
with the CODEOWNERS connector off, so declared ownership never leaks into the
input. The naive baseline is git's top three committers per directory, and
the directories where that baseline misses are scored as their own cohort,
because a tool that only finds the maintainers who are also the top
committers is a slower `git shortlog`.

Measured 2026-09-02, two-year windows, place-scoped ownership model:

| Index                       | Repository            | peepwho top 3 | git baseline | Cohort C |
| --------------------------- | --------------------- | ------------ | ------------ | -------- |
| git history only            | kubernetes/test-infra | 56% (44/79)  | 43% (34/79)  | 24% (11/45) |
| git plus placed review data | kubernetes/test-infra | 60% (70/117) | 29% (34/117) | 48% (40/83) |
| git history only            | kubernetes/kubernetes | 37% (14/38)  | 42% (16/38)  | 9% (2/22)   |
| git plus placed review data | kubernetes/kubernetes | 29% (24/83)  | 19% (16/83)  | 21% (14/67) |

Three readings.

**Ownership is asked about places, and scoring it by place is what wins.**
peepwho's subjects pool every path sharing a name; the place model reads the
same walk by directory, with the identity join, bot filtering, trailer
credit, and a breadth discount layered on. From git history alone that beats
commit counting by thirteen points on test-infra.

**Where a review happened matters more than how much review data there is.**
Crediting reviewers with the words of a pull request's title, which is what
a forge connector naturally gives you, adds nothing an ownership question can
use: it doubled the population we could judge and left the ranking flat. The
same reviews credited against the directories the pull request actually
changed take test-infra to sixty percent against a twenty-nine percent
baseline, and take the cohort that commit counting cannot reach from
twenty-eight percent to forty-eight. The link costs no extra request: a merge
commit names its pull request, so the history already knows which change
touched which places.

**Both repositories now beat the baseline, kubernetes included.** Kubernetes
is the harder case, because its approvers approve through review rather than
authorship, and with git history alone peepwho trailed commit counting there.
With review placed properly it leads by ten points. The absolute numbers stay
lower than test-infra's because two thousand pull requests is a few months of
kubernetes velocity; the gap to close now is depth of read, not method.

Read the cohort C column as the real one. Every judged directory where the
designated approver is also a top committer is a directory commit counting
already gets right, and a tool that only wins those is a slower git shortlog.

Commit trailers (Reviewed-by, Acked-by, Co-authored-by) are also credited at
ingest, which moved test-infra's git-only score two points; the mechanism
matters more than the size here, since the Kubernetes ecosystem barely uses
trailers and trailer-heavy ecosystems will see more.

## Measurement three: prediction, and what it told us

A temporal holdout builds the index from history up to a cutoff, asks who each
place rests on knowing only that, and then reads the work that came after.
Nothing past the cutoff reaches the index, so the answer cannot leak into the
prediction. It ships as `peepwho eval holdout`.

It does not favor us, and that changed the product rather than the page.
Measured on prometheus and on kubernetes/test-infra, the naive prediction, the
past window's top committers, forecasts future activity better than peepwho
does. The reason is coherent: the breadth discount that makes peepwho good at
finding designated owners deliberately down-weights the people who touch
everything, and those are exactly the people who go on committing most.
Finding who owns an area and forecasting who will be busy in it are different
questions, and one formula was answering both.

So the wording changed. Concentration findings now say what the record holds,
that an area has one person in it and nobody behind them, which is a checkable
present fact. They no longer say what will happen when that person leaves,
which is a forecast this measurement does not support.

## Measurement four: does concentration predict fragility

The holdout above asks who will be busy, which peepwho does not claim to know.
The claim it does make is narrower: that an area resting on one person is
fragile. That is testable in the same windows. Take every area busy before the
cutoff, note whether peepwho called it single-held, then read the future for
the areas whose leading person stopped working there, and see which ones fell
silent.

Measured 2026-09-13 across five repositories, a three-year window with a
one-year cutoff, quiet meaning activity fell by ninety percent:

| Repository            | flagged single-held, went quiet | not flagged, went quiet |
| --------------------- | ------------------------------- | ----------------------- |
| knative/serving       | 90% (18/20)                     | 24% (7/29)              |
| prometheus/prometheus | 83% (5/6)                       | 5% (2/42)               |
| kubernetes/kubernetes | 50% (73/146)                    | 10% (52/520)            |
| kubernetes/test-infra | 50% (16/32)                     | 32% (34/106)            |
| tektoncd/pipeline     | 40% (24/60)                     | 2% (1/41)               |
| **pooled**            | **52% (136/264)**               | **13% (96/738)**        |

When peepwho says an area rests on one person and that person stops working
there, the area goes quiet about four times as often as an area it did not
flag whose leading person also left. Every repository separates in the same
direction, and none of them now rests on a single flagged area.

These numbers replace an earlier run, and the reason they changed is worth
stating rather than burying. The benchmark was scoring a different predicate
from the one the product ships. It called an area single-held when the raw
head count of everyone who had ever touched the directory was exactly one;
the product flags on the bus factor over breadth-discounted shares, which also
catches an area where one person holds most of the work and two others touched
a file each. Those areas were being scored in the control column. The
benchmark now reads the same `Place.Bus` the exposure view reads, so the
number attaches to the flag a reader is actually shown. It flags far more
areas than before, 264 against 73, and the separation survives at four times
rather than three and a half.

One honest qualification. Part of this is definitional: an area most of whose
work sits with one person has, by construction, little cover already there.
What is not definitional, and is the whole of the risk being described, is
that nobody new arrives either. A single-held area could plausibly attract a
successor and mostly does not; a covered area could plausibly collapse when
its leading person goes and mostly does not.

This is the forward-looking measurement the concentration finding rests on,
and unlike the activity holdout it supports the claim. It ships as part of
`peepwho eval holdout`.

## Measurement five: does crediting the answerer help

A thread has someone asking and someone answering, and the answerer
demonstrably knew. That is a better signal than activity, so the obvious step
was to stop crediting whoever opened a piece of work and credit only the people
who replied to it. The hypothesis was pre-registered with a ship bar fixed in
advance: pooled cohort C had to improve by at least three points, with no
repository regressing on overall by more than two.

Measured 2026-09-15 on kubernetes/test-infra and kubernetes/kubernetes, the two
repositories in the corpus that carry enough OWNERS files to judge. Pooled over
198 judged directories, 143 in cohort C:

| Arm                                  | peepwho top 3     | Cohort C       |
| ------------------------------------ | ---------------- | -------------- |
| credit everyone who took part        | 78/198 (39%)     | 28/143 (20%)   |
| credit only the people who answered&nbsp; | 77/198 (39%) | 27/143 (19%)   |

It does not help. The bar was plus three points on cohort C; the result was
minus one. Paired directory by directory, the treatment won one and lost three.
The scoring is off by default and stays off.

The premise held and the conclusion did not. The people these projects name as
owners really do appear overwhelmingly as answerers rather than askers, 1,276
appearances against 310 on test-infra. But refusing to credit the asker removed
a fifth of all placed credit while removing almost no noise, because the
approvers who open changes are opening them in their own area. A pull request
is not a question. Whoever opens one has already done the work, which is the
opposite of a thread where somebody asks and somebody else knows.

That is the scope limit, and it is a real one. This corpus contains no
question-shaped conversations at all: no Slack threads, no tickets, no pages.
The result says the ask-and-answer asymmetry does not transfer to a code forge.
It says nothing yet about the places the asymmetry actually lives, and testing
there needs ground truth the OWNERS benchmark cannot supply.

A second measurement crediting the answerers with the subject words of the work
they replied to changed zero directories on one repository and one on the
other, inside that repository's own run-to-run wobble, while folding in more
than forty thousand subject increments. That re-confirms measurement two from a
new direction: the problem was never who was credited, it was that titles are
words and ownership is asked about places.

## The assessment itself

No sample assessment is hosted. Regenerate the one this page measured from the
same prometheus checkout, then check its seal:

    peepwho assess --repo-path ./prometheus --codeowners ./prometheus \
      --out prometheus-assessment
    cd prometheus-assessment && peepwho verify .

No account, and nothing sent anywhere. The output is the brief, the JSON
behind every number in it, and the seal over all of them. If you pass an
assessment to someone else, send it as an archive: a seal binds each file to
the name it was written under, and a static host or a mail gateway that renames
a file makes it verify as missing.

## What this does and does not show

It shows that on a large repository nobody tuned against, peepwho's heaviest
subjects name people git agrees are doing that work.

It does not show the ranking is right where git is a poor judge, which is most
of what the product claims. Commit counts say nothing about the person who
reviews every change, answers every question in Slack, and commits rarely.
Peepwho reads those sources precisely because the commit log does not hold
them, and this check cannot reach that.

It also does not measure concentration findings, only who is named. On
prometheus the concentration result was that no significant subject is
critically concentrated: the core systems carry bus factors above twenty. A
tool that reports a healthy project as healthy is the same tool that can be
believed when it reports otherwise.

## Rerunning this

The numbers above will drift as the repository moves. Rerun both commands and
recompute rather than trusting this page: a published accuracy figure that is
never rechecked becomes a claim rather than a measurement.
