package resolve

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
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
