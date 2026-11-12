package resolve

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

func TestStrengthLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         float64
		WantResult string
	}{{ // Test 0: Zero means unknown.
		In: 0, WantResult: "",
	}, { // Test 1: Full strength is strong.
		In: 1, WantResult: "strong",
	}, { // Test 2: The strong floor is inclusive.
		In: 0.75, WantResult: "strong",
	}, { // Test 3: Between floors is moderate.
		In: 0.5, WantResult: "moderate",
	}, { // Test 4: The moderate floor is inclusive.
		In: 0.45, WantResult: "moderate",
	}, { // Test 5: Below the moderate floor is weak.
		In: 0.2, WantResult: "weak",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := StrengthLabel(test.In); got != test.WantResult {
				t.Errorf("StrengthLabel(%.2f) = %q, want %q", test.In, got, test.WantResult)
			}
		})
	}
}

// TestReachable verifies a person counts as reachable only through a link the
// Mac app opens, and never through a Slack link once deactivated.
func TestReachable(t *testing.T) {
	t.Parallel()
	slack := "slack://user?team=T0&id=U1"
	tests := []struct {
		Links       []string
		Deactivated bool
		WantResult  bool
	}{{  // Test 0: No links.
	}, { // Test 1: An active person's Slack link.
		Links: []string{slack}, WantResult: true,
	}, { // Test 2: A deactivated person's Slack link.
		Links: []string{slack}, Deactivated: true,
	}, { // Test 3: The scheme's case does not matter.
		Links: []string{"SLACK://user?team=T0&id=U1"}, WantResult: true,
	}, { // Test 4: A deactivated person's Slack link in any case.
		Links: []string{"Slack://user?team=T0&id=U1"}, Deactivated: true,
	}, { // Test 5: A deactivated person's web link still reaches somebody.
		Links: []string{slack, "https://corp.example.com/u/1"}, Deactivated: true, WantResult: true,
	}, { // Test 6: Mail.
		Links: []string{"mailto:jane@corp.com"}, WantResult: true,
	}, { // Test 7: Plain http.
		Links: []string{"http://corp.example.com/u/1"}, WantResult: true,
	}, { // Test 8: Schemes the app does not open.
		Links: []string{"ftp://corp.example.com/u/1", "file:///etc/hosts"},
	}, { // Test 9: Links that do not parse.
		Links: []string{"://bad", "%zz"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := reachable(test.Links, test.Deactivated); got != test.WantResult {
				t.Errorf("reachable(%q, %v) = %v", test.Links, test.Deactivated, got)
			}
		})
	}
}

// TestPersonViewReachable verifies the answer JSON marks a person with a Slack
// link reachable and leaves the key out for a person with no link and for a
// deactivated person whose only link is Slack.
func TestPersonViewReachable(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Name: "Jane Roe",
			Email: "jane@corp.com", Link: "slack://user?team=T0&id=U1", Account: model.AccountActive},
		{Kind: connector.KindPerson, Source: "git", Name: "Ada Lane", Email: "ada@corp.com"},
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U2", Name: "Lena Gone",
			Email: "lena@corp.com", Link: "slack://user?team=T0&id=U2", Account: model.AccountDeactivated},
	})
	ix.Canonicalize()
	match := func(id string) model.Match {
		p := ix.Graph.People[model.ID(id)]
		if p == nil {
			t.Fatalf("fixture: %s not found", id)
		}
		return model.Match{Person: p}
	}
	ans := Answer{
		ix:       ix,
		People:   []model.Match{match("jane@corp.com"), match("ada@corp.com")},
		Departed: []model.Match{match("lena@corp.com")},
	}
	raw, err := json.Marshal(ans.View("billing"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		People   []map[string]any `json:"people"`
		Departed []map[string]any `json:"departed"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.People) != 2 || len(got.Departed) != 1 {
		t.Fatalf("want 2 people and 1 departed: %s", raw)
	}
	if got.People[0]["reachable"] != true {
		t.Errorf("Jane, with a Slack link, is not reachable: %v", got.People[0])
	}
	if _, ok := got.People[1]["reachable"]; ok {
		t.Errorf("Ada, with no link, carries the key: %v", got.People[1])
	}
	if _, ok := got.Departed[0]["reachable"]; ok || got.Departed[0]["deactivated"] != true {
		t.Errorf("Lena, gone with only a Slack link, is marked wrong: %v", got.Departed[0])
	}
}

// TestProfileViewLinks verifies the profile JSON lists a person's deep links
// and leaves the field out for a person with none.
func TestProfileViewLinks(t *testing.T) {
	t.Parallel()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Name: "Jane Roe",
			Email: "jane@corp.com", Link: "slack://user?team=T0&id=U1"},
		{Kind: connector.KindPerson, Source: "git", Name: "Ada Lane", Email: "ada@corp.com"},
	})
	ix.Canonicalize()
	tests := []struct {
		ID        string
		WantLinks []string
	}{{ // Test 0: Slack person.
		ID: "jane@corp.com", WantLinks: []string{"slack://user?team=T0&id=U1"},
	}, { // Test 1: Person with no Slack identity.
		ID: "ada@corp.com",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			profile, ok := ix.Profile(model.ID(test.ID))
			if !ok {
				t.Fatalf("profile %s not found", test.ID)
			}
			raw, err := json.Marshal(ProfileView(ix, profile))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				Links []string `json:"links"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if diff := cmp.Diff(test.WantLinks, got.Links); diff != "" {
				t.Errorf("links mismatch (-want +got):\n%s", diff)
			}
			if test.WantLinks == nil && strings.Contains(string(raw), `"links"`) {
				t.Errorf("profile with no links still has the field: %s", raw)
			}
		})
	}
}
