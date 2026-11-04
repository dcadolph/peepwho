package resolve

import (
	"sort"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
)

// rampMinArrivals is the fewest observed arrivals worth reporting a middle of.
// Below it the median is one or two people's experience wearing the clothes of
// a statistic, and a client's own history is exactly where that is tempting.
const rampMinArrivals = 12

// Ramp is what a repository's own history says about moving into an unfamiliar
// area: how long it took the people who did it, and how many did not.
//
// It is deliberately not a forecast. Peepwho's published holdout showed this
// model does not predict who will be busy later, and a report that turned
// around and predicted how long a successor would take would be claiming the
// thing the measurement refused. Every field here describes commits that have
// already happened, and the summary wording says so.
type Ramp struct {
	// Arrivals is how many times somebody already working in the repository
	// first touched a directory they had not worked in before.
	Arrivals int `json:"arrivals"`
	// Established is how many of those went on to land Changes changes there.
	Established int `json:"established"`
	// Changes is the number of changes counted as an established footing.
	Changes int `json:"changes"`
	// Places is how many reported systems the arrivals were counted over, so
	// the figures are read against a named denominator rather than a tree.
	Places int `json:"places"`
	// MedianDays is the median days from a newcomer's first change in an area
	// to their Changes'th, over the arrivals that got there.
	MedianDays int `json:"medianDays"`
	// QuickestDays and SlowestDays bound the middle half, so a reader sees the
	// spread rather than a median standing alone.
	QuickestDays int `json:"quickestDays"`
	// SlowestDays is the upper quartile in days.
	SlowestDays int `json:"slowestDays"`
}

// Measured reports whether enough arrivals were seen to say anything.
func (r Ramp) Measured() bool { return r.Established >= rampMinArrivals }

// StalledShare is the fraction of arrivals that never reached an established
// footing within the window, zero to one.
//
// It is the half of this measurement that survives the survivorship problem.
// A median over the people who got established describes only them; this
// counts the ones who started in an area and did not carry on, which is the
// commoner outcome and the one a reader planning a handover should hear.
func (r Ramp) StalledShare() float64 {
	if r.Arrivals == 0 {
		return 0
	}
	return float64(r.Arrivals-r.Established) / float64(r.Arrivals)
}

// MeasureRamp reduces the observed arrivals to what can honestly be said about
// them, over the places the report actually names.
//
// Two exclusions carry the honesty. Arrivals by somebody whose history in the
// repository begins with that directory are dropped: their first commit
// anywhere landed there, so nothing observed says they were new to the area
// rather than new to the record. And only the reported places count, because
// the walk credits a change to every directory above it, so a change deep in
// a tree is an arrival in its directory and in each of its parents. Counted
// over the whole tree the figure is inflated several times over by the same
// move, and a reader has no way to see that from the number.
func MeasureRamp(ramps []connector.Ramp, places []Place) Ramp {
	reported := make(map[string]bool, len(places))
	for _, pl := range places {
		reported[pl.Dir] = true
	}
	out := Ramp{Changes: connector.RampChanges, Places: len(reported)}
	var spans []int
	for _, r := range ramps {
		if !r.Newcomer || !reported[r.Dir] {
			continue
		}
		out.Arrivals++
		if r.Established.IsZero() || !r.Established.After(r.First) {
			continue
		}
		out.Established++
		spans = append(spans, int(r.Established.Sub(r.First)/(24*time.Hour)))
	}
	if len(spans) == 0 {
		return out
	}
	sort.Ints(spans)
	out.MedianDays = spans[len(spans)/2]
	out.QuickestDays = spans[len(spans)/4]
	out.SlowestDays = spans[(3*len(spans))/4]
	return out
}
