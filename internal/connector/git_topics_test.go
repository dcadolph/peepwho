package connector

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestPathTopicsAreStated checks a subject named by a file path comes back as
// stated rather than guessed at. Everything that tells a real subject from a
// passing word reads that distinction, so when it is lost a repository full of
// demonstrated expertise reports none of it: no risk, no related topics, and a
// directory of raw tokens.
func TestPathTopicsAreStated(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")
	sub := filepath.Join(dir, "billing")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "invoice.go"), []byte("package billing\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "fix the flaky proration retry")

	recs, err := NewGitHistory(GitOptions{Paths: []string{dir}, SinceDays: 3650}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want one author", len(recs))
	}
	rec := recs[0]

	// The directory the work landed in is a subject the repository states.
	if !slices.Contains(rec.Topics, "billing") {
		t.Errorf("Topics = %v, want the path subject %q stated", rec.Topics, "billing")
	}
	// A word from the commit subject corroborates; it does not establish.
	if slices.Contains(rec.Topics, "proration") {
		t.Errorf("Topics = %v, want %q left weak: it came from prose", rec.Topics, "proration")
	}
	if !slices.Contains(rec.WeakTopics, "proration") {
		t.Errorf("WeakTopics = %v, want the commit subject mined into it", rec.WeakTopics)
	}
}

// TestCompoundPathNamesYieldTheirWords checks a directory named in snake_case
// or kebab-case is findable by the words inside it. Repositories name things
// zwave_js and rate-limiter, and nobody asks for those: they ask for zwave and
// for the rate limiter, and the whole name alone never matches either.
func TestCompoundPathNamesYieldTheirWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name string
		Path string
		Want []string
	}{{ // Test 0: snake_case, which is how most Python and Go trees are named.
		Name: "snake case", Path: "homeassistant/components/zwave_js/light.py",
		Want: []string{"zwave_js", "zwave", "components", "light"},
	}, { // Test 1: kebab-case, common in config and web trees.
		Name: "kebab case", Path: "services/rate-limiter/backoff.go",
		Want: []string{"rate-limiter", "limiter", "backoff", "services"},
	}, { // Test 2: a plain name is unaffected and not split into nothing.
		Name: "plain", Path: "billing/invoice.go", Want: []string{"billing", "invoice"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			got := pathTopics(test.Path)
			for _, want := range test.Want {
				if !slices.Contains(got, want) {
					t.Errorf("pathTopics(%q) = %v, want it to include %q", test.Path, got, want)
				}
			}
		})
	}
}

// TestJunkPathTokensAreNotSubjects checks the tokens a repository is full of
// but nobody asks about stay out of the subject list. Fixture names, device
// identifiers, and content hashes look like words to a tokenizer, and left in
// they take the top of every report that ranks subjects.
func TestJunkPathTokensAreNotSubjects(t *testing.T) {
	t.Parallel()
	junk := []string{"000001", "0001", "002s", "0101x", "2bcb3113"}
	real := []string{"zwave", "oauth2", "billing", "2fa", "utf8"}
	got := pathTopics("tests/fixtures/000001/0001.json")
	for _, j := range junk {
		if slices.Contains(got, j) {
			t.Errorf("pathTopics kept %q, which nobody would ask about", j)
		}
	}
	for _, r := range real {
		if !isSubject(r) {
			t.Errorf("isSubject(%q) = false, want a real subject kept", r)
		}
	}
	for _, j := range junk {
		if isSubject(j) {
			t.Errorf("isSubject(%q) = true, want it rejected", j)
		}
	}
}

// TestDirectoriesEstablishAndFilenamesCorroborate checks which half of a path
// is treated as naming a subject. Every area of a code base carries the same
// handful of filenames, so counting those as subjects makes the busiest
// subjects in a repository const, manifest, and __init__, and buries the areas
// people actually ask about.
func TestDirectoriesEstablishAndFilenamesCorroborate(t *testing.T) {
	t.Parallel()
	dirs, leaf := pathSubjects("homeassistant/components/zwave_js/const.py")

	for _, want := range []string{"zwave_js", "zwave", "components"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("directories = %v, want %q among them", dirs, want)
		}
	}
	if slices.Contains(dirs, "const") {
		t.Errorf("directories = %v, want the filename %q left out", dirs, "const")
	}
	if !slices.Contains(leaf, "const") {
		t.Errorf("leaf = %v, want the filename to still corroborate", leaf)
	}
	// A file at the root has no directory to belong to, so nothing is lost.
	rootDirs, rootLeaf := pathSubjects("README.md")
	if len(rootDirs)+len(rootLeaf) == 0 {
		t.Error("a file at the repository root produced no tokens at all")
	}
	// An extension that names a technology establishes regardless of position.
	tfDirs, _ := pathSubjects("infra/main.tf")
	if !slices.Contains(tfDirs, "terraform") {
		t.Errorf("directories = %v, want a .tf file to state terraform", tfDirs)
	}
}
