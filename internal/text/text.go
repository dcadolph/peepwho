// Package text normalizes free text into the tokens whotoask matches on. Every
// index in the program shares it, so a posting key and a query term are always
// produced by the same rules.
package text

import (
	"strings"
	"unicode"

	"github.com/kljensen/snowball"
	"golang.org/x/text/unicode/norm"
)

// stopwords are common words ignored during tokenization. They include the
// filler words found in questions like "who do I talk to about X".
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "who": true, "are": true,
	"with": true, "about": true, "talk": true, "owns": true, "own": true,
	"what": true, "how": true, "our": true, "you": true, "your": true,
	"this": true, "that": true, "from": true, "does": true, "can": true,
	"do": true, "to": true, "of": true, "in": true, "on": true, "or": true,
	"is": true, "it": true, "we": true, "me": true, "my": true, "an": true,
	"at": true, "be": true, "as": true, "by": true, "us": true, "need": true,
	"help": true, "have": true, "has": true, "get": true, "got": true,
	"know": true, "knows": true, "handle": true, "handles": true,
	"where": true, "when": true, "which": true, "why": true,
	// Conversational filler. These carry no subject on their own, and left in
	// they are worse than noise: fuzzy matching will bend a short common word
	// into a real topic, so a question containing "here" confidently answers
	// with whoever works on hiring.
	"here": true, "there": true, "anyone": true, "anybody": true,
	"someone": true, "somebody": true, "please": true, "thanks": true,
	"hey": true, "hi": true, "hello": true, "should": true, "would": true,
	// Quantifiers and qualifiers. A question is mostly these, and none of them
	// names a subject: asked who knows the most about something, whotoask was
	// answering with whoever had touched a file called most.
	"most": true, "more": true, "less": true, "least": true, "many": true,
	"much": true, "few": true, "new": true, "old": true, "first": true, "last": true, "next": true, "other": true,
	"another": true, "same": true, "different": true, "thing": true,
	"things": true, "stuff": true, "way": true, "ways": true, "lot": true,
	"could": true, "will": true, "was": true, "were": true, "been": true,
	"but": true, "not": true, "any": true, "all": true, "some": true,
	"good": true, "best": true, "better": true, "really": true, "just": true,
	"ask": true, "asking": true, "asked": true, "tell": true, "find": true,
	"looking": true, "trying": true, "wondering": true,
}

// Tokenize folds and lowercases text, then splits it into searchable tokens,
// dropping stopwords and tokens shorter than two bytes. Folding removes
// diacritics so "josé" and "jose" share a token, and letters of any script are
// kept so a non-ASCII name is not mangled or dropped. Folding runs first
// because NFKD expands compatibility characters like ㏜ into letters ("Sv")
// that must still be lowercased.
func Tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(Fold(s)), func(r rune) bool {
		return !isWordRune(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) < 2 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Terms tokenizes s and stems each token, giving the exact keys the scorers
// store and look up.
func Terms(s string) []string {
	tokens := Tokenize(s)
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, Stem(t))
	}
	return out
}

// isWordRune reports whether r can be part of a token: a letter or digit from
// any script. Folding removes diacritics before this runs, so accented letters
// arrive as their base form.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Fold removes diacritics by decomposing to NFKD and dropping the combining
// marks, so "josé" folds to "jose". Letters from scripts without combining
// marks, such as CJK, pass through unchanged. Invalid UTF-8 bytes, which
// appear when a source file was written in a legacy encoding, become the
// replacement character before normalizing; left in place they make the
// normalizer skip the characters next to them. It is safe for concurrent use.
func Fold(s string) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Slug normalizes text into a lowercase identifier with single hyphens between
// runs of word runes. Diacritics fold so accented and unaccented spellings
// produce the same slug. Unlike Tokenize, it keeps every word.
func Slug(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(Fold(strings.TrimSpace(s))) {
		if isWordRune(r) {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// Stem reduces a token to its root for matching, so "scans", "scan", and
// "scanning" share a posting. It is applied only to posting keys and query
// terms, never to displayed text, so reasons and names stay readable.
func Stem(token string) string {
	s, err := snowball.Stem(token, "english", true)
	if err != nil || s == "" {
		return token
	}
	return s
}

// StemMatch is StemMatches with the word it found. Reporting a correction is
// only useful if it names what was matched: told that "zigby" was corrected,
// somebody still does not know whether whotoask read it as zigbee or as zigzag.
func StemMatch(want string, texts ...string) (string, bool) {
	for _, txt := range texts {
		for _, tok := range Tokenize(txt) {
			if Stem(tok) == want {
				return tok, true
			}
		}
	}
	return "", false
}
