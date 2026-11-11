package index

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
)

// newMarkedSlice returns a one-element list of the given type whose element
// carries mark, so two lists made with different marks never hold equal
// elements. An element kind it cannot mark fails the test, so a new kind of list
// on the record gets a deliberate case here.
func newMarkedSlice(t *testing.T, typ reflect.Type, mark string) reflect.Value {
	t.Helper()
	out := reflect.MakeSlice(typ, 1, 1)
	elem := out.Index(0)
	switch elem.Kind() {
	case reflect.String:
		elem.SetString(mark)
	case reflect.Struct:
		marked := false
		for i := range elem.NumField() {
			if f := elem.Field(i); f.Kind() == reflect.String && f.CanSet() {
				f.SetString(mark)
				marked = true
			}
		}
		if !marked {
			t.Fatalf("%s has no string field to mark: teach newMarkedSlice to fill it", typ.Elem())
		}
	default:
		t.Fatalf("%s holds %s elements: teach newMarkedSlice to fill it", typ, elem.Kind())
	}
	return out
}

// TestFoldRecordKeepsEverySliceField checks a fold carries every list a record
// holds, the held entries first and the new ones after. The lists are found by
// reflection, so one added to the record later fails here until the fold
// carries it. The weak and direct topics were dropped this way, and everybody
// already in the index stopped gaining them after the first run.
func TestFoldRecordKeepsEverySliceField(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[connector.Record]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			t.Parallel()
			held := newMarkedSlice(t, field.Type, "held")
			fresh := newMarkedSlice(t, field.Type, "fresh")
			want := reflect.AppendSlice(held, fresh).Interface()
			var base, add connector.Record
			reflect.ValueOf(&base).Elem().Field(i).Set(held)
			reflect.ValueOf(&add).Elem().Field(i).Set(fresh)

			got := reflect.ValueOf(foldRecord(base, add)).Field(i).Interface()
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s after a fold (-want +got):\n%s", field.Name, diff)
			}
		})
	}
}

// TestMergeIncrementalKeepsWeakAndDirectTopics checks a later run credits
// somebody already held with the words of their new work and the areas it was
// done in, exactly as one full read of the same work would. Decay is off so the
// comparison is of what accumulated alone.
func TestMergeIncrementalKeepsWeakAndDirectTopics(t *testing.T) {
	t.Parallel()
	const who = "ada@x.com"
	tests := []struct {
		Delta connector.Record
		Full  connector.Record
		Query string
	}{{ // Test 0: The words of new work reach somebody already held.
		Delta: connector.Record{
			Source: "git", Email: who, Name: "Ada", WeakTopics: []string{"autoscaler cooldown"},
		},
		Full: connector.Record{
			Source: "git", Email: who, Name: "Ada", Topics: []string{"payments"},
			DirectTopics: []string{"payments"},
			WeakTopics:   []string{"webhook retries", "autoscaler cooldown"},
		},
		Query: "autoscaler cooldown",
	}, { // Test 1: More work inside an area already held adds to it.
		Delta: connector.Record{
			Source: "git", Email: who, Name: "Ada", Topics: []string{"payments"},
			DirectTopics: []string{"payments"},
		},
		Full: connector.Record{
			Source: "git", Email: who, Name: "Ada", Topics: []string{"payments", "payments"},
			DirectTopics: []string{"payments", "payments"},
			WeakTopics:   []string{"webhook retries"},
		},
		Query: "payments",
	}, { // Test 2: Work inside a new area is credited to that area.
		Delta: connector.Record{
			Source: "git", Email: who, Name: "Ada", Topics: []string{"ledger"},
			DirectTopics: []string{"ledger"},
		},
		Full: connector.Record{
			Source: "git", Email: who, Name: "Ada", Topics: []string{"payments", "ledger"},
			DirectTopics: []string{"payments", "ledger"},
			WeakTopics:   []string{"webhook retries"},
		},
		Query: "ledger",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			full, inc := New(), New()
			full.SetHalfLife(-1)
			inc.SetHalfLife(-1)
			full.Build([]connector.Record{test.Full})
			inc.Build([]connector.Record{{
				Source: "git", Email: who, Name: "Ada", Topics: []string{"payments"},
				DirectTopics: []string{"payments"}, WeakTopics: []string{"webhook retries"},
			}})
			inc.MergeIncremental([]connector.Record{test.Delta})

			want := rows(full.Search(test.Query, 5))
			if len(want) == 0 {
				t.Fatalf("a full read finds nobody for %q, so the case proves nothing", test.Query)
			}
			if diff := cmp.Diff(want, rows(inc.Search(test.Query, 5))); diff != "" {
				t.Errorf("query %q (-full +incremental):\n%s", test.Query, diff)
			}
			wantPerson, gotPerson := full.Graph.People[who], inc.Graph.People[who]
			if wantPerson == nil || gotPerson == nil {
				t.Fatalf("person missing: full %v, incremental %v", wantPerson, gotPerson)
			}
			if diff := cmp.Diff(wantPerson, gotPerson, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("person (-full +incremental):\n%s", diff)
			}
		})
	}
}
