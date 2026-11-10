package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestExposures checks per-topic risk is read back as per-person risk, which is
// the form the question gets asked in. A subject with one expert is theirs
// alone; one they merely lead still has cover behind them.
func TestExposures(t *testing.T) {
	t.Parallel()
	risks := []resolve.TopicRisk{
		{Topic: "billing", Level: "critical", Experts: []resolve.RiskExpert{{ID: "a", Name: "Ada", Share: 1}}},
		{Topic: "kafka", Level: "critical", Experts: []resolve.RiskExpert{{ID: "a", Name: "Ada", Share: 1}}},
		{Topic: "payroll", Level: "elevated", Experts: []resolve.RiskExpert{
			{ID: "b", Name: "Bo", Share: 0.7}, {ID: "a", Name: "Ada", Share: 0.3}}},
		{Topic: "docs", Level: "ok", Experts: []resolve.RiskExpert{
			{ID: "b", Name: "Bo", Share: 0.4}, {ID: "a", Name: "Ada", Share: 0.35}}},
	}
	want := []Exposure{
		{Name: "Ada", ID: "a", Sole: []string{"billing", "kafka"}},
		{Name: "Bo", ID: "b", Leading: []string{"payroll"}},
	}
	if diff := cmp.Diff(want, Exposures(risks), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("exposures mismatch (-want +got):\n%s", diff)
	}
}

// TestCountUsesEveryScoredSubject is the guard on the report's honesty: the
// headline figures are taken before the table is capped, so showing fewer rows
// never quietly shrinks the finding.
func TestCountUsesEveryScoredSubject(t *testing.T) {
	t.Parallel()
	var risks []resolve.TopicRisk
	for i := range 10 {
		risks = append(risks, resolve.TopicRisk{
			Topic: fmt.Sprintf("t%d", i), Level: "critical",
			Experts: []resolve.RiskExpert{{ID: "a", Name: "Ada", Share: 1}},
		})
	}
	got := Count(risks, Exposures(risks))
	want := Counts{Critical: 10, SinglePoint: 10, Exposed: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("counts mismatch (-want +got):\n%s", diff)
	}
}

