package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/slack"
	"github.com/dcadolph/peepwho/internal/util"
)

// Archive bounds. A retained conversation is meant to be read by a person, so
// it keeps the discussion and stops well short of copying a channel.
const (
	// defaultMaxArchiveMessages caps retained messages per conversation.
	defaultMaxArchiveMessages = 50
	// maxArchiveBytes caps retained text per conversation.
	maxArchiveBytes = 12000
	// maxNoteBytes caps one retained message. A conversation is kept to be
	// read, so a single pasted log is cut rather than allowed to stand in for
	// the discussion around it.
	maxNoteBytes = 2000
)

// maxIndexReplies caps how many replies are fetched to fold into a thread's
// searchable body when no archive is kept. It is smaller than the archive cap
// because this text is tokenized and discarded, not retained for reading.
const maxIndexReplies = 20

// enrichThreads reads each thread's replies and folds them into the episode: the
// words into the searchable body, so recall can match how a problem was solved
// and not only how it was asked, and the authors into the participant list past
// the five names Slack puts on the parent. When an archive is asked
// for it also retains the verbatim messages for quoting. This is the flagship
// recall value, so it runs whenever episodes are collected, not only under an
// archive. A thread that cannot be read is left with its parent-only body rather
// than failing the run.
func (s *Slack) enrichThreads(ctx context.Context, eps []episode.Episode, byID map[string]slack.User) {
	limit := maxIndexReplies
	if s.opts.Archive {
		limit = s.opts.MaxArchiveMessages
		if limit <= 0 {
			limit = defaultMaxArchiveMessages
		}
	}
	for i := range eps {
		ep := &eps[i]
		if ep.Kind != episode.KindThread || ep.PlaceID == "" {
			continue
		}
		threadTS := threadTSOf(ep.ID)
		if threadTS == "" {
			continue
		}
		msgs, err := s.client.Replies(ctx, ep.PlaceID, threadTS, limit)
		if err != nil {
			if errors.Is(err, slack.ErrRateLimited) {
				fmt.Fprintf(s.opts.Log,
					"slack: rate limited reading threads in #%s; keeping the rest as links only\n", ep.Place)
				return
			}
			fmt.Fprintf(s.opts.Log, "slack: could not read a thread in #%s: %v\n", ep.Place, err)
			continue
		}
		if body := episodeText(msgs); body != "" {
			ep.Body = body
		}
		addParticipants(ep, msgs, byID)
		if s.opts.Archive {
			ep.Archive = notesFrom(msgs, byID)
		}
	}
}

// addParticipants folds the people who spoke into the participant list. Slack
// names only the first five repliers on a thread parent, so without this the
// sixth person to help, or anyone quoted, could not find the thread again with
// recall --me.
func addParticipants(ep *episode.Episode, msgs []slack.Message, byID map[string]slack.User) {
	seen := make(map[model.ID]bool, len(ep.Participants))
	for _, p := range ep.Participants {
		seen[p] = true
	}
	for _, m := range msgs {
		if !m.FromPerson() {
			continue
		}
		pid := model.ID(slackUserRef(m.User, byID))
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		ep.Participants = append(ep.Participants, pid)
	}
}

// notesFrom turns fetched messages into retained notes, skipping system and
// bot messages and stopping at the size cap.
func notesFrom(msgs []slack.Message, byID map[string]slack.User) []episode.Note {
	var notes []episode.Note
	kept := 0
	for _, m := range msgs {
		if !m.FromPerson() {
			continue
		}
		author := model.ID(slackUserRef(m.User, byID))
		text := noteText(m)
		if author == "" || text == "" {
			continue
		}
		// Each message is cut to its own cap first, so one pasted log cannot
		// exhaust the budget and drop the messages after it, which are usually
		// where the problem gets solved.
		if kept+len(text) > maxArchiveBytes {
			break
		}
		kept += len(text)
		notes = append(notes, episode.Note{
			Author: author,
			At:     slackTime(m.TS),
			Text:   text,
		})
	}
	return notes
}

// noteText returns what to retain for one message: what was written, or a line
// naming what was shared when the message carries only files. File content is
// never read, only what Slack already said the file is called.
func noteText(m slack.Message) string {
	if m.Text != "" {
		return util.Truncate(m.Text, maxNoteBytes)
	}
	if names := m.FileNames(); len(names) > 0 {
		return util.Truncate("shared "+strings.Join(names, ", "), maxNoteBytes)
	}
	return ""
}

// threadTSOf recovers the thread timestamp from an episode id of the form
// "slack:<channel>:<thread ts>". A windowed conversation carries a "w" prefix
// instead and has no thread, so it returns the empty string.
func threadTSOf(id string) string {
	i := strings.LastIndex(id, ":")
	if i < 0 {
		return ""
	}
	ts := id[i+1:]
	if strings.HasPrefix(ts, "w") {
		return ""
	}
	return ts
}
