package render

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/eval"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// update rewrites the golden files from the current output when set.
var update = flag.Bool("update", false, "Rewrite the golden files.")

// fixedTime is the one instant every time-dependent fixture field uses.
var fixedTime = time.Date(2026, time.March, 14, 15, 9, 26, 0, time.UTC)

// goldenCase is one renderer run against a fixed input.
type goldenCase struct {
	// Name is the golden file stem.
	Name string
	// Render writes the output under test with the given style.
	Render func(io.Writer, Style)
}

// goldenCases lists every entry point with a populated and an empty input.
func goldenCases() []goldenCase {
	answer := resolve.JSONAnswer{
		Summary: "Two people and one channel know about billing.",
		People: []resolve.JSONPerson{
			{Name: "Kim Doe", Title: "Staff Engineer", Team: "Payments", Strength: 0.92,
				Reasons: []string{"commits to billing", "answers in #payments"}},
			{Name: "Lee Roe", Team: "Payments", Strength: 0.4, Reasons: []string{"mentioned in docs"}},
			{Name: "Sam Poe", Title: "Manager", Strength: 0, Reasons: nil},
		},
		Channels: []resolve.JSONChannel{
			{Name: "payments", URL: "https://chat.example.com/payments", Strength: 0.7,
				Reasons: []string{"active on billing"}},
			{Name: "billing-alerts", Strength: 0.2},
		},
	}
	directory := resolve.Directory{
		People: []resolve.DirectoryPerson{
			{Name: "Kim Doe", Title: "Staff Engineer", Team: "Payments"},
			{Name: "Lee Roe", Team: "Payments"},
		},
		Channels: []resolve.DirectoryChannel{
			{Name: "payments", Topic: "Payments chat", Members: 12},
			{Name: "ops", Members: 1},
		},
		Teams:  []resolve.DirectoryTeam{{Name: "Payments", People: 2}, {Name: "Sales", People: 1}},
		Topics: []resolve.DirectoryTopic{{Name: "billing", People: 2}, {Name: "kafka", People: 1}},
	}
	status := map[string]any{
		"people": 12, "teams": 3, "subjects": 40, "channels": 5,
		"built_at": "2026-03-14T15:09:26Z", "age": "15m0s",
		"sources":                   map[string]int{"slack": 4, "github": 8},
		"encryption_key_configured": true,
		"embeddings":                false,
		"index":                     "/var/lib/whotoask/index.db",
	}
	recallAnswer := recall.Answer{
		Query: "vacation policy",
		Episodes: []recall.Episode{
			{Source: "slack", Kind: "thread", Place: "hr", When: fixedTime,
				People:    []recall.Person{{Name: "Kim Doe"}, {Email: "lee@example.com"}},
				Matched:   []string{"vacat"},
				Solution:  &recall.Solution{Summary: "Use the HR form.", Notes: []recall.Note{{Author: "Kim", Text: "Form is in the wiki."}}},
				Permalink: "https://chat.example.com/p/1", LinkMayHaveExpired: true},
			{Source: "github", Kind: "pr", Place: "repo", When: fixedTime,
				People: []recall.Person{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}}},
			{Source: "", Kind: "doc"},
		},
		Scope: recall.Scope{Note: "Searched 3 conversations."},
	}
	risk := []resolve.TopicRisk{
		{Topic: "billing", Level: "critical", Concentration: 0.95, BusFactor: 1,
			Includes: []string{"invoicing"},
			Experts:  []resolve.RiskExpert{{Name: "Kim Doe", Share: 0.95, Quiet: true}}},
		{Topic: "kafka", Level: "elevated", Concentration: 0.6, BusFactor: 2,
			Experts: []resolve.RiskExpert{{Name: "Lee Roe", Share: 0.6}}},
		{Topic: "docs", Level: "ok", Concentration: 0, BusFactor: 4},
	}
	departure := resolve.DepartureImpact{
		Person: "github:kim", Name: "Kim Doe", Sole: []string{"billing"}, Top: []string{"kafka"},
		Regions: []resolve.Region{{Lead: "Kim Doe", Topics: []string{"billing", "invoicing"}}},
	}
	related := []resolve.TopicRelation{
		{Topic: "invoicing", Overlap: 0.8, Because: "same experts", Together: 0.5},
		{Topic: "kafka-lag", Overlap: 0.3, Narrower: true, Because: "shared on-call",
			Spanned: 1, SoleName: "Kim Doe"},
		{Topic: "docs", Overlap: 0.1, Because: "one shared person"},
	}
	spans := []resolve.Span{
		{Topics: []string{"billing", "kafka"}, Person: "Kim Doe", Experts: 2},
		{Topics: []string{"a", "b", "c"}, Person: "Lee Roe", Experts: 3},
	}
	regions := []resolve.Region{{Lead: "Kim Doe", Topics: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}}
	identity := IdentityView{Merges: 3, People: []IdentityPerson{
		{ID: "kim", Name: "Kim Doe", Email: "kim@example.com", Joins: []IdentityJoin{
			{Alias: "github:kim-doe", Confidence: 0.9, Reason: "unique name match"},
			{Alias: "slack:U1", Confidence: 0.5, Reason: "name and shared team"}}},
		{ID: "lee", Joins: []IdentityJoin{{Alias: "plain-alias", Confidence: 0, Reason: "guess"}}},
	}}
	unlinked := UnlinkedView{Unlinked: 2, Declared: 5, Owners: []UnlinkedOwner{
		{ID: "kim-doe", Owns: []string{"a", "b", "c", "d", "e", "f", "g"}},
		{ID: "lee-roe", Owns: []string{"x"}},
	}}
	ownership := resolve.OwnershipReport{Declared: 6, GroupOwned: 1, Silent: 1, Unworked: 1, Trailing: 1,
		Drift: []resolve.OwnerDrift{
			{Topic: "billing", Declared: []string{"kim-doe"}, Actual: "Lee Roe"},
			{Topic: "kafka", Declared: []string{"a", "b"}, Actual: "Sam Poe"},
		}}
	evalResult := eval.Result{
		Agreement: 0.5, Declared: 10, Held: 5, Silent: 1, Unworked: 2, Trailing: 2, Answerable: 8,
		Ranked: 0.625, People: 20, Records: 30, Joins: 4, Unlinked: 2, Topics: 50, Salient: 30,
		Ties: 12, Spans: 1, Regions: 1, Sources: []string{"github", "slack"},
	}
	changes := []eval.Change{
		{Name: "agreement", Before: 0.5, After: 0.6, Better: true, Scored: true},
		{Name: "ranked", Before: 0.7, After: 0.6, Scored: true},
		{Name: "people", Before: 10, After: 12},
		{Name: "spans", Before: 3, After: 1},
	}
	head := eval.Head{Areas: 8, BeforeHeld: 4, AfterHeld: 5,
		Won: []string{"a", "b", "c", "d", "e", "f", "g"}, Lost: []string{"z"}}
	headDown := eval.Head{Areas: 8, BeforeHeld: 5, AfterHeld: 4}
	headFlat := eval.Head{Areas: 8, BeforeHeld: 5, AfterHeld: 5}

	return []goldenCase{
		{"ask", func(w io.Writer, s Style) { RenderAsk(w, "billing", answer, s) }},
		{"ask_empty", func(w io.Writer, s Style) { RenderAsk(w, "nothing", resolve.JSONAnswer{}, s) }},
		{"ask_teams", func(w io.Writer, s Style) {
			RenderAsk(w, "tsdb", resolve.JSONAnswer{Teams: []resolve.JSONTeam{
				{ID: "codeowners:acme/legal-squad", Name: "@acme/legal-squad", Team: "Legal", Strength: 1,
					Reasons: []string{"tsdb (topic)"}},
				{ID: "codeowners:acme/default-maintainers", Name: "@acme/default-maintainers", Strength: 0.5},
			}}, s)
		}},
		{"ask_departed", func(w io.Writer, s Style) {
			RenderAsk(w, "ledgers", resolve.JSONAnswer{
				People: []resolve.JSONPerson{{Name: "Nora Here", Title: "Engineer", Team: "Payments", Strength: 0.8,
					Reasons: []string{"ledgers (topic)"}}},
				Departed: []resolve.JSONPerson{{Name: "Lena Gone", Title: "Engineer", Deactivated: true, Strength: 1,
					Reasons: []string{"ledgers (topic)"}}},
			}, s)
		}},
		{"ask_departed_only", func(w io.Writer, s Style) {
			RenderAsk(w, "ledgers", resolve.JSONAnswer{
				Departed: []resolve.JSONPerson{{Name: "Lena Gone", Deactivated: true, Strength: 1}},
			}, s)
		}},
		{"status_departed", func(w io.Writer, s Style) {
			RenderStatus(w, map[string]any{"people": 220, "deactivated": 1, "teams": 13, "age": "0s"}, s)
		}},
		{"near", func(w io.Writer, s Style) {
			RenderNear(w, "Kim Doe", []index.Adjacent{
				{Name: "Lee Roe", Reasons: []string{"team Payments", "topic billing"}},
				{Name: "Sam Poe"},
			}, s)
		}},
		{"near_empty", func(w io.Writer, s Style) { RenderNear(w, "Kim Doe", nil, s) }},
		{"status", func(w io.Writer, s Style) { RenderStatus(w, status, s) }},
		{"status_bare", func(w io.Writer, s Style) {
			RenderStatus(w, map[string]any{"age": "0s", "index": "relative.db"}, s)
		}},
		{"status_unparsed", func(w io.Writer, s Style) {
			RenderStatus(w, map[string]any{"built_at": "yesterday", "age": "2h0m0s"}, s)
		}},
		{"recall", func(w io.Writer, s Style) { RenderRecall(w, recallAnswer, s) }},
		{"recall_empty", func(w io.Writer, s Style) {
			RenderRecall(w, recall.Answer{Scope: recall.Scope{Note: "Nothing indexed."}}, s)
		}},
		{"directory", func(w io.Writer, s Style) { RenderDirectory(w, directory, "", s) }},
		{"directory_teams", func(w io.Writer, s Style) { RenderDirectory(w, directory, "teams", s) }},
		{"directory_empty", func(w io.Writer, s Style) { RenderDirectory(w, resolve.Directory{}, "", s) }},
		{"directory_departed", func(w io.Writer, s Style) {
			RenderDirectory(w, resolve.Directory{People: []resolve.DirectoryPerson{
				{Name: "Dee Parted", Title: "Engineer", Deactivated: true},
				{Name: "Nora Here", Title: "Engineer", Team: "Payments"},
			}}, "people", s)
		}},
		{"feedback", func(w io.Writer, s Style) {
			RenderFeedback(w, []feedback.Entry{
				{Query: "who owns billing", Person: "Kim Doe", Vote: feedback.Helpful, Comment: "spot on"},
				{Query: "kafka", Channel: "ops", Vote: -1},
			}, s)
		}},
		{"feedback_empty", func(w io.Writer, s Style) { RenderFeedback(w, nil, s) }},
		{"identity", func(w io.Writer, s Style) { RenderIdentity(w, identity, s) }},
		{"identity_empty", func(w io.Writer, s Style) { RenderIdentity(w, IdentityView{}, s) }},
		{"search", func(w io.Writer, s Style) {
			RenderSearch(w, "pay", []resolve.SearchResult{
				{Kind: "person", Name: "Kim Doe", Title: "Engineer", Team: "Payments", Matched: []string{"name"}},
				{Kind: "person", Name: "Lee Roe", Email: "lee@example.com"},
				{Kind: "channel", Name: "payments", Team: "Payments"},
				{Kind: "channel", Name: "a-very-long-channel-name-that-keeps-going-and-going"},
			}, s)
		}},
		{"search_deactivated", func(w io.Writer, s Style) {
			RenderSearch(w, "dinesh", []resolve.SearchResult{
				{Kind: "person", Name: "Dinesh Black", Title: "Engineer", Team: "Payments",
					Matched: []string{"name"}},
				{Kind: "person", Name: "Dinesh White", Title: "Engineer", Team: "Payments",
					Matched: []string{"name"}, Deactivated: true},
				{Kind: "person", Name: "Dinesh Adams", Email: "adams@example.com",
					Matched: []string{"name"}, Deactivated: true},
			}, s)
		}},
		{"search_empty", func(w io.Writer, s Style) { RenderSearch(w, "zzz", nil, s) }},
		{"risk", func(w io.Writer, s Style) { RenderRisk(w, risk, s) }},
		{"risk_empty", func(w io.Writer, s Style) { RenderRisk(w, nil, s) }},
		{"departure", func(w io.Writer, s Style) { RenderDeparture(w, "kim", departure, s) }},
		{"departure_none", func(w io.Writer, s Style) { RenderDeparture(w, "zed", resolve.DepartureImpact{}, s) }},
		{"departure_clear", func(w io.Writer, s Style) {
			RenderDeparture(w, "lee", resolve.DepartureImpact{Person: "lee"}, s)
		}},
		{"related", func(w io.Writer, s Style) { RenderRelated(w, "billing", related, s) }},
		{"related_empty", func(w io.Writer, s Style) { RenderRelated(w, "billing", nil, s) }},
		{"spans", func(w io.Writer, s Style) { RenderSpans(w, spans, s) }},
		{"spans_empty", func(w io.Writer, s Style) { RenderSpans(w, nil, s) }},
		{"unlinked", func(w io.Writer, s Style) { RenderUnlinked(w, unlinked, s) }},
		{"unlinked_none_declared", func(w io.Writer, s Style) { RenderUnlinked(w, UnlinkedView{}, s) }},
		{"unlinked_all_worked", func(w io.Writer, s Style) { RenderUnlinked(w, UnlinkedView{Declared: 4}, s) }},
		{"regions", func(w io.Writer, s Style) { RenderRegions(w, regions, s) }},
		{"regions_empty", func(w io.Writer, s Style) { RenderRegions(w, nil, s) }},
		{"ownership", func(w io.Writer, s Style) { RenderOwnership(w, ownership, s) }},
		{"ownership_none_declared", func(w io.Writer, s Style) {
			RenderOwnership(w, resolve.OwnershipReport{}, s)
		}},
		{"ownership_clean", func(w io.Writer, s Style) {
			RenderOwnership(w, resolve.OwnershipReport{Declared: 1}, s)
		}},
		{"eval", func(w io.Writer, s Style) { RenderEval(w, evalResult, s) }},
		{"eval_empty", func(w io.Writer, s Style) { RenderEval(w, eval.Result{Regions: 1, Sources: nil}, s) }},
		{"eval_changes", func(w io.Writer, s Style) { RenderEvalChanges(w, changes, true, s) }},
		{"eval_changes_incomparable", func(w io.Writer, s Style) { RenderEvalChanges(w, nil, false, s) }},
		{"eval_changes_none", func(w io.Writer, s Style) { RenderEvalChanges(w, nil, true, s) }},
		{"eval_head", func(w io.Writer, s Style) { RenderEvalHead(w, head, true, s) }},
		{"eval_head_down", func(w io.Writer, s Style) { RenderEvalHead(w, headDown, true, s) }},
		{"eval_head_flat", func(w io.Writer, s Style) { RenderEvalHead(w, headFlat, true, s) }},
		{"eval_head_missing", func(w io.Writer, s Style) { RenderEvalHead(w, eval.Head{}, false, s) }},
	}
}

