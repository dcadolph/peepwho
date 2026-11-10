package index

import (
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestAnswerSaysWhenAnExpertHasMovedOn checks whotoask admits that the person it
// names has stopped working on the subject. Knowing something best is not the
// same as still being in it: on a real repository the leading expert of two
// subjects in five had already stopped touching them, and was less than half as
// likely to still hold the subject six months on. Sending somebody to ask them
// without saying so is how an answer goes quietly stale.
func TestAnswerSaysWhenAnExpertHasMovedOn(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Name: "Moved On", Email: "gone@x.com",
		Topics: []string{"zigbee", "zigbee"}, Source: "git",
		// Still active, just not here.
		RecentTopics: []string{"something-else"},
	}, {
		Kind: connector.KindPerson, Name: "Still In It", Email: "here@x.com",
		Topics: []string{"mqtt", "mqtt"}, RecentTopics: []string{"mqtt", "mqtt"}, Source: "git",
	}})
	ix.Canonicalize()

	find := func(query, who string) string {
		for _, m := range ix.Search(query, 5) {
			if m.Person != nil && m.Person.Name == who {
				return strings.Join(m.Reasons, " ")
			}
		}
		t.Fatalf("%s was not returned for %q", who, query)
		return ""
	}
	if got := find("zigbee", "Moved On"); !strings.Contains(got, "not lately") {
		t.Errorf("reasons = %q, want it to say they have stopped working on it", got)
	}
	if got := find("mqtt", "Still In It"); strings.Contains(got, "not lately") {
		t.Errorf("reasons = %q, want no such note for somebody still in it", got)
	}
}

// TestNoRecentRecordClaimsNothing checks a source that never said what was
// recent does not make everybody look like they have moved on. Most sources
// cannot say, and silence is not evidence of absence.
func TestNoRecentRecordClaimsNothing(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Name: "Unknown", Email: "u@x.com",
		Topics: []string{"kafka", "kafka"}, Source: "org-csv",
	}})
	ix.Canonicalize()
	for _, m := range ix.Search("kafka", 5) {
		if strings.Contains(strings.Join(m.Reasons, " "), "not lately") {
			t.Errorf("reasons = %v, want no claim when the source never said", m.Reasons)
		}
	}
}
