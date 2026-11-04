package connector

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/episode"
)

// exportFixture is the captured real-format Slack export: nested top folder,
// users.json, channels.json, groups.json, dms.json, and per-channel day files
// with join subtypes, bot messages, inline thread replies, file shares, and a
// message from a user missing from users.json.
const exportFixture = "testdata/slack_export.zip"

// fetchExport runs the export connector over the fixture zip and returns the
// records, episodes, and log output.
func fetchExport(t *testing.T, opts SlackExportOptions) ([]Record, []episode.Episode, string) {
	t.Helper()
	var log strings.Builder
	opts.Log = &log
	src := NewSlackExport(exportFixture, opts)
	recs, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return recs, src.Episodes(), log.String()
}

// TestSlackExportPeople verifies who the export yields: everyone in
// users.json including the deactivated account, with the email-less member
// counted out loud.
func TestSlackExportPeople(t *testing.T) {
	t.Parallel()
	recs, _, log := fetchExport(t, SlackExportOptions{SinceDays: 36500})

	people := make(map[string]Record)
	for _, r := range recs {
		if r.Kind == KindPerson && r.PersonID != "" {
			people[r.PersonID] = r
		}
	}
	alice := people["slack:U01AAA111"]
	if alice.Email != "alice@corp.com" || alice.Name != "Alice Nguyen" || alice.Title != "Staff Engineer" {
		t.Errorf("alice = %+v, want email, real name, and title from users.json", alice)
	}
	if dee := people["slack:U01DDD444"]; dee.Email != "dee@corp.com" {
		t.Errorf("deactivated user = %+v, want kept with email; leavers are who recall is for", dee)
	}
	// Carol has no email and the bot has no email; both are counted, and the
	// operator hears it once with the reason exports omit emails.
	if !strings.Contains(log, "2 of 5 members carry no email") {
		t.Errorf("log lacks the no-email count; log:\n%s", log)
	}
}

// TestSlackExportChannels verifies channel records: members are the people
// who actually spoke (never the bot), the purpose and sampled text are mined,
// and the channel the export lists but holds no folder for is skipped by name.
func TestSlackExportChannels(t *testing.T) {
	t.Parallel()
	recs, _, log := fetchExport(t, SlackExportOptions{SinceDays: 36500})

	channels := make(map[string]Record)
	for _, r := range recs {
		if r.Kind == KindChannel {
			channels[r.Name] = r
		}
	}
	general, ok := channels["general"]
	if !ok {
		t.Fatal("no channel record for general")
	}
	wantMembers := []string{"alice@corp.com", "bob@corp.com", "slack:U01CCC333"}
	if diff := cmp.Diff(wantMembers, general.Members); diff != "" {
		t.Errorf("general members (-want +got):\n%s", diff)
	}
	if general.Title != "Company wide announcements" {
		t.Errorf("general title = %q", general.Title)
	}
	if !strings.Contains(general.Text, "kafka") {
		t.Errorf("general text lacks message content: %q", general.Text)
	}
	if strings.Contains(general.Text, "standup in 10 minutes") {
		t.Errorf("bot message text leaked into the channel sample: %q", general.Text)
	}

	// The engineering channel: the file-share message is a person talking, so
	// Bob is a member; the unknown author keeps an opaque reference.
	eng := channels["engineering"]
	if !slices.Contains(eng.Members, "slack:U01ZZZ999") {
		t.Errorf("engineering members %v lack the unknown author's opaque id", eng.Members)
	}

	if _, ok := channels["ghost-channel"]; ok {
		t.Error("ghost-channel got a record despite having no folder")
	}
	if !strings.Contains(log, "skipping #ghost-channel") {
		t.Errorf("log does not name the skipped channel; log:\n%s", log)
	}

	// Private channels stay unread without the flag, and are named.
	if _, ok := channels["private-ops"]; ok {
		t.Error("private-ops read without IncludePrivate")
	}
	if !strings.Contains(log, "#private-ops") {
		t.Errorf("log does not name the unread private channel; log:\n%s", log)
	}
	if !strings.Contains(log, "direct message conversations") {
		t.Errorf("log does not mention the unread DMs; log:\n%s", log)
	}
}

