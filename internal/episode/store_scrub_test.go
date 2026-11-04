package episode

import (
	"strings"
	"testing"
	"time"
)

// TestAddScrubsSecrets verifies a pasted credential never survives into the
// store: not the searchable terms, and not the archived notes, which are
// quoted back verbatim to whoever asks.
func TestAddScrubsSecrets(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add(Episode{
		ID:     "slack:C1:1",
		Source: "slack",
		Kind:   KindThread,
		Body:   "the fix was setting AKIAIOSFODNN7EXAMPLE and password=hunterthreepassword in the bucket policy",
		Archive: []Note{{
			Author: "jo@corp.com",
			At:     time.Now(),
			Text:   "use password=hunter2superpassword and token ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		}},
	})
	ep, ok := s.Episode("slack:C1:1")
	if !ok {
		t.Fatal("episode not stored")
	}
	for _, secret := range []string{
		"AKIAIOSFODNN7EXAMPLE", "hunter2superpassword", "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
	} {
		if strings.Contains(ep.Body, secret) {
			t.Errorf("body kept %q", secret)
		}
		for _, n := range ep.Archive {
			if strings.Contains(n.Text, secret) {
				t.Errorf("archive note kept %q", secret)
			}
		}
	}
	if len(ep.Archive) == 0 || !strings.Contains(ep.Archive[0].Text, "password=[redacted]") {
		t.Errorf("archive note = %+v, want the label kept and the value redacted", ep.Archive)
	}
	// The body is dropped after indexing, so its real exposure is the search
	// terms built from it; a secret there is findable by anyone who greps the
	// stored file.
	for term := range s.postings {
		if strings.Contains(term, "akiaiosfodnn7example") ||
			strings.Contains(term, "hunterthreepassword") ||
			strings.Contains(term, "hunter2superpassword") {
			t.Errorf("search term %q carries a pasted secret", term)
		}
	}
	if _, ok := s.postings["bucket"]; !ok {
		t.Error("the conversation's own words were not indexed; scrubbing ate the discussion")
	}
}
