package cmd

import (
	"strings"
	"testing"
)

// TestOnceValueRejectsASecondSource pins the repeated --source contract: the
// second value fails the parse and the error names both values and --merge,
// instead of silently indexing only the last source named.
func TestOnceValueRejectsASecondSource(t *testing.T) {
	t.Parallel()
	var source string
	v := newOnceValue(&source, "org-csv")
	if source != "org-csv" {
		t.Fatalf("default = %q, want org-csv", source)
	}
	if err := v.Set("git"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if source != "git" {
		t.Fatalf("after first Set source = %q, want git", source)
	}
	err := v.Set("codeowners")
	if err == nil {
		t.Fatal("second Set accepted, want an error")
		return
	}
	for _, want := range []string{"git", "codeowners", "--merge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if source != "git" {
		t.Errorf("after rejected Set source = %q, want git untouched", source)
	}
}