// TestSlackExportPrivate verifies the flag reads groups.json.
func TestSlackExportPrivate(t *testing.T) {
	t.Parallel()
	recs, _, _ := fetchExport(t, SlackExportOptions{SinceDays: 36500, IncludePrivate: true})
	for _, r := range recs {
		if r.Kind == KindChannel && r.Name == "private-ops" {
			if !strings.Contains(r.Text, "paging") {
				t.Errorf("private-ops text = %q, want its messages mined", r.Text)
			}
			return
		}
	}
	t.Error("no channel record for private-ops with IncludePrivate")
}

// TestSlackExportEpisodes verifies conversations survive the export read: the
// thread is an episode whose body carries the words of the replies where the
// problem got solved, whose participants include the sixth-message speakers,
// and the loose general chat forms a window episode.
func TestSlackExportEpisodes(t *testing.T) {
	t.Parallel()
	_, eps, _ := fetchExport(t, SlackExportOptions{SinceDays: 36500, Episodes: true})

	var thread, window *episode.Episode
	for i := range eps {
		switch eps[i].Kind {
		case episode.KindThread:
			thread = &eps[i]
		case episode.KindWindow:
			if eps[i].Place == "general" {
				window = &eps[i]
			}
		}
	}
	if thread == nil {
		t.Fatal("no thread episode from the webhook thread")
		return
	}
	if thread.Messages != 4 {
		t.Errorf("thread messages = %d, want parent plus 3 replies", thread.Messages)
	}
	// The replies are inline in the export, so the body must hold the answer
	// (event id), not only the question.
	if !strings.Contains(thread.Body, "event id") {
		t.Errorf("thread body lacks the reply content: %q", thread.Body)
	}
	for _, want := range []string{"alice@corp.com", "bob@corp.com", "slack:U01CCC333", "dee@corp.com"} {
		if !slices.Contains(participantsOf(thread), want) {
			t.Errorf("thread participants %v lack %s", thread.Participants, want)
		}
	}
	// No archive was asked for, so nothing verbatim is retained.
	if len(thread.Archive) != 0 {
		t.Errorf("thread retained %d notes without Archive", len(thread.Archive))
	}

	if window == nil {
		t.Fatal("no window episode from the loose general conversation")
		return
	}
	if !strings.Contains(window.Body, "kafka") {
		t.Errorf("window body = %q, want the loose messages", window.Body)
	}
}

// participantsOf returns an episode's participants as strings.
func participantsOf(ep *episode.Episode) []string {
	out := make([]string, len(ep.Participants))
	for i, p := range ep.Participants {
		out[i] = string(p)
	}
	return out
}

// TestSlackExportArchive verifies the archive retains the thread verbatim,
// newest-first order preserved, and the file share is retained as the name of
// what was shared. It also holds the export path to the live connector's
// promise on window conversations: the loose general chat must carry its
// archive too, which the export path used to skip entirely.
func TestSlackExportArchive(t *testing.T) {
	t.Parallel()
	_, eps, _ := fetchExport(t, SlackExportOptions{SinceDays: 36500, Episodes: true, Archive: true})
	var thread, window *episode.Episode
	for i := range eps {
		switch {
		case eps[i].Kind == episode.KindThread:
			thread = &eps[i]
		case eps[i].Kind == episode.KindWindow && eps[i].Place == "general":
			window = &eps[i]
		}
	}
	if thread == nil {
		t.Fatal("no thread episode")
		return
	}
	notes := thread.Archive
	if len(notes) != 4 {
		t.Fatalf("archive notes = %d, want the whole thread", len(notes))
	}
	if !strings.Contains(notes[0].Text, "duplicating charges") {
		t.Errorf("first note = %q, want the parent question", notes[0].Text)
	}
	if !strings.Contains(notes[3].Text, "event id is the way") {
		t.Errorf("last note = %q, want the final reply", notes[3].Text)
	}
	if window == nil {
		t.Fatal("no window episode from the loose general conversation")
		return
	}
	if len(window.Archive) == 0 {
		t.Error("the window conversation kept no archive; the export path dropped what the live path retains")
	}
}

// TestSlackExportBadGroupsReported verifies a groups.json that exists but
// cannot be parsed is reported under --include-private, instead of every
// private channel silently vanishing from the run.
func TestSlackExportBadGroupsReported(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"users.json":    `[{"id":"U1","profile":{"real_name":"Jane","email":"jane@x.com"}}]`,
		"channels.json": `[]`,
		"groups.json":   `{this is not json`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	var log strings.Builder
	src := NewSlackExport(dir, SlackExportOptions{IncludePrivate: true, Log: &log})
	if _, err := src.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: a bad groups.json must not fail the public read: %v", err)
	}
	if !strings.Contains(log.String(), "groups.json is unreadable") {
		t.Errorf("log does not report the unreadable groups.json; log:\n%s", log.String())
	}
}

