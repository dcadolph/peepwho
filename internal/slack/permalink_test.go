package slack

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestPermalink verifies archive URLs are built from history data alone,
// including the reply form, and that missing pieces yield no link.
func TestPermalink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WorkspaceURL string
		ChannelID    string
		TS           string
		ThreadTS     string
		WantLink     string
	}{{ // Test 0: Parent message, workspace URL with a trailing slash.
		WorkspaceURL: "https://acme.slack.com/",
		ChannelID:    "C123",
		TS:           "1712345678.000100",
		WantLink:     "https://acme.slack.com/archives/C123/p1712345678000100",
	}, { // Test 1: Trailing slash absent.
		WorkspaceURL: "https://acme.slack.com",
		ChannelID:    "C123",
		TS:           "1712345678.000100",
		WantLink:     "https://acme.slack.com/archives/C123/p1712345678000100",
	}, { // Test 2: Reply carries its thread anchor.
		WorkspaceURL: "https://acme.slack.com/",
		ChannelID:    "C123",
		TS:           "1712345999.000200",
		ThreadTS:     "1712345678.000100",
		WantLink: "https://acme.slack.com/archives/C123/p1712345999000200" +
			"?thread_ts=1712345678.000100&cid=C123",
	}, { // Test 3: Parent whose thread ts equals its own ts stays unanchored.
		WorkspaceURL: "https://acme.slack.com/",
		ChannelID:    "C123",
		TS:           "1712345678.000100",
		ThreadTS:     "1712345678.000100",
		WantLink:     "https://acme.slack.com/archives/C123/p1712345678000100",
	}, { // Test 4: No workspace URL, so no link.
		ChannelID: "C123",
		TS:        "1712345678.000100",
		WantLink:  "",
	}, { // Test 5: No channel, so no link.
		WorkspaceURL: "https://acme.slack.com/",
		TS:           "1712345678.000100",
		WantLink:     "",
	}, { // Test 6: No timestamp, so no link.
		WorkspaceURL: "https://acme.slack.com/",
		ChannelID:    "C123",
		WantLink:     "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := Permalink(test.WorkspaceURL, test.ChannelID, test.TS, test.ThreadTS)
			if diff := cmp.Diff(test.WantLink, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