// TestWriteRiskIsSelfContained checks the report travels. It has to open with
// no network, no whotoask, and no server behind it, so nothing may be fetched at
// open time.
func TestWriteRiskIsSelfContained(t *testing.T) {
	t.Parallel()
	risks := []resolve.TopicRisk{
		{Topic: "billing", Level: "critical", Concentration: 1, BusFactor: 1,
			Experts: []resolve.RiskExpert{{ID: "a", Name: "Ada Lovelace", Share: 1}}},
	}
	var buf bytes.Buffer
	err := WriteRisk(&buf, Brief{
		Generated: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
		People:    12, Scored: 1, Sources: []string{"git", "slack"},
		Risks: risks, Totals: Count(risks, Exposures(risks)), Exposed: Exposures(risks),
	})
	if err != nil {
		t.Fatalf("WriteRisk: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"billing", "Ada Lovelace", "git, slack", "100%", "Knowledge risk brief"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	// A fetch of any kind means the report stops working the moment it is
	// forwarded somewhere without the network it was written on.
	for _, external := range []string{"src=\"http", "href=\"http", "@import", "fetch(", "XMLHttpRequest"} {
		if strings.Contains(out, external) {
			t.Errorf("report reaches outside itself with %q", external)
		}
	}
}

// measuredRamp is a ramp with enough arrivals to report, stalling at 70%: 28 of
// the 40 arrivals never reached an established footing.
var measuredRamp = resolve.Ramp{
	Arrivals: 40, Established: 12, Changes: 5, Places: 9,
	MedianDays: 61, QuickestDays: 20, SlowestDays: 140,
}

// TestWriteRiskSectionsFollowTheirContent is the guard on empty shells. An
// assessment of a Slack export has no repository behind it, so it has no
// systems, no checks and no ramp, and a brief that rendered the headings anyway
// would read as findings that came back empty rather than questions never
// asked.
func TestWriteRiskSectionsFollowTheirContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name    string
		In      Brief
		WantHas []string
		WantNot []string
	}{{ // Test 0: Nothing but scored subjects, so only the standing sections.
		Name:    "no assessment findings",
		In:      Brief{},
		WantNot: []string{`id="identity"`, `id="systems"`, `id="checks"`, `id="ramp"`},
	}, { // Test 1: Split identities render the caveat and its suggested joins.
		Name: "split identities",
		In: Brief{
			Merged: 4,
			Split:  []resolve.SplitName{{Name: "Ada Lovelace", IDs: []string{"git:a", "git:b"}}},
			Aliases: []resolve.AliasSuggestion{{
				Canonical: "git:a", Aliases: []string{"git:b"}, Name: "Ada Lovelace",
				Why: "Same full name, no shared address", Shared: 3,
			}},
		},
		WantHas: []string{
			`id="identity"`, "Ada Lovelace", "git:b", "Same full name, no shared address",
			"4 records were folded", "--aliases",
		},
		WantNot: []string{`id="systems"`, `id="checks"`, `id="ramp"`},
	}, { // Test 2: Places render the systems table and nothing that needs git commands.
		Name: "systems without checks",
		In: Brief{Systems: []resolve.Place{{
			Dir: "internal/billing", Work: 90, People: 7, Bus: 1,
			Holders: []resolve.PlaceHolder{{ID: "a", Name: "Ada Lovelace", Work: 80, Share: 0.89}},
		}}},
		WantHas: []string{`id="systems"`, "internal/billing", "Ada Lovelace", "89%"},
		WantNot: []string{`id="checks"`, `id="ramp"`, `id="identity"`},
	}, { // Test 3: Checks render the command and the sentence that reads it.
		Name: "checks",
		In: Brief{Checks: []resolve.CheckCommand{{
			Dir: "internal/billing", Command: "git -C repo log -- internal/billing",
			Reads: "One address dominating matches a place resting on one person.",
		}}},
		WantHas: []string{
			`id="checks"`, "git -C repo log -- internal/billing",
			"One address dominating matches a place resting on one person.", "data-copy",
		},
		WantNot: []string{`id="systems"`, `id="ramp"`},
	}, { // Test 4: A ramp with too few arrivals says nothing at all.
		Name:    "ramp below the floor",
		In:      Brief{Ramp: resolve.Ramp{Arrivals: 9, Established: 3, Places: 2}},
		WantNot: []string{`id="ramp"`, "not a forecast"},
	}, { // Test 5: A measured ramp renders, and the successor sits with the exposure.
		Name: "measured ramp and succession",
		In: Brief{
			Ramp: measuredRamp,
			Exposed: []Exposure{{
				Name: "Ada Lovelace", ID: "a", Sole: []string{"billing"},
				Succession: &Succession{
					Person: "a", Dir: "internal/billing", Nearest: "Bo Wren",
					Reasons: []string{"holds 4 of the 6 subjects they hold", "reviewed changes there"},
				},
			}},
		},
		WantHas: []string{
			`id="ramp"`, "Bo Wren",
			"holds 4 of the 6 subjects they hold; reviewed changes there",
			"Judged against internal/billing",
		},
		WantNot: []string{`id="systems"`, `id="checks"`},
	}, { // Test 6: Nobody standing out is printed as the finding it is.
		Name: "nobody stands out",
		In: Brief{Exposed: []Exposure{{
			Name: "Ada Lovelace", ID: "a", Sole: []string{"billing"},
			Succession: &Succession{Person: "a", NobodyClose: true},
		}}},
		WantHas: []string{"Nobody stands out in the record, which is itself the finding."},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := WriteRisk(&buf, test.In); err != nil {
				t.Fatalf("WriteRisk: %v", err)
			}
			out := buf.String()
			for _, want := range test.WantHas {
				if !strings.Contains(out, want) {
					t.Errorf("brief is missing %q", want)
				}
			}
			for _, bad := range test.WantNot {
				if strings.Contains(out, bad) {
					t.Errorf("brief renders %q with nothing to put in it", bad)
				}
			}
		})
	}
}

