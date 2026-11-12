package index

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/model"
)

// TestTopMembersNamesOnlyPeopleWorthAsking checks a channel's member list holds
// only people the asker could be sent to: active colleagues the question
// reaches. Before this, a departed person, a team handle, and a member who
// scored nothing all filled the open slots, so an answer named a departed
// expert and an unrelated colleague as the people active in a channel.
func TestTopMembersNamesOnlyPeopleWorthAsking(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Graph.People = map[model.ID]*model.Person{
		"ann@corp.com":  {ID: "ann@corp.com", Name: "Ann"},
		"bo@corp.com":   {ID: "bo@corp.com", Name: "Bo"},
		"gone@corp.com": {ID: "gone@corp.com", Name: "Gone", Account: model.AccountDeactivated},
		"corp/payments": {ID: "corp/payments", Name: "Payments"},
		"idle@corp.com": {ID: "idle@corp.com", Name: "Idle"},
	}
	scores := map[model.ID]float64{
		"ann@corp.com": 2, "bo@corp.com": 1, "gone@corp.com": 5, "corp/payments": 4,
	}

	tests := []struct {
		Members []model.ID
		Limit   int
		WantIDs []model.ID
	}{{ // Test 0: Active members the question reaches, best first.
		Members: []model.ID{"bo@corp.com", "ann@corp.com"}, Limit: 3,
		WantIDs: []model.ID{"ann@corp.com", "bo@corp.com"},
	}, { // Test 1: A departed member is left out, whatever the score.
		Members: []model.ID{"gone@corp.com", "ann@corp.com"}, Limit: 3,
		WantIDs: []model.ID{"ann@corp.com"},
	}, { // Test 2: A team handle is left out.
		Members: []model.ID{"corp/payments", "bo@corp.com"}, Limit: 3,
		WantIDs: []model.ID{"bo@corp.com"},
	}, { // Test 3: A member the question does not reach is left out.
		Members: []model.ID{"idle@corp.com", "ann@corp.com"}, Limit: 3,
		WantIDs: []model.ID{"ann@corp.com"},
	}, { // Test 4: Nobody worth asking leaves the list empty.
		Members: []model.ID{"gone@corp.com", "corp/payments", "idle@corp.com"}, Limit: 3,
		WantIDs: nil,
	}, { // Test 5: The limit still cuts the list.
		Members: []model.ID{"bo@corp.com", "ann@corp.com"}, Limit: 1,
		WantIDs: []model.ID{"ann@corp.com"},
	}, { // Test 6: A member the graph does not hold is left out.
		Members: []model.ID{"ghost@corp.com", "bo@corp.com"}, Limit: 3,
		WantIDs: []model.ID{"bo@corp.com"},
	}, { // Test 7: A limit of zero lists nobody.
		Members: []model.ID{"bo@corp.com", "ann@corp.com"}, Limit: 0,
		WantIDs: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			ch := &model.Channel{ID: "billing", Name: "billing", Members: test.Members}
			var got []model.ID
			for _, p := range ix.topMembers(ch, scores, test.Limit) {
				got = append(got, p.ID)
			}
			if diff := cmp.Diff(test.WantIDs, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("members mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
