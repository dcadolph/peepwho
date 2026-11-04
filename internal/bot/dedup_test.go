package bot

import (
	"fmt"
	"testing"
)

// TestRedeliveredEventIsAnsweredOnce checks a repeat of an event peepwho has
// already answered is dropped. Slack redelivers when an acknowledgment is slow
// or lost, and without this the bot answers the same question twice in the
// channel it was asked in.
func TestRedeliveredEventIsAnsweredOnce(t *testing.T) {
	t.Parallel()
	var seen recentEvents
	if !seen.first("Ev123") {
		t.Fatal("the first delivery of an event was treated as a repeat")
	}
	if seen.first("Ev123") {
		t.Error("a redelivery was treated as a new event, so it would be answered twice")
	}
	if !seen.first("Ev456") {
		t.Error("a different event was treated as a repeat")
	}
}

// TestEventWithNoIdIsAnswered checks a frame carrying no id is still answered.
// It cannot be told apart from any other, and staying silent on a real question
// is worse than the rare double answer.
func TestEventWithNoIdIsAnswered(t *testing.T) {
	t.Parallel()
	var seen recentEvents
	for range 3 {
		if !seen.first("") {
			t.Fatal("an event with no id was dropped")
		}
	}
}

// TestRememberedEventsStayBounded checks a long-running session does not keep
// an id for every message it has ever seen. Slack stops retrying long before
// the window fills, so forgetting the oldest costs nothing.
func TestRememberedEventsStayBounded(t *testing.T) {
	t.Parallel()
	var seen recentEvents
	for i := range maxRememberedEvents + 500 {
		seen.first(fmt.Sprintf("Ev%d", i))
	}
	if len(seen.seen) > maxRememberedEvents {
		t.Errorf("remembering %d events, want at most %d", len(seen.seen), maxRememberedEvents)
	}
	if len(seen.order) != len(seen.seen) {
		t.Errorf("the order list holds %d and the set %d; they must not drift apart",
			len(seen.order), len(seen.seen))
	}
	// The most recent is still known, the oldest has been let go.
	if seen.first(fmt.Sprintf("Ev%d", maxRememberedEvents+499)) {
		t.Error("the newest event was forgotten")
	}
	if !seen.first("Ev0") {
		t.Error("the oldest event was still remembered, so the window is not bounded")
	}
}
