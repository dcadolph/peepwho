package index

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestTokenizeUnicode verifies non-ASCII names tokenize instead of being mangled
// or dropped, and that diacritics fold so accented and unaccented spellings
// share a token.
func TestTokenizeUnicode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want []string
	}{{ // Test 0: Accents fold to the base letters.
		In: "José García", Want: []string{"jose", "garcia"},
	}, { // Test 1: More diacritics fold the same way.
		In: "naïve café résumé", Want: []string{"naive", "cafe", "resume"},
	}, { // Test 2: A CJK name is kept as a token, not dropped.
		In: "李明", Want: []string{"李明"},
	}, { // Test 3: ASCII text is unchanged.
		In: "billing retries", Want: []string{"billing", "retries"},
	}}
	for testNum, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, tokenize(test.In)); diff != "" {
				t.Errorf("test %d: tokenize(%q) mismatch (-want +got):\n%s", testNum, test.In, diff)
			}
		})
	}
}

// TestSlugUnicode verifies slugs fold diacritics so an accented name and its
// unaccented spelling produce the same identifier.
func TestSlugUnicode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{
		{In: "José García", Want: "jose-garcia"},
		{In: "Zoë Müller", Want: "zoe-muller"},
		{In: "Jane Roe", Want: "jane-roe"},
	}
	for testNum, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if got := slug(test.In); got != test.Want {
				t.Errorf("test %d: slug(%q) = %q, want %q", testNum, test.In, got, test.Want)
			}
		})
	}
}
