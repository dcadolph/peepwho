package simorg

import (
	"slices"
	"testing"

	"github.com/dcadolph/peepwho/internal/feedback"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
)

// buildFullIndex ingests every source against the simulated org and returns
// the merged, canonicalized index.
func buildFullIndex(t *testing.T) *index.Index {
	t.Helper()
	ix, err := BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return ix
}

// TestFullPipeline runs all eight sources through the real clients against
// wire-format servers and checks the truths that only show up end to end.
func TestFullPipeline(t *testing.T) {
	t.Parallel()
	ix := buildFullIndex(t)

	// One node per human: twelve people, no bots, no source-id duplicates.
	if got := len(ix.Graph.People); got != 12 {
		ids := make([]model.ID, 0, got)
		for id := range ix.Graph.People {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		t.Fatalf("people = %d, want 12: %v", got, ids)
	}
	for id := range ix.Graph.People {
		if p := string(id); p == "github:eve-dev" || p == "github:buildbot[bot]" {
			t.Errorf("unmerged or bot identity survived: %s", p)
		}
	}

	// Eve joined through the alias file and remembers her GitHub identity.
	eve := ix.Graph.People["eve@example.com"]
	if eve == nil {
		t.Fatal("missing eve@example.com")
		return
	}
	if !slices.Contains(eve.Identities, model.ID("github:eve-dev")) {
		t.Errorf("eve identities = %v, want github:eve-dev", eve.Identities)
	}

	// Cross-source ranking: the right owner tops each question.
	asks := []struct {
		Query      string
		WantPerson model.ID
	}{
		{"billing retries", "angela@example.com"},
		{"kafka streaming", "bob@example.com"},
		{"sso login", "dan@example.com"},
		{"react frontend", "eve@example.com"},
		{"embeddings model serving", "frank@example.com"},
		{"terraform", "carol@example.com"},
	}
	for _, ask := range asks {
		got := ix.Search(ask.Query, 3)
		if len(got) == 0 || got[0].Person.ID != ask.WantPerson {
			t.Errorf("Search(%q) top = %v, want %s", ask.Query, first(got), ask.WantPerson)
			continue
		}
		if got[0].Strength < 0.45 {
			t.Errorf("Search(%q) strength = %.2f, want at least moderate",
				ask.Query, got[0].Strength)
		}
	}

	// Recency: Carol's fresh terraform work outranks Victor's old volume.
	terraform := ix.Search("terraform", 5)
	carolRank, victorRank := rank(terraform, "carol@example.com"), rank(terraform, "victor@example.com")
	if carolRank != 1 || victorRank == 0 || victorRank < carolRank {
		t.Errorf("terraform ranks: carol %d victor %d, want carol first and victor present",
			carolRank, victorRank)
	}

	// The right channel surfaces, with the poster as a member.
	channels := ix.SearchChannels("billing retries", 3)
	if len(channels) == 0 || channels[0].Channel.Name != "payments" {
		t.Fatalf("channels top = %+v, want payments", channels)
	}
	if !slices.Contains(channels[0].Channel.Members, model.ID("angela@example.com")) {
		t.Errorf("payments members = %v, want angela", channels[0].Channel.Members)
	}

	// Feedback tunes but does not bury: votes for Victor keep Carol first.
	ix.SetFeedback([]feedback.Entry{
		{Query: "terraform", Person: "victor@example.com", Vote: feedback.Helpful},
		{Query: "terraform", Person: "victor@example.com", Vote: feedback.Helpful},
	})
	after := ix.Search("terraform", 5)
	if after[0].Person.ID != "carol@example.com" {
		t.Errorf("terraform top after votes = %s; capped feedback must not bury recency",
			after[0].Person.ID)
	}
	if rank(after, "victor@example.com") > victorRank {
		t.Errorf("victor fell after helpful votes: %d -> %d",
			victorRank, rank(after, "victor@example.com"))
	}
}

// rank returns the one-based rank of id in matches, or zero when absent.
func rank(matches []model.Match, id model.ID) int {
	for i, m := range matches {
		if m.Person.ID == id {
			return i + 1
		}
	}
	return 0
}

// first names the top match for an error message, or "none".
func first(matches []model.Match) string {
	if len(matches) == 0 {
		return "none"
	}
	return string(matches[0].Person.ID)
}
