package connector

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/slack"
)

// SlackExportOptions configures the Slack export connector.
type SlackExportOptions struct {
	// IncludePrivate also reads private channels when the export contains them
	// (groups.json). Direct and group messages are never read.
	IncludePrivate bool
	// SinceDays bounds history age; zero uses the connector default.
	SinceDays int
	// MaxMessages caps messages per channel; zero uses the default. The newest
	// messages are kept, matching what a live read returns.
	MaxMessages int
	// Episodes records the conversations behind the messages.
	Episodes bool
	// MaxEpisodesPerChannel caps episodes kept per channel; zero uses the
	// default.
	MaxEpisodesPerChannel int
	// Archive retains the content of each conversation, not just a link.
	Archive bool
	// MaxArchiveMessages caps retained messages per conversation; zero uses
	// the default.
	MaxArchiveMessages int
	// Since, when later than the SinceDays window, reads only messages posted
	// at or after it.
	Since time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// withDefaults fills zero fields with defaults.
func (o SlackExportOptions) withDefaults() SlackExportOptions {
	if o.SinceDays <= 0 {
		o.SinceDays = defaultSinceDays
	}
	if o.MaxMessages <= 0 {
		o.MaxMessages = defaultMaxMessages
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	return o
}

// SlackExport is a Source that reads a Slack workspace export, the zip a
// workspace admin downloads from Slack, without any token or network access.
// It produces the same records the live Slack connector does, built from the
// same message data: exports carry users.json, channels.json, and one file of
// messages per channel per day, including thread replies inline.
type SlackExport struct {
	// path is the export zip file or an unzipped export directory.
	path string
	// opts holds the resolved ingest bounds.
	opts SlackExportOptions
	// episodes holds the conversations seen by the last Fetch.
	episodes []episode.Episode
}

// NewSlackExport returns a connector reading the export at path, either the
// zip itself or a directory it was unzipped into.
func NewSlackExport(path string, opts SlackExportOptions) *SlackExport {
	return &SlackExport{path: path, opts: opts.withDefaults()}
}

// Episodes returns the conversations seen by the most recent Fetch. It is
// empty unless SlackExportOptions.Episodes was set.
func (s *SlackExport) Episodes() []episode.Episode { return s.episodes }

// Fetch reads the export and returns person and channel records, mirroring
// the live connector's output shape so the two paths index identically.
func (s *SlackExport) Fetch(ctx context.Context) ([]Record, error) {
	fsys, closeFS, err := openExport(s.path)
	if err != nil {
		return nil, err
	}
	defer closeFS()

	users, err := readExportJSON[[]slack.User](fsys, "users.json")
	if err != nil {
		return nil, fmt.Errorf("slack export: %w", err)
	}
	byID := make(map[string]slack.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}

	var records []Record
	noEmail := 0
	for _, u := range users {
		if u.Profile.RealName == "" && u.Profile.Email == "" {
			continue
		}
		if u.Profile.Email == "" {
			noEmail++
		}
		records = append(records, personRecord(u))
	}
	if noEmail > 0 {
		fmt.Fprintf(s.opts.Log,
			"slack export: %d of %d members carry no email, so their Slack activity will not "+
				"merge with the same people from other sources. Standard exports omit emails "+
				"unless the workspace admin enabled them\n", noEmail, len(records))
	}

	channels, err := s.exportChannels(fsys)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(s.opts.Log, "slack export: %d users, %d channels\n", len(users), len(channels))
	s.reportUnread(fsys)

	if s.opts.Episodes {
		s.episodes = nil
	}
	oldest := slackSeconds(slackOldestSince(s.opts.SinceDays, s.opts.Since))
	for _, ch := range channels {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msgs, replies, err := s.channelMessages(fsys, ch.Name, oldest)
		if err != nil {
			fmt.Fprintf(s.opts.Log, "slack export: skipping #%s: %v\n", ch.Name, err)
			continue
		}
		chRec, authorText, authorLatest := channelRecord(ch, msgs, byID, "")
		records = append(records, chRec)
		records = append(records, authorTextRecords(authorText, authorLatest)...)
		if s.opts.Episodes {
			// The archive settings ride along the way the live connector passes
			// them, so a window conversation keeps its verbatim notes here too;
			// enrichFromExport then covers the threads, whose replies live in
			// the day files rather than in the history.
			eps := collectEpisodes(ch, msgs, episodeOpts{
				byID:       byID,
				max:        s.opts.MaxEpisodesPerChannel,
				archive:    s.opts.Archive,
				maxArchive: s.opts.MaxArchiveMessages,
			})
			s.enrichFromExport(eps, msgs, replies, byID)
			s.episodes = append(s.episodes, eps...)
		}
		fmt.Fprintf(s.opts.Log, "slack export: indexed #%s (%d messages)\n", ch.Name, len(msgs))
	}
	return records, nil
}

// openExport opens the export as a file system: the zip itself, or a directory
// it was unzipped into. Slack sometimes zips the content under one top-level
// folder, so the root is wherever users.json is.
func openExport(p string) (fs.FS, func(), error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, nil, fmt.Errorf("slack export: %w", err)
	}
	var fsys fs.FS
	closeFS := func() {}
	if info.IsDir() {
		fsys = os.DirFS(p)
	} else {
		zr, err := zip.OpenReader(p)
		if err != nil {
			return nil, nil, fmt.Errorf("slack export: open zip: %w", err)
		}
		fsys, closeFS = zr, func() { _ = zr.Close() }
	}
	if _, err := fs.Stat(fsys, "users.json"); err == nil {
		return fsys, closeFS, nil
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		if sub, err := fs.Sub(fsys, entries[0].Name()); err == nil {
			if _, err := fs.Stat(sub, "users.json"); err == nil {
				return sub, closeFS, nil
			}
		}
	}
	closeFS()
	return nil, nil, fmt.Errorf("slack export: %s holds no users.json; is it a Slack export?", p)
}

// readExportJSON decodes one JSON file from the export.
func readExportJSON[T any](fsys fs.FS, name string) (T, error) {
	var out T
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("parse %s: %w", name, err)
	}
	return out, nil
}

