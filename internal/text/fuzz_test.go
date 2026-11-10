package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzNormalizers feeds the tokenizer family arbitrary bytes, since it runs
// over every message, name, and title whotoask ever ingests, in whatever
// encoding a source emits. Properties: nothing panics, tokens are never empty,
// a slug never contains a space, and normalizing twice equals normalizing
// once, so re-indexing cannot drift.
func FuzzNormalizers(f *testing.F) {
	for _, seed := range []string{
		"", " ", "billing retries", "José Zoë Søren Müller",
		"кириллица 漢字 عربى", "a\x00b\xff\xfe", "🎉🎉 emoji only",
		strings.Repeat("kafka ", 500), "under_score-hyphen.dot@sign",
		"́́ bare combining marks", "ＦＵＬＬＷＩＤＴＨ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tokens := Tokenize(s)
		for _, tok := range tokens {
			if tok == "" {
				t.Fatalf("Tokenize(%q) produced an empty token", s)
			}
		}
		terms := Terms(s)
		for _, term := range terms {
			if term == "" {
				t.Fatalf("Terms(%q) produced an empty term", s)
			}
		}
		slug := Slug(s)
		if strings.ContainsAny(slug, " \t\n") {
			t.Fatalf("Slug(%q) = %q contains whitespace", s, slug)
		}
		if slug != "" && !utf8.ValidString(slug) {
			t.Fatalf("Slug(%q) = %q is not valid UTF-8", s, slug)
		}
		if again := Slug(slug); again != slug {
			t.Fatalf("Slug is not idempotent: %q -> %q -> %q", s, slug, again)
		}
		folded := Fold(s)
		if Fold(folded) != folded {
			t.Fatalf("Fold is not idempotent on %q", s)
		}
	})
}

// FuzzScrub feeds the credential scrubber arbitrary bytes, since it runs over
// every message and title whotoask ingests and a panic there loses the record.
// Properties: nothing panics, text with no findings passes through unchanged,
// every finding leaves the mark behind, and valid UTF-8 stays valid.
func FuzzScrub(f *testing.F) {
	for _, seed := range []string{
		"", "plain prose about billing retries",
		"creds AKIAIOSFODNN7EXAMPLE here",
		"xoxc-1234567890123-4567890123456-abcDEFghiJKLmno",
		"d=xoxd-Ab3%2FqT9%3D%3Dabcdef",
		"xapp-1-A0123ABCDEF-98765-abcdef0123456789",
		"https://alice:Hunter2pass@db.corp.com",
		"ssh://deploy@bastion and https://db.corp.com:8080/health",
		"sk-proj" + "-" + strings.Repeat("Ab1", 8),
		"sk-ant" + "-api03-" + strings.Repeat("x9Y", 10),
		"password=hunter2secret",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		"Authorization: Bearer abcdef0123456789abcdef",
		"a\x00b\xff\xfe :=@ ://:@",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, found := Scrub(s)
		if found == 0 && out != s {
			t.Fatalf("Scrub(%q) changed text while reporting nothing found: %q", s, out)
		}
		if found > 0 && !strings.Contains(out, "[redacted]") {
			t.Fatalf("Scrub(%q) reported %d found but left no mark: %q", s, found, out)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("Scrub(%q) broke UTF-8: %q", s, out)
		}
	})
}
