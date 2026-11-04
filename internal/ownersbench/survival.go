package ownersbench

import (
	"fmt"
	"io"
	"sort"

	"github.com/dcadolph/peepwho/internal/index"
)

// SurvivalConfig bounds a concentration-survival run: peepwho names the places
// resting on one person, knowing only the past, and the future says whether
// those places went quiet when that person stopped.
type SurvivalConfig struct {
	// Repo is the repository root.
	Repo string
	// SinceDays is how far back the past window reaches from today.
	SinceDays int
	// CutoffDays is where the past stops and the future begins.
	CutoffDays int
	// MinPast is the least past activity a place needs to be worth a claim.
	MinPast int
	// QuietShare is the fraction of its former rate below which a place counts
	// as having gone quiet. A tenth means activity fell by ninety percent.
	QuietShare float64
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// DirWork and WorkTotals are the past window's place tally.
	DirWork    map[string]map[string]float64
	WorkTotals map[string]float64
}

// withDefaults fills unset bounds.
func (c SurvivalConfig) withDefaults() SurvivalConfig {
	if c.SinceDays <= 0 {
		c.SinceDays = 1095
	}
	if c.CutoffDays <= 0 {
		c.CutoffDays = 365
	}
	if c.MinPast <= 0 {
		c.MinPast = 20
	}
	if c.QuietShare <= 0 {
		c.QuietShare = 0.1
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
	return c
}

// SurvivalDir is one place peepwho made a concentration claim about.
type SurvivalDir struct {
	// Dir is the directory relative to the repository root.
	Dir string `json:"dir"`
	// Holder is the person peepwho said the place rested on.
	Holder string `json:"holder"`
	// Concentrated reports whether peepwho called the place single-held.
	Concentrated bool `json:"concentrated"`
	// HolderStayed reports whether that person worked there after the cutoff.
	HolderStayed bool `json:"holderStayed"`
	// PastRate and FutureRate are file touches per day in each window, so
	// windows of different lengths compare.
	PastRate   float64 `json:"pastRate"`
	FutureRate float64 `json:"futureRate"`
	// WentQuiet reports whether the place fell below the quiet threshold.
	WentQuiet bool `json:"wentQuiet"`
}

// SurvivalResult is a full run.
type SurvivalResult struct {
	// Dirs are the judged places.
	Dirs []SurvivalDir `json:"dirs"`
}

// Rates returns how often a place went quiet after its holder stopped, split
// by whether peepwho had called it single-held. The claim under test is that
// concentration predicts fragility: a place peepwho flagged should go quiet
// more often, when its holder leaves, than one it did not flag.
func (r *SurvivalResult) Rates() (flaggedQuiet, flagged, otherQuiet, other int) {
	for _, d := range r.Dirs {
		if !d.HolderStayed {
			if d.Concentrated {
				flagged++
				if d.WentQuiet {
					flaggedQuiet++
				}
			} else {
				other++
				if d.WentQuiet {
					otherQuiet++
				}
			}
		}
	}
	return flaggedQuiet, flagged, otherQuiet, other
}

// RunSurvival tests the claim the product actually makes. For every place
// busy enough before the cutoff, it records who peepwho said it rested on and
// whether peepwho called it single-held, then reads the future: did that
// person keep working there, and did the place go quiet.
//
// The comparison that matters is among places whose holder stopped. If
// concentration means anything, those peepwho flagged should fall silent more
// often than those it did not.
func RunSurvival(ix *index.Index, cfg SurvivalConfig) (*SurvivalResult, error) {
	cfg = cfg.withDefaults()
	if cfg.CutoffDays >= cfg.SinceDays {
		return nil, fmt.Errorf("ownersbench: cutoff %d must be inside the window %d",
			cfg.CutoffDays, cfg.SinceDays)
	}
	past, err := dirActivityRange(cfg.Repo, cfg.SinceDays, cfg.CutoffDays)
	if err != nil {
		return nil, err
	}
	future, err := dirActivityRange(cfg.Repo, cfg.CutoffDays, 0)
	if err != nil {
		return nil, err
	}
	pastDays := float64(cfg.SinceDays - cfg.CutoffDays)
	futureDays := float64(cfg.CutoffDays)

	res := &SurvivalResult{}
	for dir, p := range past {
		if p.total < cfg.MinPast {
			continue
		}
		place, ok := dirPlace(ix, Config{
			DirWork: cfg.DirWork, WorkTotals: cfg.WorkTotals, TopK: 3,
		}, dir)
		if !ok || len(place.Holders) == 0 {
			continue
		}
		// Single-held is read off the place the product computed, by the same
		// bus factor the product flags on. It was once a raw head count of
		// everyone who ever touched the directory, which is a different claim:
		// a place where one person holds eighty-five percent and two others
		// touched a file each is flagged by the product and landed in this
		// benchmark's control column, so the published rates were measured on
		// a predicate the product does not ship.
		f := future[dir]
		var futureRate float64
		if f.byAuthor != nil {
			futureRate = float64(f.total) / futureDays
		}
		stayed := false
		if f.byAuthor != nil {
			stayed = f.humans()[nameKey(place.Holders[0].Name)]
		}
		pastRate := float64(p.total) / pastDays
		res.Dirs = append(res.Dirs, SurvivalDir{
			Dir: dir, Holder: place.Holders[0].Name,
			Concentrated: place.Bus <= 1,
			HolderStayed: stayed,
			PastRate:     pastRate, FutureRate: futureRate,
			WentQuiet: futureRate < pastRate*cfg.QuietShare,
		})
	}
	if len(res.Dirs) == 0 {
		return nil, fmt.Errorf("ownersbench: no place is busy enough before the cutoff to claim about")
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return res.Dirs[i].Dir < res.Dirs[j].Dir })
	return res, nil
}

// Report renders a survival run for a terminal.
func (r *SurvivalResult) Report(w io.Writer) {
	fq, f, oq, o := r.Rates()
	fmt.Fprintf(w, "places judged: %d\n", len(r.Dirs))
	fmt.Fprintf(w, "of those whose holder stopped working there:\n")
	if f == 0 && o == 0 {
		fmt.Fprintln(w, "  none, so this run says nothing")
		return
	}
	pct := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	fmt.Fprintf(w, "  peepwho called single-held: %d of %d went quiet (%.0f%%)\n", fq, f, pct(fq, f))
	fmt.Fprintf(w, "  peepwho did not:            %d of %d went quiet (%.0f%%)\n", oq, o, pct(oq, o))
	if f == 0 || o == 0 {
		fmt.Fprintln(w, "  one side is empty, so the comparison carries nothing")
		return
	}
	fmt.Fprintf(w, "  difference: %+.0f points\n", pct(fq, f)-pct(oq, o))
}
