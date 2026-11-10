package index

import (
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestCorrectionPrefersTheEstablishedWord checks a typo is read as the word the
// organization uses rather than as one of its own misspellings. Real material
// contains both, and a misspelling sits exactly as close to a typo as the
// correct word does, so whichever sorted first used to win: asked about
// "blutooth", whotoask answered with a directory misspelled "blueooth".
func TestCorrectionPrefersTheEstablishedWord(t *testing.T) {
	t.Parallel()
	ix := New()
	recs := []connector.Record{{
		Kind: connector.KindPerson, Email: "typo@corp.com", Name: "Typo",
		Topics: []string{"blueooth"}, Source: "git",
	}}
	// The real word, held by enough people to be what the company calls it.
	for _, who := range []string{"ada", "bo", "cy", "di", "ez"} {
		recs = append(recs, connector.Record{
			Kind: connector.KindPerson, Email: who + "@corp.com", Name: strings.ToUpper(who),
			Topics: []string{"bluetooth"}, Source: "git",
		})
	}
	ix.Build(recs)
	ix.Canonicalize()

	got := ix.Search("blutooth", 3)
	if len(got) == 0 {
		t.Fatal("the typo matched nothing at all")
	}
	reasons := strings.Join(got[0].Reasons, " ")
	if !strings.Contains(reasons, "bluetooth") {
		t.Errorf("reasons = %v, want the typo read as the established word", got[0].Reasons)
	}
	if strings.Contains(reasons, "blueooth") {
		t.Errorf("reasons = %v, want the misspelling in the data left alone", got[0].Reasons)
	}
	// And the correction says what it was read as, so a wrong guess is visible
	// rather than silent.
	if !strings.Contains(reasons, `read for "blutooth"`) {
		t.Errorf("reasons = %v, want the correction to name the word it came from", got[0].Reasons)
	}
}
