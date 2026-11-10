package index

import (
	"github.com/dcadolph/whotoask/internal/text"
)

// The index shares one normalizer with every other searchable store in the
// program, so a posting key here and a query term elsewhere are produced by
// identical rules. These wrappers keep the package's own call sites short.

// tokenize splits s into searchable tokens, dropping stopwords.
func tokenize(s string) []string { return text.Tokenize(s) }

// slug normalizes text into a lowercase hyphenated identifier.
func slug(s string) string { return text.Slug(s) }

// stem reduces a token to its root for matching.
func stem(token string) string { return text.Stem(token) }

// stemMatch reports whether want stems to a token of texts, and the word it found.
func stemMatch(want string, texts ...string) (string, bool) { return text.StemMatch(want, texts...) }