// TestGolden checks every renderer against its golden file with color off and on.
func TestGolden(t *testing.T) {
	t.Parallel()
	for testNum, test := range goldenCases() {
		for _, mode := range []struct {
			Suffix string
			Style  Style
		}{{"plain", Style{}}, {"color", Style{On: true}}} {
			// Test N: Renderer output matches its golden file.
			t.Run(fmt.Sprintf("test %d %s %s", testNum, test.Name, mode.Suffix), func(t *testing.T) {
				t.Parallel()
				var buf bytes.Buffer
				test.Render(&buf, mode.Style)
				path := filepath.Join("testdata", test.Name+"."+mode.Suffix+".golden")
				if *update {
					if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
						t.Fatalf("write golden: %v", err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden: %v", err)
				}
				if diff := cmp.Diff(string(want), buf.String()); diff != "" {
					t.Errorf("mismatch (-want +got):\n%s", diff)
				}
			})
		}
	}
}

// TestEscapeControls checks terminal control characters are made visible
// instead of reaching the terminal, and that clean strings pass unchanged.
func TestEscapeControls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: A clean name passes through untouched.
		In: "Jane Roe", Want: "Jane Roe",
	}, { // Test 1: An OSC title-set plus conceal, the shape of a git author
		// name that retitles the window and hides output mid-assessment.
		In:   "Jo\x1b]0;pwned\x07\x1b[8m",
		Want: `Jo\x1b]0;pwned\a\x1b[8m`,
	}, { // Test 2: Newline and carriage return cannot forge extra rows.
		In: "top\nfake row\r", Want: `top\nfake row\r`,
	}, { // Test 3: DEL and a C1 CSI, which some terminals honor as escapes.
		In: "a\x7fbc", Want: `a\x7fbc`,
	}, { // Test 4: Non-ASCII text that is not a control survives as itself.
		In: "Bücher · naïve", Want: "Bücher · naïve",
	}, { // Test 5: A C1 control, the 8-bit CSI, is escaped too.
		In: "x\u009by", Want: `x\u009by`,
	}, { // Test 6: Empty input stays empty.
		In: "", Want: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := EscapeControls(test.In)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			if strings.ContainsFunc(got, isTerminalControl) {
				t.Errorf("escaped output still carries a control character: %q", got)
			}
		})
	}
}

