package resolve

import (
	"slices"
	"testing"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// salientIndex builds a person who owns a declared subject and has picked up
// two words from prose along the way, which is what every real index looks
// like once free text has been read.
func salientIndex() *index.Index {
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Holly Dunn", Email: "holly@corp.com", Team: "People",
			Topics: []string{"vacation"}, WeakTopics: []string{"runbook", "issue"},
			Source: "org-csv"},
	})
	ix.Canonicalize()
	return ix
}

// TestSalientTopicsHidesMinedWords checks that what a person is known for
// leaves out the words merely mined from prose. Those words earn their keep as
// ranking signal, but presenting somebody as knowing "issue" is not an answer.
func TestSalientTopicsHidesMinedWords(t *testing.T) {
	t.Parallel()
	ix := salientIndex()
	p := ix.Graph.People["holly@corp.com"]
	if p == nil {
		t.Fatal("person not indexed")
		return
	}
	got := salientTopics(ix, p.Topics, 8)
	if !slices.Contains(got, "vacation") {
		t.Errorf("topics = %v, want the declared subject", got)
	}
	for _, mined := range []string{"runbook", "issue"} {
		if slices.Contains(got, mined) {
			t.Errorf("topics = %v, want %q left out: it was only mined from prose", got, mined)
		}
	}
}

// TestSalientTopicsFallsBackWhenNothingIsDeclared checks a person whose every
// topic came from prose still gets an answer rather than an empty list.
func TestSalientTopicsFallsBackWhenNothingIsDeclared(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Name: "Pat Vance", Email: "pat@corp.com", WeakTopics: []string{"runbook"}, Source: "github"},
	})
	ix.Canonicalize()
	p := ix.Graph.People["pat@corp.com"]
	if got := salientTopics(ix, p.Topics, 8); len(got) == 0 {
		t.Error("topics = empty, want the unfiltered list rather than nothing")
	}
}

// TestDirectoryListsSubjectsNotMinedWords checks the browsable list is the
// subjects a company has, not every word its prose contained.
func TestDirectoryListsSubjectsNotMinedWords(t *testing.T) {
	t.Parallel()
	dir := BuildDirectory(salientIndex())
	var names []string
	for _, tp := range dir.Topics {
		names = append(names, tp.Name)
	}
	if !slices.Contains(names, "vacation") {
		t.Errorf("directory topics = %v, want the declared subject", names)
	}
	for _, mined := range []string{"runbook", "issue"} {
		if slices.Contains(names, mined) {
			t.Errorf("directory topics = %v, want %q left out", names, mined)
		}
	}
}