// TestSlackExportUnreadLinesOrderStable verifies the unread-conversation
// report lines land in the same order on every run. They were ranged from a
// map literal, and identical runs logged them in different orders.
func TestSlackExportUnreadLinesOrderStable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"users.json":    `[{"id":"U1","profile":{"real_name":"Jane","email":"jane@x.com"}}]`,
		"channels.json": `[]`,
		"dms.json":      `[{"id":"D1"}]`,
		"mpims.json":    `[{"id":"G1"}]`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for range 12 {
		var log strings.Builder
		src := NewSlackExport(dir, SlackExportOptions{Log: &log})
		if _, err := src.Fetch(context.Background()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		dms := strings.Index(log.String(), "direct message conversations")
		mpims := strings.Index(log.String(), "group message conversations")
		if dms < 0 || mpims < 0 {
			t.Fatalf("log lacks the unread-conversation lines; log:\n%s", log.String())
		}
		if dms > mpims {
			t.Fatalf("unread lines changed order between runs; log:\n%s", log.String())
		}
	}
}

// TestSlackExportSince verifies the incremental window: a Since inside the
// fixture's range drops the older days.
func TestSlackExportSince(t *testing.T) {
	t.Parallel()
	recs, _, _ := fetchExport(t, SlackExportOptions{
		SinceDays: 36500,
		Since:     time.Unix(1780472000, 0), // Between June 2 and June 3.
	})
	for _, r := range recs {
		if r.Kind == KindChannel && r.Name == "general" && r.Text != "" &&
			strings.Contains(r.Text, "kafka") {
			t.Errorf("general still carries June 1 messages: %q", r.Text)
		}
		if r.Kind == KindChannel && r.Name == "engineering" &&
			!strings.Contains(r.Text, "terraform") {
			t.Errorf("engineering lost its June 3 messages: %q", r.Text)
		}
	}
}

// TestSlackExportDeterminism verifies the product claim byte for byte: two
// reads of one export produce identical records and episodes.
func TestSlackExportDeterminism(t *testing.T) {
	t.Parallel()
	opts := SlackExportOptions{SinceDays: 36500, Episodes: true, Archive: true}
	recs1, eps1, _ := fetchExport(t, opts)
	recs2, eps2, _ := fetchExport(t, opts)
	if diff := cmp.Diff(recs1, recs2); diff != "" {
		t.Errorf("records differ between two reads:\n%s", diff)
	}
	if diff := cmp.Diff(eps1, eps2); diff != "" {
		t.Errorf("episodes differ between two reads:\n%s", diff)
	}
}

// TestSlackExportUnzippedDir verifies a directory the zip was unzipped into
// reads identically to the zip itself.
func TestSlackExportUnzippedDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zr, err := zip.OpenReader(exportFixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", f.Name, err)
		}
	}

	opts := SlackExportOptions{SinceDays: 36500, Episodes: true}
	fromZip := NewSlackExport(exportFixture, opts)
	zipRecs, err := fromZip.Fetch(context.Background())
	if err != nil {
		t.Fatalf("zip fetch: %v", err)
	}
	fromDir := NewSlackExport(dir, opts)
	dirRecs, err := fromDir.Fetch(context.Background())
	if err != nil {
		t.Fatalf("dir fetch: %v", err)
	}
	if diff := cmp.Diff(zipRecs, dirRecs); diff != "" {
		t.Errorf("zip and directory reads differ:\n%s", diff)
	}
	if diff := cmp.Diff(fromZip.Episodes(), fromDir.Episodes()); diff != "" {
		t.Errorf("zip and directory episodes differ:\n%s", diff)
	}
}

// TestSlackExportNotAnExport verifies a path with no users.json is refused
// with a message saying what was expected.
func TestSlackExportNotAnExport(t *testing.T) {
	t.Parallel()
	_, err := NewSlackExport(t.TempDir(), SlackExportOptions{}).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "users.json") {
		t.Errorf("err = %v, want a users.json complaint", err)
	}
}
