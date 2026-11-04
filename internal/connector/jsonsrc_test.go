package connector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestJSONFetch(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		In       string
		WantRecs []Record
		WantErr  bool
	}{{ // Test 0: Minimal person; kind and source default.
		In:       `[{"name":"Ada Lovelace","email":"ada@x.io","topics":["ml"]}]`,
		WantRecs: []Record{{Kind: KindPerson, Name: "Ada Lovelace", Email: "ada@x.io", Topics: []string{"ml"}, Source: "json"}},
	}, { // Test 1: Channel with members.
		In:       `[{"kind":"channel","name":"billing","members":["u1","u2"]}]`,
		WantRecs: []Record{{Kind: KindChannel, Name: "billing", Members: []string{"u1", "u2"}, Source: "json"}},
	}, { // Test 2: Explicit source overrides the default; time and weight carried.
		In:       fmt.Sprintf(`[{"id":"p1","name":"Kim","source":"catalog","weight":2.5,"time":%q}]`, when.Format(time.RFC3339)),
		WantRecs: []Record{{Kind: KindPerson, PersonID: "p1", Name: "Kim", Source: "catalog", Weight: 2.5, Time: when}},
	}, { // Test 3: Empty array yields no records.
		In:       `[]`,
		WantRecs: nil,
	}, { // Test 4: Unknown kind is an error.
		In:      `[{"name":"x","kind":"robot"}]`,
		WantErr: true,
	}, { // Test 5: Malformed JSON is an error.
		In:      `{not json`,
		WantErr: true,
	}, { // Test 6: Trailing garbage after the array is an error, not ignored.
		In:      `[{"name":"x"}] trailing`,
		WantErr: true,
	}, { // Test 7: A second value after the array is an error; only the first
		// used to be read, and the import reported success over a half-read file.
		In:      `[{"name":"x"}] [{"name":"y"}]`,
		WantErr: true,
	}, { // Test 8: Trailing whitespace is fine.
		In:       "[{\"name\":\"Ada\"}]\n  \n",
		WantRecs: []Record{{Kind: KindPerson, Name: "Ada", Source: "json"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, err := NewJSON(strings.NewReader(test.In), "json").Fetch(context.Background())
			if test.WantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
					return
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(test.WantRecs, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
