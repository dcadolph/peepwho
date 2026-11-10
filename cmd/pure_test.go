package cmd

import (
	"fmt"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/fact"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// TestDateText pins the date rendering: a zero time is an empty string, not
// the epoch, and everything else is the plain date.
func TestDateText(t *testing.T) {
	t.Parallel()
	if got := dateText(time.Time{}); got != "" {
		t.Errorf("zero time = %q, want empty", got)
	}
	when := time.Date(2026, 8, 27, 15, 4, 5, 0, time.UTC)
	if got := dateText(when); got != "2026-08-27" {
		t.Errorf("dateText = %q, want 2026-08-27", got)
	}
}

// TestFactMentions covers the fact filter: terms match any of subject, object,
// or detail, case-insensitively, and no terms match nothing.
func TestFactMentions(t *testing.T) {
	t.Parallel()
	f := fact.Fact{Subject: "Ana", Object: "billing-retries", Detail: "Owns the Kafka DLQ"}
	tests := []struct {
		Terms []string
		Want  bool
	}{
		{[]string{"billing"}, true},   // Test 0: Object matches.
		{[]string{"kafka"}, true},     // Test 1: Detail matches, case folded.
		{[]string{"ana"}, true},       // Test 2: Subject matches.
		{[]string{"payments"}, false}, // Test 3: No field matches.
		{nil, false},                  // Test 4: No terms match nothing.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := factMentions(f, test.Terms); got != test.Want {
				t.Errorf("factMentions(%v) = %v, want %v", test.Terms, got, test.Want)
			}
		})
	}
}

// TestAttestPayload pins the sealed claim's shape: only critical topics count,
// the top list caps at ten, and the sole expert is the first expert named.
func TestAttestPayload(t *testing.T) {
	t.Parallel()
	report := make([]resolve.TopicRisk, 0, 13)
	for i := 0; i < 12; i++ {
		report = append(report, resolve.TopicRisk{
			Topic: fmt.Sprintf("area-%d", i), Level: "critical", BusFactor: 1,
			Experts: []resolve.RiskExpert{{Name: fmt.Sprintf("Expert %d", i)}},
		})
	}
	report = append(report, resolve.TopicRisk{Topic: "healthy", Level: "ok", BusFactor: 4})

	payload := attestPayload(report)
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want map", payload)
	}
	if m["critical"] != 12 || m["topics_scored"] != 13 {
		t.Errorf("critical=%v scored=%v, want 12 and 13", m["critical"], m["topics_scored"])
	}
	top, ok := m["top_critical"].([]any)
	if !ok || len(top) != 10 {
		t.Fatalf("top_critical len = %d, want capped at 10", len(top))
	}
	first, ok := top[0].(map[string]any)
	if !ok || first["sole_expert"] != "Expert 0" {
		t.Errorf("first sole_expert = %v, want Expert 0", first["sole_expert"])
	}
	if m["single_holder"] != 12 {
		t.Errorf("single_holder = %v, want 12: the payload must carry the plainest "+
			"reading of the finding, not only the severity level", m["single_holder"])
	}
}
