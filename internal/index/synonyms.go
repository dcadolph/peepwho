package index

import (
	"maps"
	"slices"
	"strings"
	"sync"
)

// synonymGroups are expressions that name the same thing. When a query uses any
// member of a group, the search also tries the others, at a discount, so asking
// about "time off" finds the person the index knows for "vacation". Groups are
// deliberately conservative: a wrong synonym silently hands answers to the wrong
// person, so a term only belongs here when the equivalence holds anywhere a
// workplace would use it. Multi-word members are detected as adjacent words in
// the question; only single words can be added to a search, since the index
// posts single tokens.
var synonymGroups = [][]string{
	// The everyday questions every company has.
	{"vacation", "pto", "ooo", "time off", "days off", "out of office"},
	{"payroll", "paycheck", "salary", "compensation", "pay stub"},
	{"benefits", "insurance", "medical", "dental", "health plan"},
	{"expenses", "reimbursement", "receipts", "expense report"},
	{"laptop", "computer", "workstation", "hardware"},
	{"onboarding", "orientation", "new hire", "first day"},
	{"hiring", "recruiting", "interviews", "candidates"},
	{"office", "facilities", "parking", "desks"},
	{"contracts", "nda", "legal", "agreement"},
	// The engineering vocabulary that splits by tool and abbreviation.
	{"kubernetes", "k8s", "kube"},
	{"terraform", "iac"},
	{"postgres", "postgresql", "database"},
	{"authentication", "auth", "login", "sso", "sign in"},
	{"deploys", "deployment", "rollout", "shipping"},
	{"incident", "outage", "oncall", "paging", "on call"},
	{"monitoring", "observability", "metrics", "alerting"},
	{"billing", "invoices", "payments"},
}

// expansionPenalty scales what a synonym contributes. A synonym is the right
// idea in different words, which is close to what was asked but never quite it,
// so it must not outrank a person who matches the words themselves.
const expansionPenalty = 0.8

// expTerm is one extra term a query grows: the word to search, the original
// query words it stands in for, and the expression that triggered it, for the
// reason line.
type expTerm struct {
	// term is the word added to the search.
	term string
	// covers are the original query terms this expansion answers for, so a hit
	// through a synonym still counts as covering what was asked.
	covers []string
	// asked is the expression the person actually used.
	asked string
}

// synonym lookup tables, built once from the groups.
var (
	synOnce sync.Once
	// wordGroup maps a single word's stem to its group.
	wordGroup map[string]int
	// phraseGroup maps a phrase of two or three words, as joined stems, to its
	// group.
	phraseGroup map[string]int
	// groupSingles lists each group's single-word members, the only ones that
	// can be searched.
	groupSingles [][]string
)

// buildSynonyms indexes the groups by stem for lookup during a query.
func buildSynonyms() {
	wordGroup = make(map[string]int)
	phraseGroup = make(map[string]int)
	groupSingles = make([][]string, len(synonymGroups))
	for g, group := range synonymGroups {
		for _, member := range group {
			parts := strings.Fields(member)
			if len(parts) == 1 {
				wordGroup[stem(parts[0])] = g
				groupSingles[g] = append(groupSingles[g], parts[0])
				continue
			}
			stems := make([]string, len(parts))
			for i, part := range parts {
				stems[i] = stem(part)
			}
			phraseGroup[strings.Join(stems, " ")] = g
		}
	}
}

// rawWords splits a question into lowercased words with nothing removed.
// Phrase detection has to see the question as typed: the search tokenizer
// strips stopwords, and "sign in" or "out of office" are exactly the phrases
// whose identifying words a stopword list eats.
func rawWords(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// expandTerms returns the synonyms a question brings in: every single-word
// member of every group the question touches, minus the words already asked.
// The raw question is scanned for phrases, words and stopwords alike, so an
// expression is recognized as the person typed it: the tokenizer's stopword
// list would otherwise eat the middle of "sign in" and "out of office".
func expandTerms(query string) []expTerm {
	synOnce.Do(buildSynonyms)
	words := rawWords(query)
	type trigger struct {
		covers []string
		asked  string
	}
	triggered := make(map[int]trigger)
	note := func(g int, parts []string) {
		if _, seen := triggered[g]; !seen {
			triggered[g] = trigger{covers: parts, asked: strings.Join(parts, " ")}
		}
	}
	for n := 3; n >= 2; n-- {
		for i := 0; i+n <= len(words); i++ {
			stems := make([]string, n)
			for j := range n {
				stems[j] = stem(words[i+j])
			}
			if g, ok := phraseGroup[strings.Join(stems, " ")]; ok {
				note(g, words[i:i+n])
			}
		}
	}
	for _, w := range words {
		if g, ok := wordGroup[stem(w)]; ok {
			note(g, []string{w})
		}
	}
	if len(triggered) == 0 {
		return nil
	}

	present := make(map[string]bool, len(words))
	for _, w := range words {
		present[stem(w)] = true
	}
	// Expansions come out in group order, then member order. The list feeds
	// scoring, where per-person floats accumulate term by term, so an unstable
	// order here changes the last bits of scores between runs of one query on
	// one index, and byte-for-byte reproducibility is a product claim.
	groups := slices.Sorted(maps.Keys(triggered))
	var out []expTerm
	for _, g := range groups {
		trig := triggered[g]
		for _, w := range groupSingles[g] {
			if present[stem(w)] {
				continue
			}
			present[stem(w)] = true
			out = append(out, expTerm{term: w, covers: trig.covers, asked: trig.asked})
		}
	}
	return out
}
