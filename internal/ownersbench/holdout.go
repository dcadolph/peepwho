package ownersbench

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/index"
)

// HoldoutConfig bounds a temporal holdout: peepwho is shown only the history
// before a cutoff and judged on who actually did the work after it.
type HoldoutConfig struct {
	// Repo is the repository root.
	Repo string
	// SinceDays is how far back the past window reaches from today.
	SinceDays int
	// CutoffDays is where the past window stops and the future begins,
	// counted back from today. The index sees nothing after it.
	CutoffDays int
	// MinPast is the least activity a directory needs before the cutoff to be
	// worth predicting about.
	MinPast int
	// MinFuture is the least activity a directory needs after the cutoff for
	// there to be an answer to check against.
	MinFuture int
	// TopK is how many names each predictor may offer and how deep the truth
	// list runs.
	TopK int
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// DirWork and WorkTotals are the past window's place tally, from a git
	// connector run with the same cutoff.
	DirWork    map[string]map[string]float64
	WorkTotals map[string]float64
}

// withDefaults fills unset bounds.
func (c HoldoutConfig) withDefaults() HoldoutConfig {
	if c.SinceDays <= 0 {
		c.SinceDays = 1095
	}
	if c.CutoffDays <= 0 {
		c.CutoffDays = 365
	}
	if c.MinPast <= 0 {
		c.MinPast = 20
	}
	if c.MinFuture <= 0 {
		c.MinFuture = 10
	}
	if c.TopK <= 0 {
		c.TopK = 3
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
	return c
}

// HoldoutDir is one directory's prediction and what actually happened.
type HoldoutDir struct {
	// Dir is the directory relative to the repository root.
	Dir string `json:"dir"`
	// Peepwho is who peepwho said the place rested on, knowing only the past.
	Peepwho []string `json:"peepwho"`
	// Baseline is the past window's top committers, the naive prediction.
	Baseline []string `json:"baseline"`
	// Actual is who did the most work there after the cutoff.
	Actual []string `json:"actual"`
	// PeepwhoHit reports whether peepwho named somebody who went on to do the
	// work.
	PeepwhoHit bool `json:"peepwhoHit"`
	// BaselineHit reports the same for the naive prediction.
	BaselineHit bool `json:"baselineHit"`
	// PeepwhoStayed reports whether the first person peepwho named was still
	// working in the place a year later, at all. It is a different question
	// from out-committing everybody, and it is the one the product actually
	// answers: whether the person it would send you to is still here.
	PeepwhoStayed bool `json:"peepwhoStayed"`
	// BaselineStayed reports the same for the naive prediction's first name.
	BaselineStayed bool `json:"baselineStayed"`
}

// HoldoutResult is a full holdout run.
type HoldoutResult struct {
	// Dirs are the judged directories.
	Dirs []HoldoutDir `json:"dirs"`
	// DroppedQuietPast were too quiet before the cutoff to predict about.
	DroppedQuietPast int `json:"droppedQuietPast"`
	// DroppedQuietFuture had no work after the cutoff to check against.
	DroppedQuietFuture int `json:"droppedQuietFuture"`
}

// Score tallies both predictors.
func (r *HoldoutResult) Score() (peepwho, baseline, of int) {
	for _, d := range r.Dirs {
		of++
		if d.PeepwhoHit {
			peepwho++
		}
		if d.BaselineHit {
			baseline++
		}
	}
	return peepwho, baseline, of
}

// Disagreements returns the directories where the two predictors named
// different people, which is where the comparison carries information: where
// they agree, any result says the same thing about both.
func (r *HoldoutResult) Disagreements() []HoldoutDir {
	var out []HoldoutDir
	for _, d := range r.Dirs {
		if d.PeepwhoHit != d.BaselineHit {
			out = append(out, d)
		}
	}
	return out
}

// RunHoldout scores prediction rather than description. The index and the
// place tally must have been built with the same cutoff, seeing nothing after
// it; this reads the future directly from the repository and asks which
// predictor pointed at the people who went on to do the work.
//
// Both predictors read the same past, so the comparison is about what is done
// with the evidence rather than about who had more of it.
func RunHoldout(ix *index.Index, cfg HoldoutConfig) (*HoldoutResult, error) {
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
	fmt.Fprintf(cfg.Log, "ownersbench: %d directories active before the cutoff, %d after\n",
		len(past), len(future))

	res := &HoldoutResult{}
	for dir, p := range past {
		if p.total < cfg.MinPast {
			continue
		}
		f, ok := future[dir]
		if !ok || f.total < cfg.MinFuture {
			res.DroppedQuietFuture++
			continue
		}
		actual := f.top(cfg.TopK)
		actualKeys := make(map[string]bool, len(actual))
		for _, n := range actual {
			actualKeys[nameKey(n)] = true
		}
		peepwho := dirRanked(ix, Config{
			DirWork: cfg.DirWork, WorkTotals: cfg.WorkTotals, TopK: cfg.TopK,
		}, dir)
		baseline := p.top(cfg.TopK)
		present := f.humans()
		stayed := func(names []string) bool {
			return len(names) > 0 && present[nameKey(names[0])]
		}
		res.Dirs = append(res.Dirs, HoldoutDir{
			Dir: dir, Peepwho: peepwho, Baseline: baseline, Actual: actual,
			PeepwhoHit:     anyKeyed(peepwho, actualKeys),
			BaselineHit:    anyKeyed(baseline, actualKeys),
			PeepwhoStayed:  stayed(peepwho),
			BaselineStayed: stayed(baseline),
		})
	}
	for _, p := range past {
		if p.total < cfg.MinPast {
			res.DroppedQuietPast++
		}
	}
	if len(res.Dirs) == 0 {
		return nil, fmt.Errorf(
			"ownersbench: no directory is busy enough on both sides of the cutoff; "+
				"%d too quiet before, %d too quiet after", res.DroppedQuietPast,
			res.DroppedQuietFuture)
	}
	sort.Slice(res.Dirs, func(i, j int) bool { return res.Dirs[i].Dir < res.Dirs[j].Dir })
	return res, nil
}

// HoldoutPlaces is a convenience for callers that already hold a place tally
// and want the prediction for one directory, in the same order the holdout
// scores it.
func HoldoutPlaces(ix *index.Index, cfg HoldoutConfig, dir string) []string {
	return dirRanked(ix, Config{
		DirWork: cfg.DirWork, WorkTotals: cfg.WorkTotals, TopK: cfg.withDefaults().TopK,
	}, dir)
}

// Report renders a holdout for a terminal.
func (r *HoldoutResult) Report(w io.Writer, k int) {
	peepwho, baseline, of := r.Score()
	fmt.Fprintf(w, "judged %d directories busy on both sides of the cutoff\n", of)
	fmt.Fprintf(w, "peepwho predicted a future worker in its top %d: %d/%d (%.0f%%)\n",
		k, peepwho, of, 100*float64(peepwho)/float64(of))
	fmt.Fprintf(w, "past top-%d committers, the naive prediction:  %d/%d (%.0f%%)\n",
		k, baseline, of, 100*float64(baseline)/float64(of))
	ws, bs := 0, 0
	for _, d := range r.Dirs {
		if d.PeepwhoStayed {
			ws++
		}
		if d.BaselineStayed {
			bs++
		}
	}
	fmt.Fprintf(w, "\nthe person each names first was still working there a year on:\n")
	fmt.Fprintf(w, "  peepwho:   %d/%d (%.0f%%)\n", ws, of, 100*float64(ws)/float64(of))
	fmt.Fprintf(w, "  baseline: %d/%d (%.0f%%)\n", bs, of, 100*float64(bs)/float64(of))

	dis := r.Disagreements()
	if len(dis) == 0 {
		fmt.Fprintln(w, "the two predictors never disagreed, so this run says nothing")
		return
	}
	won := 0
	for _, d := range dis {
		if d.PeepwhoHit {
			won++
		}
	}
	fmt.Fprintf(w, "where they disagreed (%d): peepwho right %d, baseline right %d\n",
		len(dis), won, len(dis)-won)
	for i, d := range dis {
		if i == 10 {
			fmt.Fprintf(w, "  and %d more\n", len(dis)-i)
			break
		}
		mark := "baseline"
		if d.PeepwhoHit {
			mark = "peepwho  "
		}
		fmt.Fprintf(w, "  %s %-40s actual=%s\n", mark, trim(d.Dir, 40),
			strings.Join(d.Actual, ", "))
	}
}

// trim shortens a string for a column.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
