package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/slack"
)

// TestThreadTSOf verifies the thread timestamp is recovered from an episode
// id, and that a windowed conversation reports none.
func TestThreadTSOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{
		{In: "slack:C1:1712345678.000100", Want: "1712345678.000100"},
		{In: "slack:C1:w1712345678.000100", Want: ""},
		{In: "slack:C1:", Want: ""},
		{In: "nocolons", Want: ""},
	}
	for _, test := range tests {
		if got := threadTSOf(test.In); got != test.Want {
			t.Errorf("threadTSOf(%q) = %q, want %q", test.In, got, test.Want)
		}
	}
}

// TestNotesFrom verifies retained notes skip system and bot messages, resolve
// authors to people, and stop at the size cap.
func TestNotesFrom(t *testing.T) {
	t.Parallel()
	msgs := []slack.Message{
		{User: "U1", Text: "the cert expired", TS: ts(1000)},
		{User: "U2", BotID: "B1", Text: "deploy finished", TS: ts(1100)},
		{Subtype: "channel_join", User: "U2", Text: "joined", TS: ts(1200)},
		{User: "U2", Text: "", TS: ts(1300)},
		{User: "U2", Text: "renew it with certbot", TS: ts(1400)},
	}
	notes := notesFrom(msgs, testUsers)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want two", notes)
	}
	if notes[0].Author != "ann@x.com" || notes[1].Author != "bo@x.com" {
		t.Errorf("authors = %q and %q, want the two people", notes[0].Author, notes[1].Author)
	}
	if notes[0].Text != "the cert expired" || notes[0].At.IsZero() {
		t.Errorf("note = %+v, want the text and its time", notes[0])
	}

	// A pasted log must be cut to its own cap rather than consuming the whole
	// conversation's budget. The messages after it are where the problem gets
	// solved, so losing them would leave the archive worth nothing.
	long := []slack.Message{
		{User: "U1", Text: strings.Repeat("x", maxArchiveBytes*3), TS: ts(1000)},
		{User: "U2", Text: "renew it with certbot", TS: ts(1100)},
	}
	got := notesFrom(long, testUsers)
	if len(got) != 2 {
		t.Fatalf("notes = %d, want the pasted log cut and the answer after it kept", len(got))
	}
	if len(got[0].Text) != maxNoteBytes {
		t.Errorf("pasted log kept %d bytes, want it cut to %d", len(got[0].Text), maxNoteBytes)
	}
	if got[1].Text != "renew it with certbot" {
		t.Errorf("message after the log = %q, want it kept whole", got[1].Text)
	}

	// The conversation budget still stops accumulation once it is used up.
	var many []slack.Message
	for i := range 20 {
		many = append(many, slack.Message{
			User: "U1", Text: strings.Repeat("y", maxNoteBytes), TS: ts(1000 + i),
		})
	}
	if got := notesFrom(many, testUsers); len(got) != maxArchiveBytes/maxNoteBytes {
		t.Errorf("notes = %d, want the budget to stop at %d", len(got), maxArchiveBytes/maxNoteBytes)
	}
}

// TestNotesFromFiles verifies a message carrying only a shared file is kept as
// what it is called, since whotoask never reads file content but the name is
// often the most useful thing in the conversation.
func TestNotesFromFiles(t *testing.T) {
	t.Parallel()
	msgs := []slack.Message{
		{User: "U1", Subtype: "file_share", TS: ts(1000), Files: []slack.File{
			{Name: "q3-billing-retry-postmortem.pdf", Filetype: "pdf"},
		}},
		{User: "U2", Text: "see page 4", TS: ts(1100), Files: []slack.File{
			{Name: "trace.mp4", Title: "screen recording", Filetype: "mp4"},
		}},
	}
	notes := notesFrom(msgs, testUsers)
	if len(notes) != 2 {
		t.Fatalf("notes = %+v, want both kept", notes)
	}
	if !strings.Contains(notes[0].Text, "q3-billing-retry-postmortem.pdf") {
		t.Errorf("file-only note = %q, want it to name the file", notes[0].Text)
	}
	if notes[1].Text != "see page 4" {
		t.Errorf("note with text = %q, want what was written", notes[1].Text)
	}
}

