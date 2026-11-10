package simorg

import (
	"fmt"
	"testing"
)

// TestWhatEachSourceIsWorth measures whether folding more tools together
// actually answers better, which the product's whole pitch assumes and nothing
// had ever checked. It builds the same company from growing subsets of its
// sources and asks the same questions of each.
//
// What it found, and what to expect when it is run again:
//
//   - Git alone answers 0.914, from 43 people. The ranking works on evidence of
//     work, without any source stating who owns what.
//   - Adding GitHub, Jira, Confluence, and PagerDuty moves precision NOT AT ALL.
//     They agree with git rather than adding to it.
//   - Slack takes it to 1.000, and takes the index from 43 people to 220. That
//     is the whole shape of the result: SOURCES BUY COVERAGE, NOT PRECISION.
//     Most people never appear in a commit, and a source that reaches them is
//     what puts them in the index to be found at all.
//   - CODEOWNERS and org-csv then add nothing, being already saturated.
//
// One caveat this test exists to keep visible: the generated org-csv carries a
// topics column naming exactly what each person owns, which is this gauntlet's
// own answer key. Put it first and a lone spreadsheet scores 0.909. That is why
// it is ordered last here, and why any future ablation has to account for it.
func TestWhatEachSourceIsWorth(t *testing.T) {
	if testing.Short() {
		t.Skip("builds eight indexes")
	}
	c := buildCompany(BigSpec())
	cases := brainGauntlet(c)

	// Added in the order a company would connect them: the directory first,
	// then where the work is recorded, then where it is discussed.
	// org-csv is deliberately LAST. The generated CSV carries a topics column
	// naming exactly what each person owns, which is the gauntlet's own answer
	// key, so putting it first measures whether whotoask can read a spreadsheet.
	// Everything before it is work: what people committed, filed, wrote, and
	// were paged for.
	order := []string{"git", "github", "jira", "confluence", "pagerduty", "slack", "codeowners", "org-csv"}
	only := map[string]bool{}
	for i, name := range order {
		only[name] = true
		set := make(map[string]bool, len(only))
		for k, v := range only {
			set[k] = v
		}
		ix, err := buildIndexFrom(t.TempDir(), BigSpec(), set)
		if err != nil {
			t.Fatalf("%d sources: %v", i+1, err)
		}
		byStyle, _ := scoreBrain(ix, cases)
		var top1, top3, asked int
		for style, tally := range byStyle {
			if style == "anchored" {
				continue
			}
			top1 += tally[0]
			top3 += tally[1]
			asked += tally[2]
		}
		if asked == 0 {
			t.Logf("%2d sources (+%-11s) nothing answerable", i+1, name)
			continue
		}
		t.Logf("%2d sources (+%-11s) p@1 %.3f  p@3 %.3f  people %d",
			i+1, name, float64(top1)/float64(asked), float64(top3)/float64(asked),
			len(ix.Graph.People))
	}
	_ = fmt.Sprint()
}