// exportChannels lists the conversations to read: channels.json always, plus
// groups.json (private channels) when asked for. Order is the file's own, so
// two reads of one export index identically. An export without groups.json
// simply has no private channels; a groups.json that exists but cannot be read
// is reported, since dropping every private channel in silence is exactly the
// failure --include-private was passed to avoid.
func (s *SlackExport) exportChannels(fsys fs.FS) ([]slack.Channel, error) {
	channels, err := readExportJSON[[]slack.Channel](fsys, "channels.json")
	if err != nil {
		return nil, fmt.Errorf("slack export: %w", err)
	}
	if s.opts.IncludePrivate {
		groups, err := readExportJSON[[]slack.Channel](fsys, "groups.json")
		switch {
		case err == nil:
			channels = append(channels, groups...)
		case errors.Is(err, fs.ErrNotExist):
		default:
			fmt.Fprintf(s.opts.Log,
				"slack export: groups.json is unreadable, so no private channel was read: %v\n", err)
		}
	}
	return channels, nil
}

// reportUnread names what the export contains that this run will not read, so
// nothing is dropped in silence: private channels without the flag, and direct
// or group messages ever.
func (s *SlackExport) reportUnread(fsys fs.FS) {
	if !s.opts.IncludePrivate {
		if groups, err := readExportJSON[[]slack.Channel](fsys, "groups.json"); err == nil && len(groups) > 0 {
			names := make([]string, 0, len(groups))
			for _, g := range groups {
				names = append(names, "#"+g.Name)
			}
			sort.Strings(names)
			if len(names) > maxNamedSkips {
				names = append(names[:maxNamedSkips], fmt.Sprintf("and %d more", len(groups)-maxNamedSkips))
			}
			fmt.Fprintf(s.opts.Log,
				"slack export: %d private channels not read (pass --include-private to read them): %s\n",
				len(groups), strings.Join(names, ", "))
		}
	}
	// A slice, not a map: ranging a map varied the line order between
	// identical runs, and reproducible output is a product claim.
	for _, f := range []struct{ name, what string }{
		{"dms.json", "direct message conversations"},
		{"mpims.json", "group message conversations"},
	} {
		if raw, err := readExportJSON[[]json.RawMessage](fsys, f.name); err == nil && len(raw) > 0 {
			fmt.Fprintf(s.opts.Log,
				"slack export: %d %s in the export are never read; peepwho indexes channels, not mail\n",
				len(raw), f.what)
		}
	}
}

