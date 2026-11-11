package resolve

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/model"
)

func TestWithoutChatter(t *testing.T) {
	t.Parallel()
	// chatterItem stands in for a match: a name, whether it is declared, and
	// whether it rests only on tie reach.
	type chatterItem struct {
		Name     string
		Declared bool
		Reach    bool
	}
	tests := []struct {
		WantResult []chatterItem
		In         []chatterItem
	}{{ // Test 0: A declared leader drops every undeclared match after it.
		In: []chatterItem{
			{"owner", true, false}, {"chatter", false, false}, {"backup", true, false}, {"passerby", false, false},
		},
		WantResult: []chatterItem{{"owner", true, false}, {"backup", true, false}},
	}, { // Test 1: An undeclared leader keeps everything, declared matches below included.
		In: []chatterItem{
			{"talker", false, false}, {"team", true, false}, {"other", false, false},
		},
		WantResult: []chatterItem{{"talker", false, false}, {"team", true, false}, {"other", false, false}},
	}, { // Test 2: Nothing declared keeps every match.
		In:         []chatterItem{{"a", false, false}, {"b", false, false}},
		WantResult: []chatterItem{{"a", false, false}, {"b", false, false}},
	}, { // Test 3: Everything declared keeps every match.
		In:         []chatterItem{{"a", true, false}, {"b", true, false}},
		WantResult: []chatterItem{{"a", true, false}, {"b", true, false}},
	}, { // Test 4: An empty list stays empty.
		In: nil, WantResult: nil,
	}, { // Test 5: A direct leader drops tie reach as well as chatter.
		In: []chatterItem{
			{"owner", true, false}, {"neighbor", true, true}, {"talker", false, false}, {"backup", true, false},
		},
		WantResult: []chatterItem{{"owner", true, false}, {"backup", true, false}},
	}, { // Test 6: A reach leader keeps reach and drops chatter.
		In: []chatterItem{
			{"neighbor", true, true}, {"talker", false, false}, {"other", true, true},
		},
		WantResult: []chatterItem{{"neighbor", true, true}, {"other", true, true}},
	}, { // Test 7: A reach leader keeps a direct match below it.
		In:         []chatterItem{{"neighbor", true, true}, {"owner", true, false}},
		WantResult: []chatterItem{{"neighbor", true, true}, {"owner", true, false}},
	}, { // Test 8: A chatter leader keeps reach and direct matches below it.
		In: []chatterItem{
			{"talker", false, false}, {"neighbor", true, true}, {"owner", true, false},
		},
		WantResult: []chatterItem{{"talker", false, false}, {"neighbor", true, true}, {"owner", true, false}},
	}, { // Test 9: Reach without a declared field is chatter.
		In:         []chatterItem{{"owner", true, false}, {"echo", false, true}},
		WantResult: []chatterItem{{"owner", true, false}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			in := append([]chatterItem(nil), test.In...)
			got := withoutChatter(in, func(c chatterItem) int { return matchClass(c.Declared, c.Reach) })
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// classified is a test match's name and class: direct, reach, or chatter.
type classified struct {
	// Name identifies the match.
	Name string
	// Class is "direct", "reach", or "chatter".
	Class string
}

// asPeople builds person matches for the named classes.
func asPeople(items ...classified) []model.Match {
	out := make([]model.Match, 0, len(items))
	for _, c := range items {
		out = append(out, model.Match{
			Person:   &model.Person{ID: model.ID(c.Name), Name: c.Name},
			Declared: c.Class != "chatter",
			Reach:    c.Class == "reach",
		})
	}
	return out
}

// asChannels builds channel matches for the named classes.
func asChannels(items ...classified) []model.ChannelMatch {
	out := make([]model.ChannelMatch, 0, len(items))
	for _, c := range items {
		out = append(out, model.ChannelMatch{
			Channel:  &model.Channel{ID: model.ID(c.Name), Name: c.Name},
			Declared: c.Class != "chatter",
			Reach:    c.Class == "reach",
		})
	}
	return out
}

// personNames lists the matched people's names in order.
func personNames(ms []model.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Person.Name)
	}
	return out
}

// channelNames lists the matched channels' names in order.
func channelNames(ms []model.ChannelMatch) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Channel.Name)
	}
	return out
}

func TestTrimChatter(t *testing.T) {
	t.Parallel()
	// mixed holds one of each class after a leader, in rank order.
	mixed := func(leader string) []classified {
		return []classified{
			{"lead", leader}, {"chat", "chatter"}, {"tie", "reach"}, {"own", "direct"}, {"tie2", "reach"},
		}
	}
	tests := []struct {
		WantNames []string
		In        []classified
		Limit     int
	}{{ // Test 0: A direct leader keeps only direct matches.
		In: mixed("direct"), Limit: 0, WantNames: []string{"lead", "own"},
	}, { // Test 1: A reach leader keeps reach and direct, drops chatter.
		In: mixed("reach"), Limit: 0, WantNames: []string{"lead", "tie", "own", "tie2"},
	}, { // Test 2: A chatter leader keeps everything.
		In: mixed("chatter"), Limit: 0, WantNames: []string{"lead", "chat", "tie", "own", "tie2"},
	}, { // Test 3: The filter runs before the cut, so dropped matches free their slots.
		In: mixed("direct"), Limit: 2, WantNames: []string{"lead", "own"},
	}, { // Test 4: A reach leader at limit 2 fills from below the dropped chatter.
		In: mixed("reach"), Limit: 2, WantNames: []string{"lead", "tie"},
	}, { // Test 5: The cut still holds when more pass than the limit.
		In: mixed("chatter"), Limit: 3, WantNames: []string{"lead", "chat", "tie"},
	}, { // Test 6: Empty lists stay empty.
		In: nil, Limit: 5, WantNames: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			// Each list is fed the same shape alone, the other two empty, so a
			// list judged against the wrong leader would show.
			people, _, _ := trimChatter(asPeople(test.In...), nil, nil, test.Limit)
			if diff := cmp.Diff(test.WantNames, personNames(people), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("people mismatch (-want +got):\n%s", diff)
			}
			_, departed, _ := trimChatter(nil, asPeople(test.In...), nil, test.Limit)
			if diff := cmp.Diff(test.WantNames, personNames(departed), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("departed mismatch (-want +got):\n%s", diff)
			}
			_, _, channels := trimChatter(nil, nil, asChannels(test.In...), test.Limit)
			if diff := cmp.Diff(test.WantNames, channelNames(channels), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("channels mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTrimChatterListsJudgedApart(t *testing.T) {
	t.Parallel()
	// A direct active leader must not trim the departed, whose own leader is
	// reach, and neither may touch the channels, led by chatter.
	people, departed, channels := trimChatter(
		asPeople(classified{"owner", "direct"}, classified{"tie", "reach"}),
		asPeople(classified{"gone-tie", "reach"}, classified{"gone-talk", "chatter"}, classified{"gone-tie2", "reach"}),
		asChannels(classified{"talk", "chatter"}, classified{"own", "direct"}),
		5)
	if diff := cmp.Diff([]string{"owner"}, personNames(people)); diff != "" {
		t.Errorf("people mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"gone-tie", "gone-tie2"}, personNames(departed)); diff != "" {
		t.Errorf("departed mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"talk", "own"}, channelNames(channels)); diff != "" {
		t.Errorf("channels mismatch (-want +got):\n%s", diff)
	}
}
