package resolve

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestPlaceChecksResolveLabels verifies each tally key maps back to the clone
// it came from: bare keys to the single repository, prefixed keys through the
// label, and a key no repository claims yields no command rather than a wrong
// one.
func TestPlaceChecksResolveLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name     string
		Places   []Place
		Labels   map[string]string
		WantDirs []string
		WantCmd  string
	}{{ // Test 0: A single-repository read keeps bare directory names.
		Name:     "single repository",
		Places:   []Place{{Dir: "internal/billing"}},
		Labels:   map[string]string{"./repo": ""},
		WantDirs: []string{"internal/billing"},
		WantCmd:  "git -C 'repo' log --since=\"730 days ago\" --no-merges --format='%an <%ae>' -- 'internal/billing'",
	}, { // Test 1: A labelled key resolves through its repository's label.
		Name:     "two repositories",
		Places:   []Place{{Dir: "svc-a/internal"}, {Dir: "svc-b/internal"}},
		Labels:   map[string]string{"/data/svc-a": "svc-a", "/data/svc-b": "svc-b"},
		WantDirs: []string{"svc-a/internal", "svc-b/internal"},
	}, { // Test 2: A key nothing claims is dropped, never guessed.
		Name:     "unclaimed key",
		Places:   []Place{{Dir: "orphan/internal"}},
		Labels:   map[string]string{"/data/svc-a": "svc-a"},
		WantDirs: nil,
	}, { // Test 3: The longer label wins when one is a prefix of another.
		Name:     "prefix labels",
		Places:   []Place{{Dir: "svc/internal"}, {Dir: "svc-billing/internal"}},
		Labels:   map[string]string{"/data/svc": "svc", "/data/svc-billing": "svc-billing"},
		WantDirs: []string{"svc/internal", "svc-billing/internal"},
	}}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			got := PlaceChecks(test.Places, test.Labels, 730)
			dirs := make([]string, 0, len(got))
			for _, c := range got {
				dirs = append(dirs, c.Dir)
			}
			if diff := cmp.Diff(test.WantDirs, dirs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dirs (-want +got):\n%s", diff)
			}
			if test.WantCmd != "" && (len(got) == 0 || !strings.Contains(got[0].Command, test.WantCmd)) {
				t.Errorf("command = %q, want it to carry %q", got[0].Command, test.WantCmd)
			}
		})
	}
}

// TestPlaceChecksPrefixResolution pins test 3's substance: the svc-billing key
// must resolve to the svc-billing clone, not to svc with "-billing/internal"
// left dangling.
func TestPlaceChecksPrefixResolution(t *testing.T) {
	t.Parallel()
	got := PlaceChecks(
		[]Place{{Dir: "svc-billing/internal"}},
		map[string]string{"/data/svc": "svc", "/data/svc-billing": "svc-billing"}, 90)
	if len(got) != 1 {
		t.Fatalf("checks = %d, want 1", len(got))
	}
	if !strings.Contains(got[0].Command, "-C 'svc-billing'") ||
		!strings.Contains(got[0].Command, "-- 'internal'") {
		t.Errorf("command = %q, want the svc-billing clone and the bare directory", got[0].Command)
	}
}

// TestShellQuote verifies a hostile path cannot escape its quoting, because
// the command lands in a client's terminal by design.
func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{
		// Test 0: A plain path is wrapped as is.
		{In: "internal/billing", Want: "'internal/billing'"},
		// Test 1: A single quote cannot terminate the quoting.
		{In: "a'b", Want: `'a'\''b'`},
		// Test 2: Empty stays a valid empty argument.
		{In: "", Want: "''"},
		// Test 3: Command substitution stays inert inside single quotes.
		{In: "$(rm -rf /)", Want: "'$(rm -rf /)'"},
	}
	for testNum, test := range tests {
		if got := shellQuote(test.In); got != test.Want {
			t.Errorf("test %d: shellQuote(%q) = %s, want %s", testNum, test.In, got, test.Want)
		}
	}
}

// TestPlaceChecksCarryNoAssessorPaths pins the privacy property: the machine
// an assessment ran on must not leak into a deliverable a client receives, so
// no command may carry an absolute path.
func TestPlaceChecksCarryNoAssessorPaths(t *testing.T) {
	t.Parallel()
	got := PlaceChecks(
		[]Place{{Dir: "internal/billing"}, {Dir: "svc-a/cmd"}},
		map[string]string{"/Users/somebody/clients/acme/repo": "", "/Users/somebody/clients/acme/svc-a": "svc-a"},
		365)
	for _, c := range got {
		if strings.Contains(c.Command, "/Users/") || strings.Contains(c.Command, "/private/") {
			t.Errorf("command leaks an assessor path: %s", c.Command)
		}
	}
	if len(got) == 0 {
		t.Fatal("no checks produced")
	}
}
