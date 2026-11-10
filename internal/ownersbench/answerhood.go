// Answer-hood in the owners benchmark: saving one repository's read so every
// scoring arm is measured on the same bytes, and varying who earns credit for
// a pull request.

package ownersbench

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// CreditMode says who earns place credit for a pull request.
type CreditMode string

const (
	// CreditNone credits nobody, leaving the git history's own tally. It is the
	// index-built baseline the benchmark reports first.
	CreditNone CreditMode = "none"
	// CreditAll credits everyone who took part, the author included. It is what
	// the product ships and what the published review-credit numbers were
	// measured with.
	CreditAll CreditMode = "all"
	// CreditAnswered credits only the people who answered, dropping the person
	// who opened the pull request.
	//
	// It was the change under test and it lost. Measured 2026-09-15 it dropped
	// 20% of test-infra's placed credits and 14% of kubernetes', and cost one
	// judged directory out of 198 against CreditAll. The premise held and the
	// conclusion did not: on both repositories the people their OWNERS files
	// name appear overwhelmingly as answerers already, 1276 appearances against
	// 310 on test-infra and 226 against 8 on kubernetes, so refusing to credit
	// the asker removed evidence about the approvers who do open changes in
	// their own area and removed almost no noise. A pull request is not a
	// question. The person who opens one has already done the work.
	CreditAnswered CreditMode = "answered"
)

// Valid reports whether m names a mode.
func (m CreditMode) Valid() bool {
	return m == CreditNone || m == CreditAll || m == CreditAnswered
}

// Probe is one repository's saved forge read, and only the forge read. Arms
// that differ in what was fetched are not arms of one experiment, and the forge
// is the part that cannot be repeated exactly: pull requests move between one
// run and the next.
//
// The git side is deliberately not saved. It is local, deterministic, and
// cheap, so every arm walks it fresh at the depth the benchmark asks for. A
// saved git tally read at the connector's default cap put the credited arms on
// a shallower history than the baseline they were being compared against.
type Probe struct {
	// PullDirs are the directories each pull request changed, learned from the
	// merge commits. Keys are "owner/name#number", because a JSON object cannot
	// be keyed by a struct.
	PullDirs map[string][]string `json:"pullDirs"`
	// PullPeople are the logins of everyone who took part in each pull request,
	// under the same keys.
	PullPeople map[string][]string `json:"pullPeople"`
	// Episodes are the merged changes, each carrying who asked and who
	// answered. They are what CreditAnswered reads.
	Episodes []episode.Episode `json:"episodes"`
}

// SaveProbe writes a probe bundle.
func SaveProbe(path string, p Probe) error {
	blob, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("ownersbench: encode probe: %w", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return fmt.Errorf("ownersbench: write probe: %w", err)
	}
	return nil
}

// LoadProbe reads a probe bundle.
func LoadProbe(path string) (Probe, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Probe{}, fmt.Errorf("ownersbench: read probe: %w", err)
	}
	var p Probe
	if err := json.Unmarshal(raw, &p); err != nil {
		return Probe{}, fmt.Errorf("ownersbench: parse probe: %w", err)
	}
	return p, nil
}

// PullKey renders a pull request reference as a map key a JSON object can hold.
func PullKey(ref connector.PullRef) string {
	return ref.Repo + "#" + strconv.Itoa(ref.Number)
}

// pullRef parses a key PullKey wrote. It reports false on anything else, so a
// hand-edited bundle cannot quietly credit a repository it does not name.
func pullRef(key string) (connector.PullRef, bool) {
	i := strings.LastIndex(key, "#")
	if i < 0 {
		return connector.PullRef{}, false
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil {
		return connector.PullRef{}, false
	}
	return connector.PullRef{Repo: key[:i], Number: n}, true
}

// Keyed renders a reference-keyed map for saving.
func Keyed(m map[connector.PullRef][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for ref, v := range m {
		out[PullKey(ref)] = v
	}
	return out
}

// refKeyed parses a saved map back to reference keys, dropping entries whose
// key does not parse.
func refKeyed(m map[string][]string) map[connector.PullRef][]string {
	out := make(map[connector.PullRef][]string, len(m))
	for key, v := range m {
		if ref, ok := pullRef(key); ok {
			out[ref] = v
		}
	}
	return out
}

// Tally folds this probe's forge credit onto a git tally, working on a copy so
// arms never read each other's credit.
//
// Every mode runs through the same resolve.AddReviewCredit the product ships.
// CreditAnswered differs from CreditAll in one thing and one thing only: the
// asker is not in the list of people handed to it. Reimplementing the credit
// for the treatment arm would have let a second difference in unnoticed, which
// is how an experiment comes to measure its own implementation.
func (p Probe) Tally(
	mode CreditMode, base map[string]map[string]float64, baseTotals map[string]float64,
) (map[string]map[string]float64, map[string]float64, error) {
	if !mode.Valid() {
		return nil, nil, fmt.Errorf("%w: credit mode %q is not none, all, or answered", ErrProbe, mode)
	}
	dirWork := make(map[string]map[string]float64, len(base))
	for dir, people := range base {
		dirWork[dir] = maps.Clone(people)
	}
	totals := maps.Clone(baseTotals)
	if totals == nil {
		totals = make(map[string]float64)
	}
	if mode == CreditNone {
		return dirWork, totals, nil
	}
	people := refKeyed(p.PullPeople)
	if mode == CreditAnswered {
		people = answeredOnly(people, p.Episodes)
	}
	return resolve.AddReviewCredit(dirWork, totals, refKeyed(p.PullDirs), people), totals, nil
}

// answeredOnly returns the participant lists with each pull request's asker
// removed, read from the episode the pull request produced.
//
// The asker is dropped rather than filtered by role at the forge, because the
// episode is where the distinction is already recorded and canonicalized. A
// pull request with no episode keeps every participant: the absence of an
// episode is not evidence that nobody asked, and silently zeroing such a pull
// request would make the treatment arm a smaller read as well as a different
// rule.
func answeredOnly(
	pullPeople map[connector.PullRef][]string, eps []episode.Episode,
) map[connector.PullRef][]string {
	askers := make(map[connector.PullRef]model.ID, len(eps))
	for i := range eps {
		ref, ok := episodeRef(&eps[i])
		if !ok || eps[i].Asked == "" {
			continue
		}
		askers[ref] = eps[i].Asked
	}
	out := make(map[connector.PullRef][]string, len(pullPeople))
	for ref, logins := range pullPeople {
		asked, ok := askers[ref]
		if !ok {
			out[ref] = logins
			continue
		}
		kept := make([]string, 0, len(logins))
		for _, login := range logins {
			if model.ID("github:"+strings.ToLower(login)) == asked {
				continue
			}
			kept = append(kept, login)
		}
		out[ref] = kept
	}
	return out
}

// episodeRef recovers which pull request a change episode came from: its place
// is the repository and its identifier ends in the number.
func episodeRef(ep *episode.Episode) (connector.PullRef, bool) {
	if ep.Kind != episode.KindChange || ep.Place == "" {
		return connector.PullRef{}, false
	}
	i := strings.LastIndex(ep.ID, ":")
	if i < 0 {
		return connector.PullRef{}, false
	}
	n, err := strconv.Atoi(ep.ID[i+1:])
	if err != nil {
		return connector.PullRef{}, false
	}
	return connector.PullRef{Repo: strings.ToLower(ep.Place), Number: n}, true
}
