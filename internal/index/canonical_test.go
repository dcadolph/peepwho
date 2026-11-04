package index

import (
	"fmt"
	"testing"
)

// TestCanonicalTopic checks that synonyms and abbreviations fold onto one topic.
func TestCanonicalTopic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantSlug string
	}{ // Test 0: k8s abbreviation folds to kubernetes.
		{In: "k8s", WantSlug: "kubernetes"},
		{In: "Kube", WantSlug: "kubernetes"},
		{In: "kubectl", WantSlug: "kubernetes"},
		{In: "TF", WantSlug: "terraform"},
		{In: "postgres", WantSlug: "postgres"},
		{In: "PostgreSQL", WantSlug: "postgres"},
		{In: "billing retries", WantSlug: "billing-retries"},
		{In: "kafka", WantSlug: "kafka"},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := canonicalTopic(test.In); got != test.WantSlug {
				t.Errorf("canonicalTopic(%q) = %q, want %q", test.In, got, test.WantSlug)
			}
		})
	}
}