// channelMessages reads every day file of one channel, splitting thread
// replies out the way a live history read does: the channel history holds
// parents and loose messages, and the replies enrich threads afterward. The
// newest MaxMessages survive a cap, matching the live read.
func (s *SlackExport) channelMessages(
	fsys fs.FS, name string, oldest float64,
) (msgs []slack.Message, replies map[string][]slack.Message, err error) {
	days, err := fs.ReadDir(fsys, name)
	if err != nil {
		return nil, nil, fmt.Errorf("the export lists this channel but holds no folder for it: %w", err)
	}
	files := make([]string, 0, len(days))
	for _, d := range days {
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			files = append(files, d.Name())
		}
	}
	sort.Strings(files)

	replies = make(map[string][]slack.Message)
	replyUsers := make(map[string][]string)
	for _, f := range files {
		day, err := readExportJSON[[]slack.Message](fsys, path.Join(name, f))
		if err != nil {
			fmt.Fprintf(s.opts.Log, "slack export: unreadable day file %s/%s: %v\n", name, f, err)
			continue
		}
		for _, m := range day {
			if slackSeconds(m.TS) < oldest {
				continue
			}
			if m.ThreadTS != "" && m.ThreadTS != m.TS {
				replies[m.ThreadTS] = append(replies[m.ThreadTS], m)
				replyUsers[m.ThreadTS] = append(replyUsers[m.ThreadTS], m.User)
				continue
			}
			msgs = append(msgs, m)
		}
	}
	// Some exports leave reply_count and reply_users off the parent. The
	// replies are in hand, so restore the thread shape collectEpisodes needs.
	for i, m := range msgs {
		rs := replies[m.TS]
		if m.ThreadTS == m.TS && len(rs) > 0 {
			if m.ReplyCount < len(rs) {
				msgs[i].ReplyCount = len(rs)
			}
			if len(m.ReplyUsers) == 0 {
				msgs[i].ReplyUsers = distinctUsers(replyUsers[m.TS])
			}
			last := rs[len(rs)-1]
			if m.LatestReply == "" {
				msgs[i].LatestReply = last.TS
			}
		}
	}
	if len(msgs) > s.opts.MaxMessages {
		fmt.Fprintf(s.opts.Log,
			"slack export: #%s hit the %d message cap; %d older messages skipped\n",
			name, s.opts.MaxMessages, len(msgs)-s.opts.MaxMessages)
		msgs = msgs[len(msgs)-s.opts.MaxMessages:]
	}
	return msgs, replies, nil
}

// distinctUsers returns the user IDs in first-seen order without duplicates.
func distinctUsers(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// enrichFromExport folds each thread's replies into its episode the way
// enrichThreads does over the API, using the replies already read from the
// day files: the words into the searchable body, the speakers into the
// participants, and, under an archive, the verbatim messages.
func (s *SlackExport) enrichFromExport(
	eps []episode.Episode, msgs []slack.Message, replies map[string][]slack.Message,
	byID map[string]slack.User,
) {
	parents := make(map[string]slack.Message, len(msgs))
	for _, m := range msgs {
		if isThreadParent(m) {
			parents[m.TS] = m
		}
	}
	limit := maxIndexReplies
	if s.opts.Archive {
		limit = s.opts.MaxArchiveMessages
		if limit <= 0 {
			limit = defaultMaxArchiveMessages
		}
	}
	for i := range eps {
		ep := &eps[i]
		if ep.Kind != episode.KindThread {
			continue
		}
		threadTS := threadTSOf(ep.ID)
		parent, ok := parents[threadTS]
		if !ok {
			continue
		}
		thread := append([]slack.Message{parent}, replies[threadTS]...)
		sort.SliceStable(thread, func(a, b int) bool {
			return slackSeconds(thread[a].TS) < slackSeconds(thread[b].TS)
		})
		if len(thread) > limit {
			thread = thread[:limit]
		}
		if body := episodeText(thread); body != "" {
			ep.Body = body
		}
		addParticipants(ep, thread, byID)
		if s.opts.Archive {
			ep.Archive = notesFrom(thread, byID)
		}
	}
}
