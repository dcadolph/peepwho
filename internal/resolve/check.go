package resolve

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// CheckCommand is the one-line command a reader pastes to check a finding
// against the same history it was counted from, with no whotoask involved.
//
// It exists because the reader's staff engineer will say the finding is wrong,
// and the difference between an argument and a settled question is whether the
// row they are pointing at carries the way to check it. The demo has printed
// this command since the beginning, and the assessment must too.
type CheckCommand struct {
	// Dir is the directory the command counts, as the place tally keys it.
	Dir string `json:"dir"`
	// Command is the paste-ready line.
	Command string `json:"command"`
	// Reads is one sentence saying what the output shows and what to compare
	// it against, so the reader is not left interpreting raw counts.
	Reads string `json:"reads"`
}

// PlaceCheck builds the check for one place finding. repoName is the name the
// reader's clone of the repository goes by, never a path from the machine the
// assessment ran on: the command lands in a deliverable, a client's clone
// lives wherever it lives, and the assessor's filesystem is nobody's business.
// sinceDays is the window the assessment read, so the count and the finding
// cover the same commits. localDir is the directory inside the clone, which
// differs from the tally key when a multi-repository read prefixed it.
func PlaceCheck(repoName, localDir string, sinceDays int) CheckCommand {
	cmd := fmt.Sprintf(
		"git -C %s log --since=%q --no-merges --format='%%an <%%ae>' -- %s | sort | uniq -c | sort -rn",
		shellQuote(repoName), fmt.Sprintf("%d days ago", sinceDays), shellQuote(localDir))
	return CheckCommand{
		Dir:     localDir,
		Command: cmd,
		Reads: "Run beside your clone of " + repoName + "; point -C at it if the " +
			"directory is named differently. Counts commits per author under the " +
			"directory, the same history the finding was counted from. One address " +
			"dominating matches a place resting on one person; remember the " +
			"assessment folds one person's several addresses together and this raw " +
			"count does not, so the split identities listed in identity.json appear " +
			"here as separate lines.",
	}
}

// PlaceChecks builds the check for every place, resolving each tally key back
// to the clone it came from. placeLabels maps repository path to the prefix
// its directories carry, empty for a single-repository read.
func PlaceChecks(places []Place, placeLabels map[string]string, sinceDays int) []CheckCommand {
	// Longest label first, so a repository named "svc" never claims the
	// directories of one named "svc-billing".
	type labelled struct{ label, path string }
	byLabel := make([]labelled, 0, len(placeLabels))
	for path, label := range placeLabels {
		byLabel = append(byLabel, labelled{label: label, path: path})
	}
	sort.Slice(byLabel, func(i, j int) bool {
		if len(byLabel[i].label) != len(byLabel[j].label) {
			return len(byLabel[i].label) > len(byLabel[j].label)
		}
		return byLabel[i].label < byLabel[j].label
	})

	out := make([]CheckCommand, 0, len(places))
	for _, pl := range places {
		repoName, localDir := "", pl.Dir
		for _, l := range byLabel {
			if l.label == "" {
				// A single-repository read: the clone's own directory name is
				// the one a reader will recognize.
				repoName = filepath.Base(strings.TrimRight(l.path, "/"))
				break
			}
			if rest, ok := strings.CutPrefix(pl.Dir, l.label+"/"); ok {
				repoName, localDir = l.label, rest
				break
			}
		}
		if repoName == "" || repoName == "." || repoName == string(filepath.Separator) {
			// A place whose prefix matches no known repository cannot be
			// checked, and a wrong command is worse than none. A clone read as
			// "." has no recognizable name either; the reader has the summary
			// naming the repository and can substitute their own.
			continue
		}
		check := PlaceCheck(repoName, localDir, sinceDays)
		check.Dir = pl.Dir
		out = append(out, check)
	}
	return out
}

// shellQuote wraps s in single quotes for a POSIX shell, escaping any single
// quote inside it. Paths land in a command a reader pastes, so a quoting slip
// is not a cosmetic bug.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