// TestWriteRiskRampCarriesItsFraming is the guard on the one number in the
// brief that turns into a lie without its wording. A median without the stall
// share is survivorship: it describes only the people who got through. A median
// without "history, not a forecast" is a prediction of how long a handover
// takes, which the published holdout says this model cannot make.
func TestWriteRiskRampCarriesItsFraming(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := WriteRisk(&buf, Brief{Ramp: measuredRamp}); err != nil {
		t.Fatalf("WriteRisk: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `id="ramp"`) {
		t.Fatal("the ramp section did not render, so its framing cannot be checked")
	}
	ramp := out[strings.Index(out, `id="ramp"`):]
	if end := strings.Index(ramp, "</section>"); end >= 0 {
		ramp = ramp[:end]
	}
	for _, want := range []string{
		"70%",                     // The stall share, which survivorship cannot flatter.
		"28 of 40",                // The count behind it.
		"had not carried on",      // What stalling means, in words.
		"history, not a forecast", // What the figures are not.
		"Read that figure first",  // The stall share leads, not the median.
	} {
		if !strings.Contains(ramp, want) {
			t.Errorf("the ramp section published its days without %q", want)
		}
	}
	// The stall share has to come before the median, or a reader takes the
	// median for the expected cost of a handover.
	if strings.Index(ramp, "70%") > strings.Index(ramp, "61") {
		t.Error("the median is printed above the stall share")
	}
}

// TestWriteRiskEscapes checks a person or subject named after markup cannot
// break the document, which matters because every name in it came from an
// indexed source rather than from whotoask.
func TestWriteRiskEscapes(t *testing.T) {
	t.Parallel()
	risks := []resolve.TopicRisk{{
		Topic: `<img src=x onerror="alert(1)">`, Level: "critical", BusFactor: 1,
		Experts: []resolve.RiskExpert{{ID: "a", Name: `</td></tr><script>alert(2)</script>`, Share: 1}},
	}}
	var buf bytes.Buffer
	if err := WriteRisk(&buf, Brief{Generated: time.Now(), Risks: risks, Scored: 1}); err != nil {
		t.Fatalf("WriteRisk: %v", err)
	}
	out := buf.String()
	for _, bad := range []string{"<img src=x", "<script>alert(2)</script>", "</td></tr><script"} {
		if strings.Contains(out, bad) {
			t.Errorf("indexed text was rendered as markup: %q survived", bad)
		}
	}
	// Escaped is the point: the text still appears, inert.
	if !strings.Contains(out, "&lt;img src=x") {
		t.Error("the subject name is missing entirely, want it escaped rather than dropped")
	}
}

// hostile is a name of the kind a repository a third party controls can carry:
// markup, both quotes, and a control character.
const hostile = "<script>alert(\"x\")</script>\x00' onmouseover='y"

// TestWriteRiskEscapesAssessmentFindings puts hostile text through every field
// the assessment sections added. The names, directories and reasons all come
// from indexed sources rather than from whotoask, so each is an injection point
// until the escaping is proved over it rather than over the topic table alone.
func TestWriteRiskEscapesAssessmentFindings(t *testing.T) {
	t.Parallel()
	b := Brief{
		Merged: 2,
		Split:  []resolve.SplitName{{Name: hostile, IDs: []string{hostile, "git:b"}}},
		Aliases: []resolve.AliasSuggestion{{
			Canonical: hostile, Aliases: []string{hostile}, Name: hostile, Why: hostile, Shared: 2,
		}},
		Systems: []resolve.Place{{
			Dir: hostile, People: 2, Bus: 1,
			Holders: []resolve.PlaceHolder{{ID: "a", Name: hostile, Share: 0.9}},
		}},
		Checks: []resolve.CheckCommand{{Dir: hostile, Command: hostile, Reads: hostile}},
		Ramp:   measuredRamp,
		Exposed: []Exposure{{
			Name: hostile, ID: "a", Sole: []string{hostile},
			Succession: &Succession{
				Person: "a", Dir: hostile, Nearest: hostile, Reasons: []string{hostile},
			},
		}},
	}
	var buf bytes.Buffer
	if err := WriteRisk(&buf, b); err != nil {
		t.Fatalf("WriteRisk: %v", err)
	}
	out := buf.String()
	// The document has a script of its own, so the markers are the injected
	// payload rather than the tag.
	for _, bad := range []string{"<script>alert", `alert("x")`, "onmouseover='y", "\x00"} {
		if strings.Contains(out, bad) {
			t.Errorf("indexed text reached the document raw: %q survived", bad)
		}
	}
	// Inert, not dropped: a reader still sees the name that was indexed.
	for _, want := range []string{"&lt;script&gt;", "&#34;x&#34;", "�"} {
		if !strings.Contains(out, want) {
			t.Errorf("the escaped form %q is missing, so the text was dropped instead", want)
		}
	}
}

// TestAttachSuccession checks each succession lands on the exposure it answers
// and that one for somebody with no exposure is dropped rather than rendered
// beside the wrong person.
func TestAttachSuccession(t *testing.T) {
	t.Parallel()
	exposed := []Exposure{
		{Name: "Ada", ID: "a", Sole: []string{"billing"}},
		{Name: "Bo", ID: "b", Leading: []string{"payroll"}},
	}
	found := []Succession{
		{Person: "b", Nearest: "Cy", Reasons: []string{"holds 3 of their subjects"}},
		{Person: "z", Nearest: "Nobody's exposure"},
		{Person: "", NobodyClose: true},
	}
	want := []Exposure{
		{Name: "Ada", ID: "a", Sole: []string{"billing"}},
		{Name: "Bo", ID: "b", Leading: []string{"payroll"}, Succession: &Succession{
			Person: "b", Nearest: "Cy", Reasons: []string{"holds 3 of their subjects"},
		}},
	}
	got := AttachSuccession(exposed, found)
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("succession mismatch (-want +got):\n%s", diff)
	}
	if exposed[1].Succession != nil {
		t.Error("the input exposures were modified in place")
	}
}

