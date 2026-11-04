package connector

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// fixedMatters builds a Matters connector over csv with a pinned clock.
func fixedMatters(csv string) *Matters {
	m := &Matters{RecentWindow: 180 * 24 * time.Hour}
	m.now = func() time.Time { return time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) }
	_ = csv
	return m
}

// TestMattersParse covers the connector's contract: entries fold into one
// record per timekeeper, practice areas and matters are stated while
// narratives stay mined, and only entries inside the window count as recent.
func TestMattersParse(t *testing.T) {
	t.Parallel()
	csv := strings.Join([]string{
		"Timekeeper Email,Timekeeper,Matter,Practice Area,Narrative,Date",
		`ana@firm.com,Ana Reyes,Acme v. Bolt,ERISA Litigation,"Draft motion for summary judgment re fiduciary duty",2026-08-10`,
		`ana@firm.com,Ana Reyes,Acme v. Bolt,ERISA Litigation,"Review deposition transcripts",2024-01-05`,
		`bo@firm.com,Bo Chen,Delta Merger,M&A,"Diligence on target IP assignments",2026-08-01`,
		",,,,no timekeeper at all,2026-08-01",
	}, "\n")

	m := fixedMatters(csv)
	recs, err := m.parse(context.Background(), strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 timekeepers", len(recs))
	}

	ana := recs[0]
	if ana.Email != "ana@firm.com" || ana.Name != "Ana Reyes" {
		t.Errorf("ana = %q %q", ana.Name, ana.Email)
	}
	// Test 0: The stated columns are curated topics, with phrases kept.
	for _, want := range []string{"erisa", "litigation", "erisa-litigation", "acme", "bolt"} {
		if !contains(ana.Topics, want) {
			t.Errorf("ana curated topics %v missing %q", ana.Topics, want)
		}
	}
	// Test 1: Narrative words are mined, never stated.
	if !contains(ana.WeakTopics, "fiduciary") {
		t.Errorf("ana weak topics %v missing narrative word", ana.WeakTopics)
	}
	if contains(ana.Topics, "fiduciary") {
		t.Error("a narrative word became a stated topic")
	}
	// Test 2: Only the in-window entry feeds recent work; the 2024 entry does
	// not, even though it names the same subjects.
	recent := strings.Join(ana.RecentTopics, " ")
	if !strings.Contains(recent, "erisa") {
		t.Errorf("recent %v missing the in-window subject", ana.RecentTopics)
	}
	if got := count(ana.RecentTopics, "erisa"); got != 1 {
		t.Errorf("erisa counted %d times in recent, want only the 2026 entry", got)
	}

	// Test 3: The second person is independent.
	bo := recs[1]
	if bo.Email != "bo@firm.com" || !contains(bo.Topics, "delta-merger") {
		t.Errorf("bo = %+v", bo)
	}
}

// TestMattersHeaderErrors pins the two refusals: no identity column, and
// nothing to learn from.
func TestMattersHeaderErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name string
		CSV  string
		Want string
	}{{ // Test 0: No timekeeper column at all.
		Name: "no identity", CSV: "Matter,Date\nAcme,2026-01-01\n", Want: "no timekeeper column",
	}, { // Test 1: An identity but nothing that teaches expertise.
		Name: "nothing to learn", CSV: "Email,Date\nana@firm.com,2026-01-01\n", Want: "nothing to learn",
	}}
	for testNum, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			m := fixedMatters(test.CSV)
			_, err := m.parse(context.Background(), strings.NewReader(test.CSV))
			if err == nil || !strings.Contains(err.Error(), test.Want) {
				t.Errorf("test %d: err = %v, want %q", testNum, err, test.Want)
			}
		})
	}
}

// brokenReader yields its content, then one read error, then EOF. It is the
// shape of a disk or pipe failing mid-file.
type brokenReader struct {
	// data is served on the first read.
	data string
	// state counts reads, so the error is delivered exactly once.
	state int
}

// Read serves the data, then the error, then EOF.
func (b *brokenReader) Read(p []byte) (int, error) {
	b.state++
	switch b.state {
	case 1:
		return copy(p, b.data), nil
	case 2:
		return 0, errors.New("disk gone")
	default:
		return 0, io.EOF
	}
}

// TestMattersReadErrorIsFatal verifies a non-parse read failure stops the run
// with the cause, instead of being skipped like a malformed row. Treating it
// as skippable looped forever printing "line N skipped" on a persistent I/O
// error, and here it would silently truncate the export instead.
func TestMattersReadErrorIsFatal(t *testing.T) {
	t.Parallel()
	m := fixedMatters("")
	r := &brokenReader{data: "Email,Matter\nana@firm.com,Acme\n"}
	recs, err := m.parse(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("err = %v, want the read failure surfaced", err)
	}
	if recs != nil {
		t.Errorf("records = %v, want none from a failed read", recs)
	}
}

// TestMattersMalformedRowStillSkips verifies the parse-error path stays
// skippable: one bad row is named and the rest of the export is read.
func TestMattersMalformedRowStillSkips(t *testing.T) {
	t.Parallel()
	csv := "Email,Matter\n" +
		"ana@firm.com,Acme\n" +
		"\"unterminated,Bolt\n" // A quote that never closes is a parse error.
	m := fixedMatters(csv)
	var log strings.Builder
	m.Log = &log
	recs, err := m.parse(context.Background(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse: a malformed row must not fail the file: %v", err)
	}
	if len(recs) != 1 || recs[0].Email != "ana@firm.com" {
		t.Errorf("records = %+v, want ana kept", recs)
	}
	if !strings.Contains(log.String(), "skipped") {
		t.Errorf("log does not name the skipped row; log:\n%s", log.String())
	}
}

// contains reports whether list holds want.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// count returns how many times want appears in list.
func count(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

// Silence unused-import lint if cmp assertions are trimmed later.
var _ = cmp.Diff
var _ = cmpopts.EquateEmpty
