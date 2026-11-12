package index

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
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

// channelView is the part of a channel match a ranking decides, with the
// members reduced to their ids so two searches compare by value.
type channelView struct {
	// Name is the channel's name.
	Name string
	// Score is the ranking score.
	Score float64
	// Strength is the strength the answer shows.
	Strength float64
	// Declared reports a match on a stated subject.
	Declared bool
	// Reach reports a match only through a tied subject.
	Reach bool
	// Reasons are the reason lines.
	Reasons []string
	// Members are the ids of the listed members, best first.
	Members []model.ID
}

// channelViews reduces matches to their views, in order.
func channelViews(ms []model.ChannelMatch) []channelView {
	out := make([]channelView, 0, len(ms))
	for _, m := range ms {
		v := channelView{
			Name: m.Channel.Name, Score: m.Score, Strength: m.Strength,
			Declared: m.Declared, Reach: m.Reach, Reasons: m.Reasons,
		}
		for _, p := range m.TopMembers {
			v.Members = append(v.Members, p.ID)
		}
		out = append(out, v)
	}
	return out
}

// TestRankChannelsThenFillMatchesSearchChannels checks the split under the
// channel search. RankChannels ranks exactly as SearchChannels does and lists
// no members, and FillTopMembers on its matches gives what SearchChannels
// gives, so a caller that filters between the two shows the same members on
// every channel it keeps.
func TestRankChannelsThenFillMatchesSearchChannels(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Topics: []string{"kafka"}},
		{Name: "Ann Poe", Email: "ann@x.com", Topics: []string{"kafka", "retries"}},
		{Kind: connector.KindChannel, Name: "billing", Title: "retries and dunning",
			Members: []string{"jane@x.com", "bob@x.com", "ann@x.com"}},
		{Kind: connector.KindChannel, Name: "streams", Topics: []string{"kafka"},
			Members: []string{"bob@x.com", "ann@x.com"}},
		{Kind: connector.KindChannel, Name: "ops", Text: "kafka retries pager",
			Members: []string{"jane@x.com", "bob@x.com"}},
	})

	tests := []struct {
		Query       string
		Limit       int
		WantMembers bool
	}{{ // Test 0: One word, every channel.
		Query: "retries", Limit: 0, WantMembers: true,
	}, { // Test 1: Two words, every channel.
		Query: "kafka retries", Limit: 0, WantMembers: true,
	}, { // Test 2: The limit cuts before the members are filled.
		Query: "kafka", Limit: 1, WantMembers: true,
	}, { // Test 3: A query with no words gives nothing either way.
		Query: "   ", Limit: 0, WantMembers: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			want := channelViews(ix.SearchChannels(test.Query, test.Limit))
			var listed bool
			wantRanked := make([]channelView, len(want))
			for i, v := range want {
				listed = listed || len(v.Members) > 0
				v.Members = nil
				wantRanked[i] = v
			}
			if listed != test.WantMembers {
				t.Fatalf("SearchChannels listed members: %v, want %v", listed, test.WantMembers)
			}
			ranked := ix.RankChannels(test.Query, test.Limit)
			gotRanked := channelViews(ranked)
			if diff := cmp.Diff(wantRanked, gotRanked, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ranking mismatch (-search without members +rank):\n%s", diff)
			}
			ix.FillTopMembers(test.Query, ranked)
			if diff := cmp.Diff(want, channelViews(ranked), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("filled ranking mismatch (-search +rank and fill):\n%s", diff)
			}
		})
	}
}

