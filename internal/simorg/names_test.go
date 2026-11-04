package simorg

import "testing"

// TestNoExactCharacters checks that the generated company recombines the name
// pools rather than reproducing the people they came from.
func TestNoExactCharacters(t *testing.T) {
	t.Parallel()
	c := buildCompany(BigSpec())
	seen := make(map[string]bool)
	for _, p := range c.people {
		if showCharacters[p.name] {
			t.Errorf("generated %q, which is one of the originals", p.name)
		}
		seen[p.name] = true
	}
	if len(seen) < len(c.people)*8/10 {
		t.Errorf("only %d distinct names for %d people, the mix is too repetitive",
			len(seen), len(c.people))
	}
	t.Logf("%d people, %d distinct names", len(c.people), len(seen))
	for i := 0; i < 12 && i < len(c.people); i++ {
		t.Logf("  %s", c.people[i].name)
	}
}
