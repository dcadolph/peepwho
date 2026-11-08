package simorg

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// TestHostileQueries pushes the junk a search box actually receives through the
// resolver. None of it should panic, error, or hang: a question peepwho cannot
// answer is an empty answer, not a crash.
func TestHostileQueries(t *testing.T) {
	t.Parallel()
	ix, err := buildIndexFor(t.TempDir(), BigSpec())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	r := resolve.NewKeyword(ix)

	queries := []struct{ Name, Query string }{
		{"empty", ""},
		{"spaces", "     "},
		{"tabs and newlines", "\t\n\r\n  \t"},
		{"punctuation only", "???!!!..."},
		{"one letter", "a"},
		{"regex metachars", "a[b)c*+?^$|{}\\"},
		{"sql-ish", "'; DROP TABLE people; --"},
		{"html", "<script>alert(1)</script>"},
		{"path traversal", "../../../../etc/passwd"},
		{"null byte", "who knows \x00 kafka"},
		{"unicode", "誰がカフカを知っていますか"},
		{"emoji", "🔥🔥🔥 who owns 🚀"},
		{"combining marks", strings.Repeat("é", 200)},
		{"rtl override", "who knows \u202ekafka"},
		{"very long token", strings.Repeat("kafka", 4000)},
		{"very long question", "who knows " + strings.Repeat("kafka ", 3000)},
		{"only stopwords", "the a an of to and is are was"},
		{"name prefix with nothing", "who is "},
		{"name suffix with nothing", " know about"},
		{"repeated at signs", strings.Repeat("@", 500)},
		{"lone at", "@"},
		{"email shaped junk", "@@@@@@@@@@@@@@@@@@@@@@@@example.com"},
		{"negative-looking", "-1e999"},
		{"windows newline flood", strings.Repeat("\r\n", 5000)},
	}
	for testNum, test := range queries {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			ans, err := r.Resolve(context.Background(), test.Query, 5)
			if err != nil {
				t.Fatalf("resolve %q: %v", test.Name, err)
			}
			if len(ans.People) > 5 {
				t.Errorf("resolve %q returned %d people, want at most the 5 asked for",
					test.Name, len(ans.People))
			}
			for _, m := range ans.People {
				if m.Person == nil {
					t.Fatalf("resolve %q returned a match with no person", test.Name)
				}
			}
		})
	}
}

// TestHostileLimits covers the other half of the call: the limit the caller
// passes. Zero and negative arrive from query strings and must not panic or
// quietly return the whole company.
func TestHostileLimits(t *testing.T) {
	t.Parallel()
	ix, err := buildIndexFor(t.TempDir(), BigSpec())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	r := resolve.NewKeyword(ix)
	for testNum, limit := range []int{0, -1, -1000, 1, 1 << 20} {
		t.Run(fmt.Sprintf("test %d limit %d", testNum, limit), func(t *testing.T) {
			t.Parallel()
			ans, err := r.Resolve(context.Background(), "who knows kafka", limit)
			if err != nil {
				t.Fatalf("resolve limit %d: %v", limit, err)
			}
			if limit > 0 && len(ans.People) > limit {
				t.Errorf("limit %d returned %d people", limit, len(ans.People))
			}
			if limit <= 0 && len(ans.People) > len(ix.Graph.People) {
				t.Errorf("limit %d returned %d people, more than exist", limit, len(ans.People))
			}
		})
	}
}
