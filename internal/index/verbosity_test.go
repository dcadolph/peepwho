package index

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestBusyOwnerOutranksPassingMention verifies the person who owns a topic wins
// no matter how much else they say. A term's weight is capped, so an uncapped
// length normalizer would eventually divide an explicit topic tag below a
// single passing mention, which would mean the more work someone does, the
// worse they rank on the thing they own. That is the opposite of what whotoask
// is for, so it is pinned here at volumes far past anything realistic.
func TestBusyOwnerOutranksPassingMention(t *testing.T) {
	t.Parallel()
	for _, volume := range []int{1, 5, 10, 15, 30, 60, 120} {
		t.Run(fmt.Sprintf("owner talks %dx", volume), func(t *testing.T) {
			t.Parallel()
			recs := []connector.Record{
				{
					Kind: connector.KindPerson, Email: "owner@x.com", Name: "Owner",
					Topics: []string{"billing"}, Source: "org-csv",
				},
				{
					Kind: connector.KindPerson, Email: "bystander@x.com", Name: "Bystander",
					Source: "org-csv",
				},
				{
					Kind: connector.KindPerson, Email: "bystander@x.com",
					Text: "billing", Source: "slack",
				},
			}
			// A company of ordinary people sets the average profile length the
			// normalizer divides by. Without them the busy owner would be the
			// average and nothing would ever look verbose.
			chatter := func(who string, times int) {
				for i := range times {
					recs = append(recs, connector.Record{
						Kind: connector.KindPerson, Email: who, Source: "slack",
						Text: fmt.Sprintf("deploy pipeline dashboard latency %d %s",
							i, strings.Repeat("standup notes and release chatter ", 8)),
					})
				}
			}
			for p := range 30 {
				who := fmt.Sprintf("person%d@x.com", p)
				recs = append(recs, connector.Record{
					Kind: connector.KindPerson, Email: who, Name: fmt.Sprintf("Person %d", p),
					Source: "org-csv",
				})
				chatter(who, 1)
			}
			chatter("bystander@x.com", 1)
			// The owner is the busiest person in the company, and none of the
			// extra talk is about billing.
			chatter("owner@x.com", volume)

			ix := New()
			ix.Build(recs)
			ix.Canonicalize()

			hits := ix.Search("billing", 5)
			if len(hits) == 0 {
				t.Fatal("no result for the owner's own topic")
			}
			if got := string(hits[0].Person.ID); got != "owner@x.com" {
				var order []string
				for _, h := range hits {
					order = append(order, fmt.Sprintf("%s=%.4f", h.Person.ID, h.Score))
				}
				t.Errorf("top result = %s, want the topic owner: %s", got, strings.Join(order, " "))
			}
		})
	}
}