// TestEscapeControlsList checks every element is escaped independently.
func TestEscapeControlsList(t *testing.T) {
	t.Parallel()
	got := EscapeControlsList([]string{"a\x1b", "b"})
	if diff := cmp.Diff([]string{`a\x1b`, "b"}, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// TestPlural checks the suffix helpers agree for the cases callers use.
func TestPlural(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In          int
		WantPlural  string
		WantPlural2 string
	}{{ // Test 0: One is singular.
		In: 1, WantPlural: "", WantPlural2: "body",
	}, { // Test 1: Zero is plural.
		In: 0, WantPlural: "s", WantPlural2: "bodies",
	}, { // Test 2: Many is plural.
		In: 5, WantPlural: "s", WantPlural2: "bodies",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantPlural, Plural(test.In)); diff != "" {
				t.Errorf("Plural mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPlural2, Plural2(test.In, "body", "bodies")); diff != "" {
				t.Errorf("Plural2 mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStyleHelpers checks the small formatting helpers and the style switch.
func TestStyleHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Got  string
		Want string
	}{{ // Test 0: Plain style leaves text alone.
		Got: Style{}.Bold("x"), Want: "x",
	}, { // Test 1: Empty text is never wrapped.
		Got: Style{On: true}.Bold(""), Want: "",
	}, { // Test 2: Color on wraps in ANSI.
		Got: Style{On: true}.Bad("x"), Want: "\033[31mx\033[0m",
	}, { // Test 3: Zero confidence shows a tilde.
		Got: pct(0), Want: "   ~",
	}, { // Test 4: Confidence shows a percentage.
		Got: pct(0.5), Want: " 50%",
	}, { // Test 5: Title alone.
		Got: joinRole("Eng", ""), Want: "Eng",
	}, { // Test 6: Team alone.
		Got: joinRole("", "Pay"), Want: "Pay",
	}, { // Test 7: Both.
		Got: joinRole("Eng", "Pay"), Want: "Eng, Pay",
	}, { // Test 8: Padding measures raw text.
		Got: pad("\033[1mab\033[0m", "ab", 4), Want: "\033[1mab\033[0m  ",
	}, { // Test 9: Confidence bar clamps above one.
		Got: Style{}.ConfBar(2), Want: "▰▰▰▰▰▰▰▰▰▰",
	}, { // Test 10: Confidence bar clamps below zero.
		Got: Style{}.ConfBar(-1), Want: "▱▱▱▱▱▱▱▱▱▱",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, test.Got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSourceColor checks known sources keep their label and code.
func TestSourceColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantLabel string
		WantCode  int
	}{
		{"GitHub", "GitHub", 141}, {"slack", "Slack", 197}, {"pagerduty", "PagerDuty", 41},
		{"jira", "Jira", 39}, {"confluence", "Confluence", 33}, {"gitlab", "GitLab", 208},
		{"teams", "Teams", 104}, {"linear", "Linear", 99}, {"notion", "Notion", 250},
		{"", "source", 245}, {"other", "other", 245},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			label, code := sourceColor(test.In)
			if label != test.WantLabel || code != test.WantCode {
				t.Errorf("sourceColor(%q) = %q, %d; want %q, %d",
					test.In, label, code, test.WantLabel, test.WantCode)
			}
		})
	}
}

func TestHumanAge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantResult string
	}{{ // Test 0: Zero reads as just now.
		In: "0s", WantResult: "just now",
	}, { // Test 1: Under a minute reads as just now.
		In: "45s", WantResult: "just now",
	}, { // Test 2: Minutes.
		In: "15m0s", WantResult: "15m ago",
	}, { // Test 3: A whole hour drops the minutes.
		In: "1h0m0s", WantResult: "1h ago",
	}, { // Test 4: Hours and minutes.
		In: "3h5m0s", WantResult: "3h 5m ago",
	}, { // Test 5: The last minute before a day stays in hours.
		In: "23h59m0s", WantResult: "23h 59m ago",
	}, { // Test 6: A day or more reads in days.
		In: "50h3m0s", WantResult: "2d ago",
	}, { // Test 7: An unparsable string is shown as is.
		In: "soon", WantResult: "soon ago",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantResult, humanAge(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