// TestHitReasonNamesWhatACorrectionWasReadAs checks the reason line for one
// matched word. Before this, a corrected word that landed on a passing mention
// printed the word as typed, so "spark" answered by a channel about parking
// read as if the channel had talked about Spark.
func TestHitReasonNamesWhatACorrectionWasReadAs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Term       string
		Field      string
		Found      string
		Hit        termHit
		Asked      map[string]string
		WantResult string
	}{{ // Test 0: A corrected mention names the stem it resolved to.
		Term: "spark", Field: "mention", Hit: termHit{key: "park", penalty: fuzzyPenalty},
		WantResult: `park (mention, read for "spark")`,
	}, { // Test 1: A corrected field hit names the word the field holds.
		Term: "kafkaa", Field: "topic", Found: "kafka",
		Hit:        termHit{key: "kafka", penalty: fuzzyPenalty},
		WantResult: `kafka (topic, read for "kafkaa")`,
	}, { // Test 2: An exact mention prints the word as typed.
		Term: "parking", Field: "mention", Hit: termHit{key: "park", penalty: 1},
		WantResult: "parking (mention)",
	}, { // Test 3: An exact field hit prints the word as typed.
		Term: "kafka", Field: "name", Found: "kafka", Hit: termHit{key: "kafka", penalty: 1},
		WantResult: "kafka (name)",
	}, { // Test 4: A synonym says what it stood in for and is no correction.
		Term: "vacation", Field: "topic", Found: "vacation",
		Hit:        termHit{key: "vacat", penalty: expansionPenalty},
		Asked:      map[string]string{"vacation": "time off"},
		WantResult: `vacation (topic) for "time off"`,
	}, { // Test 5: A correction that reads as the word typed has nothing to name.
		Term: "parking", Field: "mention", Hit: termHit{key: "parking", penalty: fuzzyPenalty},
		WantResult: "parking (mention, fuzzy)",
	}, { // Test 6: Two edits on a mention still name the stem.
		Term: "elastcsearh", Field: "mention",
		Hit:        termHit{key: "elasticsearch", penalty: fuzzyPenalty * fuzzyPenalty},
		WantResult: `elasticsearch (mention, read for "elastcsearh")`,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := hitReason(test.Term, test.Field, test.Found, test.Hit, test.Asked)
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("reason mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestReasonsNameWhatAMentionWasReadAs checks the whole path for people and
// channels: a misspelled question answered through a passing mention says in
// its reason which word the answer was read for.
func TestReasonsNameWhatAMentionWasReadAs(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "owner@corp.com", Name: "Owner",
		Topics: []string{"kafka"}, Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "mention@corp.com", Name: "Mention",
		Text: "the parking garage is full again", Source: "slack",
	}, {
		Kind: connector.KindChannel, Name: "office", Title: "office chatter",
		Text: "parking passes are at the front desk", Source: "slack",
	}, {
		Kind: connector.KindChannel, Name: "kafka", Title: "kafka questions", Source: "slack",
	}})

	tests := []struct {
		Query       string
		Person      string
		Channel     string
		WantReasons []string
	}{{ // Test 0: A person who only mentioned the corrected word.
		Query: "spark", Person: "mention@corp.com",
		WantReasons: []string{`park (mention, read for "spark")`},
	}, { // Test 1: A channel that only mentioned the corrected word.
		Query: "spark", Channel: "office",
		WantReasons: []string{`park (mention, read for "spark")`},
	}, { // Test 2: A person holding the corrected word as a topic.
		Query: "kafkaa", Person: "owner@corp.com",
		WantReasons: []string{`kafka (topic, read for "kafkaa")`},
	}, { // Test 3: A channel holding the corrected word as its topic.
		Query: "kafkaa", Channel: "kafka",
		WantReasons: []string{`kafka (topic, read for "kafkaa")`},
	}, { // Test 4: The exact word on a mention prints as typed.
		Query: "parking", Person: "mention@corp.com",
		WantReasons: []string{"parking (mention)"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var got []string
			if test.Person != "" {
				for _, m := range ix.Search(test.Query, 0) {
					if m.Person.Email == test.Person {
						got = m.Reasons
					}
				}
			} else {
				for _, m := range ix.SearchChannels(test.Query, 0) {
					if m.Channel.Name == test.Channel {
						got = m.Reasons
					}
				}
			}
			if diff := cmp.Diff(test.WantReasons, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("reasons mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
