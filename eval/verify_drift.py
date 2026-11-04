#!/usr/bin/env python3
"""Check ownership-drift findings against the history, not against peepwho.

peepwho eval scores ranking against declared ownership, but it reads the same
index the finding came from, so it cannot say whether a finding is TRUE. This
reads raw git and nothing else, and asks one question of each reported area: is
the person peepwho names also the one who has worked in that area most?

Two exclusions, both learned the hard way. Bots are not people. And a commit
touching a large part of the tree is a codemod rather than ownership: counting
those made every finding look wrong, because the sweep authors appear
everywhere.

Usage:  verify_drift.py <repo> <ownership-json>  [--window-days N]
"""
import collections, json, os, subprocess, sys

# A commit touching more than this many distinct directories is a sweep, not
# work on an area. Measured on home-assistant/core, the rival authors of every
# checked finding were commits of 900 to 1271 components.
MAX_BREADTH = 18


def commits(repo, window):
    """Yield (name, email, [paths]) for each non-merge commit in the window."""
    out = subprocess.run(
        ["git", "-C", repo, "log", f"--since={window} days ago", "--no-merges",
         "--format=%H%x00%an%x00%ae", "--name-only"],
        capture_output=True, text=True).stdout
    name, email, files = None, None, []
    for line in out.split("\n"):
        if "\x00" in line:
            if name is not None:
                yield name, email, files
            _, name, email = line.split("\x00")
            files = []
        elif line.strip():
            files.append(line)
    if name is not None:
        yield name, email, files


def is_bot(name, email):
    """Automation is not a person. The [bot] suffix is GitHub's convention and
    not everyone's: PyTorch re-authors every landed change as "PyTorch
    MergeBot", which made a robot the top committer of most of the repository.
    "bot" standing alone as a word, hyphens included, marks automation while
    Talbot and Abbott stay people."""
    if "[bot]" in name or "[bot]" in email:
        return True
    for field in (name, email.partition("@")[0]):
        low = field.lower()
        for sep in "-_.+ ":
            low = low.replace(sep, " ")
        for w in low.split():
            if w == "bot" or w in ("mergebot", "dependabot", "renovatebot"):
                return True
    return False

def person_key(email):
    """One key per human, from their commit email.

    Keying by display name split one person into "Paulus Schoutsen" and
    "Balloob Bot" and then scored peepwho wrong for joining them. GitHub's
    private address keeps its login and drops the account number, matching how
    peepwho normalizes the same address.
    """
    e = email.strip().lower()
    local, _, domain = e.partition("@")
    if domain == "users.noreply.github.com" and "+" in local:
        local = local.split("+", 1)[1]
    return f"{local}@{domain}"


def finding_keys(x):
    """Every identity a finding names its actual owner by."""
    keys = set()
    for raw in x.get("actualIds") or [x.get("actualId") or ""]:
        raw = raw.strip().lower()
        if not raw:
            continue
        if "@" in raw:
            keys.add(person_key(raw))
        elif ":" in raw:
            handle = raw.split(":", 1)[1]
            keys.add(f"{handle}@users.noreply.github.com")
        else:
            keys.add(raw)
    return keys


def directories(repo):
    """Map a normalized directory name to every path with that basename.

    A subject is usually a directory somewhere. Which directory differs by
    project, so this finds them rather than assuming a layout.
    """
    found = collections.defaultdict(set)
    for root, dirs, _ in os.walk(repo):
        dirs[:] = [d for d in dirs if d != ".git"]
        for d in dirs:
            rel = os.path.relpath(os.path.join(root, d), repo)
            found[d.lower().replace("_", "-")].add(rel)
    return found


def main():
    repo, findings = sys.argv[1], sys.argv[2]
    window = 365
    if "--window-days" in sys.argv:
        window = int(sys.argv[sys.argv.index("--window-days") + 1])
    else:
        print("verify_drift: using the default 365-day window; if the index was"
              " built with --max-commits, match --window-days to its reach or"
              " the score is meaningless", file=sys.stderr)

    drift = [x for x in (json.load(open(findings)).get("drift") or [])
             if "leads less" in (x.get("why") or "")]
    dirs = directories(repo)

    # Every focused commit and the directories it changed, keyed by person
    # rather than by the name they happened to sign with.
    history = []
    label = {}
    for name, email, files in commits(repo, window):
        if is_bot(name, email):
            continue
        key = person_key(email)
        label.setdefault(key, name)
        touched = {os.path.dirname(p) for p in files if "/" in p}
        if not touched or len(touched) > MAX_BREADTH:
            continue
        history.append((key, touched))

    right = wrong = skipped = 0
    misses = []
    for x in drift:
        key = x["topic"].lower().replace("_", "-")
        paths = dirs.get(key)
        if not paths:
            skipped += 1
            continue
        # One commit is one change to the area, however many of the area's
        # directories it touched. Summing per directory counted a change that
        # landed code, tests, and a subdirectory as three, which quietly turned
        # "most changes" into "most directories per change" and scored the
        # product wrong against a unit nobody would defend out loud.
        tally = collections.Counter()
        for key, touched in history:
            if any(d == p or d.startswith(p + "/") for d in touched for p in paths):
                tally[key] += 1
        if not tally:
            skipped += 1
            continue
        # A tie for the top is not a contradiction. If git says two people have
        # done the most work and peepwho named one of them, the history has not
        # disagreed with the finding. Comparing against most_common(1) was also
        # non-deterministic: Counter breaks ties by insertion order, insertion
        # order flows from set iteration, and set order is salted per process,
        # so the same findings file scored 31/40 or 30/40 depending on the run.
        mx = max(tally.values())
        keys = finding_keys(x)
        # The finding names one person across every identity they are known by,
        # so their work is the SUM over those keys. Taking the max re-split the
        # person the product had just correctly joined, and scored the join as
        # a mistake.
        mine = sum(tally.get(k, 0) for k in keys)
        if mine == 0 and not keys:
            mine = tally.get(x["actual"], 0)
        # At or above the best single identity in the field: a person summed
        # across their mailboxes can exceed mx, and equality scored them wrong
        # for being too far ahead.
        if mine >= mx:
            right += 1
        else:
            top = min(a for a, n in tally.items() if n == mx)
            wrong += 1
            misses.append((x["topic"], x["actual"], mine, label.get(top, top), mx))

    total = right + wrong
    pct = (100 * right // total) if total else 0
    print(f"  correct {right} / {total} = {pct}%   (skipped {skipped}: no matching directory)")
    for topic, who, mine, top, n in misses[:8]:
        print(f"    MISS {topic:24} peepwho={who} ({mine})  git-top={top} ({n})")


if __name__ == "__main__":
    main()
