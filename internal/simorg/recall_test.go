package simorg

import (
	"context"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/recall"
)

// TestRecallPipeline runs the whole recall path over the simulated workspace:
// conversations are collected from Slack, kept with their content, and found
// again by the person who took part in them.
func TestRecallPipeline(t *testing.T) {
	t.Parallel()
	ix, err := BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	store, err := BuildEpisodes(ix)
	if err != nil {
		t.Fatalf("BuildEpisodes: %v", err)
	}
	if store.Len() == 0 {
		t.Fatal("no conversations were remembered")
	}

	res := recall.New(store, ix)
	ans := res.Resolve(context.Background(), recall.Query{
		Text: "certificate renewal failing", Person: "angela@example.com", Explain: true,
	})
	if len(ans.Episodes) == 0 {
		t.Fatalf("no conversation found; scope = %+v", ans.Scope)
	}
	ep := ans.Episodes[0]
	if ep.Place != "infra" {
		t.Errorf("place = %q, want infra", ep.Place)
	}
	if !strings.HasPrefix(ep.Permalink, "https://corp.slack.com/archives/C3/p") {
		t.Errorf("permalink = %q, want a link into the workspace", ep.Permalink)
	}

	var named []string
	for _, p := range ep.People {
		named = append(named, p.Name)
	}
	if len(named) == 0 {
		t.Fatal("nobody was named as having helped")
	}
	for _, want := range []string{"Carol Lee", "Grace Kim"} {
		if !strings.Contains(strings.Join(named, ","), want) {
			t.Errorf("people = %v, want %s among them", named, want)
		}
	}
	for _, p := range ep.People {
		if p.Name == "Angela Malone" {
			t.Error("the asker was listed as someone who helped her")
		}
	}

	if ep.Solution == nil {
		t.Fatal("the kept conversation was not returned")
	}
	joined := ""
	for _, n := range ep.Solution.Notes {
		joined += n.Author + ": " + n.Text + "\n"
	}
	if !strings.Contains(joined, "certbot") || !strings.Contains(joined, "wildcard") {
		t.Errorf("conversation = %q, want the messages that solved it", joined)
	}

	// Someone who was not in the conversation must not reach it.
	other := res.Resolve(context.Background(), recall.Query{
		Text: "certificate renewal failing", Person: "dan@example.com", Explain: true,
	})
	for _, e := range other.Episodes {
		if e.Place == "infra" && e.Solution != nil {
			t.Error("a conversation reached someone who was not in it")
		}
	}
}

// TestRecallSpansSources verifies past work is found wherever it happened: a
// Slack thread, a merged change, a resolved ticket, and a resolved incident
// all become conversations recall can point back at.
func TestRecallSpansSources(t *testing.T) {
	t.Parallel()
	store, err := BuildEpisodes(nil)
	if err != nil {
		t.Fatalf("BuildEpisodes: %v", err)
	}
	kinds := make(map[string]bool)
	sources := make(map[string]bool)
	for _, ep := range store.All() {
		kinds[string(ep.Kind)] = true
		sources[ep.Source] = true
		if ep.Permalink == "" {
			t.Errorf("episode %q has no link back to it", ep.ID)
		}
		if len(ep.Participants) == 0 {
			t.Errorf("episode %q names nobody", ep.ID)
		}
	}
	for _, want := range []string{"slack", "github", "jira", "pagerduty"} {
		if !sources[want] {
			t.Errorf("no conversation came from %s; sources = %v", want, sources)
		}
	}
	for _, want := range []string{"thread", "change", "issue", "incident"} {
		if !kinds[want] {
			t.Errorf("no %s was recorded; kinds = %v", want, kinds)
		}
	}
}

// TestRecallFindsCrossSourceWork verifies each source's record is findable by
// the person who took part, with the link that opens it.
func TestRecallFindsCrossSourceWork(t *testing.T) {
	t.Parallel()
	ix, err := BuildIndex(t.TempDir())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	store, err := BuildEpisodes(ix)
	if err != nil {
		t.Fatalf("BuildEpisodes: %v", err)
	}
	res := recall.New(store, ix)

	tests := []struct {
		Name       string
		Query      string
		Person     string
		WantSource string
		WantLink   string
	}{
		{
			Name: "merged change", Query: "billing retry backoff",
			Person: "angela@example.com", WantSource: "github",
			WantLink: "https://github.com/corp/billing-service/pull/412",
		},
		{
			Name: "resolved ticket", Query: "kafka consumer lag",
			Person: "bob@example.com", WantSource: "jira",
			WantLink: "/browse/DAT-1",
		},
		{
			Name: "resolved incident", Query: "billing api latency",
			Person: "angela@example.com", WantSource: "pagerduty",
			WantLink: "https://corp.pagerduty.com/incidents/PINC1",
		},
	}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ans := res.Resolve(context.Background(), recall.Query{
				Text: test.Query, Person: model.ID(test.Person), Limit: 5,
			})
			found := false
			for _, ep := range ans.Episodes {
				if ep.Source == test.WantSource && strings.Contains(ep.Permalink, test.WantLink) {
					found = true
				}
			}
			if !found {
				t.Errorf("%q for %s did not return the %s record; got %+v",
					test.Query, test.Person, test.WantSource, ans.Episodes)
			}
		})
	}
}
