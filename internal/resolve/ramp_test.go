package resolve

import (
	"fmt"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
)

// arrival builds one observed arrival, established after days when reached.
func arrival(dir, email string, newcomer bool, days int) connector.Ramp {
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := connector.Ramp{Dir: dir, Email: email, First: first, Newcomer: newcomer}
	if days > 0 {
		r.Established = first.AddDate(0, 0, days)
		r.Changes = connector.RampChanges
	}
	return r
}

// manyArrivals builds n arrivals in one place, each established after days, so
// a case can clear the floor the measurement needs before it reports.
func manyArrivals(dir string, n, days int) []connector.Ramp {
	out := make([]connector.Ramp, 0, n)
	for i := range n {
		out = append(out, arrival(dir, fmt.Sprintf("p%d@x.com", i), true, days))
	}
	return out
}

// TestMeasureRampExcludesNewcomersToTheRecord pins the exclusion the whole
// measurement rests on.
//
// Somebody whose history in the repository begins with this directory is not
// observed to be new to the area, only new to the record. The window's edge
// manufactures them by the hundred, and counting them collapses the reported
// time toward nothing.
func TestMeasureRampExcludesNewcomersToTheRecord(t *testing.T) {
	t.Parallel()
	places := []Place{{Dir: "tsdb"}}
	ramps := manyArrivals("tsdb", 20, 100)
	// Twenty more who arrived instantly but were never seen working before.
	for i := range 20 {
		ramps = append(ramps, arrival("tsdb", fmt.Sprintf("edge%d@x.com", i), false, 1))
	}
	got := MeasureRamp(ramps, places)
	if got.Arrivals != 20 {
		t.Errorf("arrivals = %d, want 20: a first commit landing here is not an arrival", got.Arrivals)
	}
	if got.MedianDays != 100 {
		t.Errorf("median = %d days, want 100: the window's edge pulled it down", got.MedianDays)
	}
}

// TestMeasureRampCountsOnlyReportedPlaces pins the second exclusion. The walk
// credits a change to every directory above it, so counting the whole tree
// inflates one move into an arrival per level with nothing in the number
// saying so.
func TestMeasureRampCountsOnlyReportedPlaces(t *testing.T) {
	t.Parallel()
	ramps := manyArrivals("tsdb", 20, 100)
	ramps = append(ramps, manyArrivals("tsdb/chunks", 20, 100)...)
	ramps = append(ramps, manyArrivals("tsdb/chunks/head", 20, 100)...)

	got := MeasureRamp(ramps, []Place{{Dir: "tsdb"}})
	if got.Arrivals != 20 {
		t.Errorf("arrivals = %d, want 20: parents and children counted the same moves", got.Arrivals)
	}
	if got.Places != 1 {
		t.Errorf("places = %d, want 1", got.Places)
	}
}

// TestMeasureRampCountsWhoStalled verifies the figure that survives the
// survivorship problem: arrivals that never reached a footing are counted, and
// the share is reported over all arrivals rather than over the ones who made it.
func TestMeasureRampCountsWhoStalled(t *testing.T) {
	t.Parallel()
	ramps := manyArrivals("tsdb", 15, 60)
	for i := range 45 {
		ramps = append(ramps, arrival("tsdb", fmt.Sprintf("stall%d@x.com", i), true, 0))
	}
	got := MeasureRamp(ramps, []Place{{Dir: "tsdb"}})
	if got.Arrivals != 60 || got.Established != 15 {
		t.Fatalf("arrivals=%d established=%d, want 60 and 15", got.Arrivals, got.Established)
	}
	if share := got.StalledShare(); share < 0.74 || share > 0.76 {
		t.Errorf("stalled share = %.2f, want about 0.75", share)
	}
}

// TestMeasureRampStaysQuietOnTooFew verifies a handful of arrivals reports no
// median at all. A client's own history is exactly where one or two people's
// experience is tempting to dress up as a statistic.
func TestMeasureRampStaysQuietOnTooFew(t *testing.T) {
	t.Parallel()
	got := MeasureRamp(manyArrivals("tsdb", 3, 40), []Place{{Dir: "tsdb"}})
	if got.Measured() {
		t.Errorf("reported a middle over %d arrivals, want silence", got.Established)
	}
}

// TestMeasureRampQuartiles verifies the spread brackets the median rather than
// the median standing alone.
func TestMeasureRampQuartiles(t *testing.T) {
	t.Parallel()
	var ramps []connector.Ramp
	for i := range 40 {
		ramps = append(ramps, arrival("tsdb", fmt.Sprintf("p%d@x.com", i), true, (i+1)*10))
	}
	got := MeasureRamp(ramps, []Place{{Dir: "tsdb"}})
	if !got.Measured() {
		t.Fatal("not measured")
	}
	if got.QuickestDays >= got.MedianDays || got.MedianDays >= got.SlowestDays {
		t.Errorf("quartiles = %d/%d/%d, want them to bracket the median",
			got.QuickestDays, got.MedianDays, got.SlowestDays)
	}
}
