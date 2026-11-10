package simorg

import (
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// Test 0: The big demo company builds at scale with expertise and show names.
func TestBuildBigIndex(t *testing.T) {
	t.Parallel()
	ix, err := BuildBigIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildBigIndex: %v", err)
	}
	t.Logf("people=%d channels=%d", len(ix.Graph.People), len(ix.Graph.Channels))

	names := make([]string, 0, 14)
	for _, p := range ix.Graph.People {
		names = append(names, p.Name)
		if len(names) >= 14 {
			break
		}
	}
	t.Logf("sample names: %s", strings.Join(names, ", "))

	risks := resolve.Risk(ix, 8)
	for _, r := range risks {
		exp := ""
		if len(r.Experts) > 0 {
			exp = r.Experts[0].Name
		}
		t.Logf("RISK %-22s level=%-8s bus=%d top=%s", r.Topic, r.Level, r.BusFactor, exp)
	}

	drift := resolve.Ownership(ix).Drift
	t.Logf("ownership drift entries: %d", len(drift))
	for i, d := range drift {
		if i >= 4 {
			break
		}
		t.Logf("DRIFT %-22s declared=%s actual=%s", d.Topic, d.Declared, d.Actual)
	}

	if len(risks) > 0 && len(risks[0].Experts) > 0 {
		imp := resolve.Departure(ix, risks[0].Experts[0].ID)
		t.Logf("DEPARTURE %s sole=%v top=%v", imp.Name, imp.Sole, imp.Top)
	}

	if got := len(ix.Graph.People); got < 200 {
		t.Errorf("people = %d, want >= 200", got)
	}
	if got := len(ix.Graph.Channels); got < 8 {
		t.Errorf("channels = %d, want >= 8", got)
	}
	if len(risks) < 5 {
		t.Errorf("risk topics = %d, want several", len(risks))
	}

	if _, err := BuildBigEpisodes(ix); err != nil {
		t.Fatalf("BuildBigEpisodes: %v", err)
	}
	email, query := BigDemoPerson()
	if email == "" || query == "" {
		t.Errorf("BigDemoPerson returned empty: %q %q", email, query)
	}
	t.Logf("demo person=%s query=%q", email, query)
	found := false
	for _, p := range ix.Graph.People {
		for _, sn := range []string{"Halpert", "Tanner", "Pinkman", "Schrute", "Bachman"} {
			if strings.Contains(p.Name, sn) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected a show surname among the generated people")
	}
}