// TestEnrichThreadsFoldsReplies verifies reading a thread folds the replies into
// the searchable body on any tier and retains them verbatim only under an
// archive; a thread it cannot read keeps its parent body, and a windowed
// conversation is never fetched because its messages are already in hand.
func TestEnrichThreadsFoldsReplies(t *testing.T) {
	t.Parallel()
	server := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "ts=9999") {
				io.WriteString(w, `{"ok":false,"error":"thread_not_found"}`)
				return
			}
			io.WriteString(w, `{"ok":true,"messages":[
				{"user":"U1","text":"the cert expired","ts":"1000.000100"},
				{"user":"U2","text":"renew it with certbot","ts":"1100.000100"}]}`)
		}))
	}
	newEps := func() []episode.Episode {
		return []episode.Episode{
			{ID: "slack:C1:1000.000100", Kind: episode.KindThread, PlaceID: "C1", Place: "infra", Body: "cert broken"},
			{ID: "slack:C1:9999", Kind: episode.KindThread, PlaceID: "C1", Place: "broken", Body: "still down"},
			{ID: "slack:C1:w2000.000100", Kind: episode.KindWindow, PlaceID: "C1", Place: "loose", Body: "loose"},
		}
	}

	// Under an archive the replies fold into the body and are retained verbatim.
	srv := server()
	t.Cleanup(srv.Close)
	arch := NewSlackWithClient(slack.New("t", slack.WithBaseURL(srv.URL)), SlackOptions{Archive: true})
	eps := newEps()
	arch.enrichThreads(context.Background(), eps, testUsers)
	if !strings.Contains(eps[0].Body, "certbot") {
		t.Errorf("body = %q, want the reply folded in", eps[0].Body)
	}
	if len(eps[0].Archive) != 2 {
		t.Errorf("archive = %+v, want the two replies retained under an archive", eps[0].Archive)
	}
	if eps[1].Archived() || !strings.Contains(eps[1].Body, "still down") {
		t.Error("an unreadable thread should keep its parent body and no archive")
	}
	if eps[2].Archived() || eps[2].Body != "loose" {
		t.Error("a windowed conversation should not be fetched")
	}

	// Without the archive the replies still fold into the body, but nothing is kept.
	srv2 := server()
	t.Cleanup(srv2.Close)
	plain := NewSlackWithClient(slack.New("t", slack.WithBaseURL(srv2.URL)), SlackOptions{})
	eps2 := newEps()
	plain.enrichThreads(context.Background(), eps2, testUsers)
	if !strings.Contains(eps2[0].Body, "certbot") {
		t.Errorf("no-archive body = %q, want the reply folded in for search", eps2[0].Body)
	}
	if eps2[0].Archived() {
		t.Error("a run without the archive retained verbatim text")
	}
}

// TestCollectEpisodesArchivesWindows verifies a loose conversation keeps its
// content without any extra call, since those messages are already in hand.
func TestCollectEpisodesArchivesWindows(t *testing.T) {
	t.Parallel()
	msgs := []slack.Message{
		{User: "U1", Text: "anyone seen this timeout", TS: ts(2000)},
		{User: "U2", Text: "bump the pool size", TS: ts(2100)},
		{User: "U1", Text: "that worked", TS: ts(2200)},
	}
	got := collectEpisodes(testChannel, msgs, episodeOpts{byID: testUsers, archive: true})
	if len(got) != 1 || len(got[0].Archive) != 3 {
		t.Fatalf("episodes = %+v, want one conversation with three notes", got)
	}
	if got[0].Archive[1].Text != "bump the pool size" {
		t.Errorf("note = %q, want the reply that solved it", got[0].Archive[1].Text)
	}

	plain := collectEpisodes(testChannel, msgs, episodeOpts{byID: testUsers})
	if plain[0].Archived() {
		t.Error("content was kept without the archive being asked for")
	}
}