// TestListedSystems checks the brief cuts to the most exposed places and takes
// each one's command with it. A reader matching a command to the row above it
// goes by position, so a cut that kept forty commands beside fifteen rows would
// put the wrong command under the wrong finding.
func TestListedSystems(t *testing.T) {
	t.Parallel()
	places := []resolve.Place{
		{Dir: "repo/web", Work: 900, People: 30, Bus: 9},
		{Dir: "repo/billing", Work: 200, People: 4, Bus: 1},
		{Dir: "repo/docs", Work: 400, People: 12, Bus: 4},
	}
	// Built from places in the same order, and naming the directory inside the
	// clone rather than the prefixed tally key.
	checks := []resolve.CheckCommand{
		{Dir: "web", Command: "git log -- web"},
		{Dir: "billing", Command: "git log -- billing"},
		{Dir: "docs", Command: "git log -- docs"},
	}
	wantPlaces := []resolve.Place{
		{Dir: "repo/billing", Work: 200, People: 4, Bus: 1},
		{Dir: "repo/docs", Work: 400, People: 12, Bus: 4},
	}
	wantChecks := []resolve.CheckCommand{
		{Dir: "billing", Command: "git log -- billing"},
		{Dir: "docs", Command: "git log -- docs"},
	}
	gotPlaces, gotChecks := ListedSystems(places, checks, 2)
	if diff := cmp.Diff(wantPlaces, gotPlaces, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("places mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantChecks, gotChecks, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("checks mismatch (-want +got):\n%s", diff)
	}
	// No repository read means no places and no commands, not a section with
	// headings and nothing under them.
	none, noChecks := ListedSystems(nil, nil, 15)
	if len(none) != 0 || len(noChecks) != 0 {
		t.Errorf("want nothing listed without places, got %d places and %d checks",
			len(none), len(noChecks))
	}
}

// TestCheckNotes checks the reading instruction is carried once per distinct
// sentence, in first-appearance order. Every check against one clone repeats
// it, and forty copies of the same paragraph is how a reader learns to skip the
// part that stops them misreading the raw counts.
func TestCheckNotes(t *testing.T) {
	t.Parallel()
	checks := []resolve.CheckCommand{
		{Dir: "a", Reads: "Run beside your clone of billing."},
		{Dir: "b", Reads: "Run beside your clone of billing."},
		{Dir: "c", Reads: "Run beside your clone of ledger."},
		{Dir: "d", Reads: ""},
	}
	want := []string{"Run beside your clone of billing.", "Run beside your clone of ledger."}
	if diff := cmp.Diff(want, CheckNotes(checks), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("notes mismatch (-want +got):\n%s", diff)
	}
}
