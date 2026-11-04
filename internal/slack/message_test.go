package slack

import (
	"fmt"
	"strings"
	"testing"
)

// TestFromPerson verifies conversation is told apart from channel bookkeeping.
// A file share is a person talking and must be kept; joins, topic changes, and
// bot posts are not conversation and must not become episodes.
func TestFromPerson(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   Message
		Want bool
	}{{ // Test 0: An ordinary message.
		In: Message{User: "U1", Text: "the cert expired"}, Want: true,
	}, { // Test 1: A file share, which is a person sharing something.
		In: Message{User: "U1", Subtype: "file_share"}, Want: true,
	}, { // Test 2: A reply also sent to the channel.
		In: Message{User: "U1", Subtype: "thread_broadcast", Text: "fixed"}, Want: true,
	}, { // Test 3: An emote.
		In: Message{User: "U1", Subtype: "me_message", Text: "shrugs"}, Want: true,
	}, { // Test 4: A join notice.
		In: Message{User: "U1", Subtype: "channel_join", Text: "joined"}, Want: false,
	}, { // Test 5: A topic change.
		In: Message{User: "U1", Subtype: "channel_topic", Text: "topic"}, Want: false,
	}, { // Test 6: A bot post.
		In: Message{User: "U1", BotID: "B1", Text: "deploy finished"}, Want: false,
	}, { // Test 7: No author at all.
		In: Message{Text: "orphan"}, Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := test.In.FromPerson(); got != test.Want {
				t.Errorf("FromPerson() = %v, want %v for %+v", got, test.Want, test.In)
			}
		})
	}
}

// TestSearchTextAndFileNames verifies shared files contribute what they are
// called and nothing more. peepwho never downloads a file, so a video or an
// installer costs nothing beyond its name, and that name is often the most
// searchable thing in the conversation.
func TestSearchTextAndFileNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        Message
		WantHas   []string
		WantNames int
	}{{ // Test 0: Plain text and no files.
		In: Message{Text: "the cert expired"}, WantHas: []string{"the cert expired"},
	}, { // Test 1: Text plus a shared document.
		In: Message{Text: "see page 4", Files: []File{
			{Name: "q3-billing-retry-postmortem.pdf", Filetype: "pdf"}}},
		WantHas:   []string{"see page 4", "q3-billing-retry-postmortem.pdf"},
		WantNames: 1,
	}, { // Test 2: A file with no message text at all.
		In:        Message{Files: []File{{Name: "outage.mp4", Filetype: "mp4"}}},
		WantHas:   []string{"outage.mp4"},
		WantNames: 1,
	}, { // Test 3: A title is preferred over the raw upload name.
		In:        Message{Files: []File{{Name: "IMG_2231.png", Title: "billing dashboard"}}},
		WantHas:   []string{"billing dashboard"},
		WantNames: 1,
	}, { // Test 4: Several files, with a repeated name kept once.
		In: Message{Files: []File{
			{Name: "a.pdf", Title: "a.pdf"}, {Name: "b.pdf"}}},
		WantHas:   []string{"a.pdf", "b.pdf"},
		WantNames: 2,
	}, { // Test 5: Nothing at all.
		In: Message{}, WantNames: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := test.In.SearchText()
			for _, want := range test.WantHas {
				if !strings.Contains(got, want) {
					t.Errorf("SearchText() = %q, missing %q", got, want)
				}
			}
			if names := test.In.FileNames(); len(names) != test.WantNames {
				t.Errorf("FileNames() = %v, want %d", names, test.WantNames)
			}
			// A repeated name must not be indexed twice.
			if testNum == 4 && strings.Count(got, "a.pdf") != 1 {
				t.Errorf("SearchText() = %q, want a repeated name once", got)
			}
		})
	}
}
