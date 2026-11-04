package simorg

import (
	"strings"
	"testing"
)

// TestEveryoneReportsSomewhere checks the generated company is a tree rather
// than a crowd. The org chart draws whatever the reporting lines say, so a
// person left without a manager, or made their own, becomes another root and
// the chart opens as a flat row instead of a hierarchy.
func TestEveryoneReportsSomewhere(t *testing.T) {
	t.Parallel()
	c := buildCompany(BigSpec())
	rows := strings.Split(strings.TrimSpace(c.orgCSV()), "\n")[1:]

	type row struct{ email, manager, title string }
	byEmail := make(map[string]row, len(rows))
	var order []string
	for _, line := range rows {
		f := strings.Split(line, ",")
		if len(f) < 6 {
			t.Fatalf("org row has %d columns, want at least 6: %q", len(f), line)
		}
		r := row{email: f[1], manager: f[5], title: f[2]}
		byEmail[r.email] = r
		order = append(order, r.email)
	}

	var roots, self int
	for _, email := range order {
		r := byEmail[email]
		switch r.manager {
		case r.email:
			self++
			t.Errorf("%s (%s) is their own manager", r.email, r.title)
		case "":
			roots++
		default:
			if _, ok := byEmail[r.manager]; !ok {
				t.Errorf("%s reports to %s, who is not in the company", r.email, r.manager)
			}
		}
	}
	if roots != 1 {
		t.Errorf("company has %d people with no manager, want exactly the one at the top", roots)
	}

	// Walking up from anybody has to reach the top, which is only true when the
	// reporting lines carry no cycle.
	for _, email := range order {
		seen := map[string]bool{}
		for at := email; at != ""; at = byEmail[at].manager {
			if seen[at] {
				t.Fatalf("reporting line from %s loops at %s", email, at)
			}
			seen[at] = true
		}
	}
}
