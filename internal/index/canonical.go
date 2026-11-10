package index

import (
	"slices"
	"strings"

	"github.com/dcadolph/whotoask/internal/model"
)

// topicAliases folds common surface forms of one concept onto a single canonical
// topic slug, so a person fluent in "k8s" ranks for "kubernetes" and the topic
// space stops splitting the same idea across synonyms. The seed is deliberately
// conservative: only unambiguous abbreviations and spellings of the same thing.
// It is the curated half of canonicalization; a co-occurrence pass can extend it
// later without changing any caller.
var topicAliases = map[string]string{
	"k8s":        "kubernetes",
	"k8":         "kubernetes",
	"kube":       "kubernetes",
	"kubectl":    "kubernetes",
	"tf":         "terraform",
	"pg":         "postgres",
	"psql":       "postgres",
	"postgresql": "postgres",
	"gh":         "github",
	"js":         "javascript",
	"ts":         "typescript",
	"ml":         "machine-learning",
	"pd":         "pagerduty",
}

// canonicalTopic slugs name and folds it onto its canonical form through the
// alias table, so many surface forms of one concept share a single topic ID.
func canonicalTopic(name string) string {
	s := slug(name)
	if c, ok := topicAliases[s]; ok {
		return c
	}
	return s
}

// noteTopic records a topic in the graph along with how it was seen: which
// source produced it, and whether that source stated it outright or inferred it
// from prose. Provenance is what lets a declared subject be told apart from a
// word that happened to appear in a title.
func noteTopic(g *model.Graph, tid model.ID, name, source string, curated bool) {
	t := g.Topics[tid]
	if t == nil {
		t = &model.Topic{ID: tid, Name: strings.ToLower(name)}
		g.Topics[tid] = t
	}
	if curated {
		t.Curated = true
	}
	if source != "" && !slices.Contains(t.Sources, source) {
		t.Sources = append(t.Sources, source)
	}
}
