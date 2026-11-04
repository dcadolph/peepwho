package connector

import (
	"sort"
	"strconv"
	"strings"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/slack"
	"github.com/dcadolph/peepwho/internal/util"
)

// Episode bounds for Slack. A thread is an episode outright; loose messages
// become one when several people talk in the same stretch of time.
const (
	// windowGapSeconds ends a conversation once nobody speaks for this long.
	windowGapSeconds = 30 * 60
	// minWindowMessages is the fewest messages a loose conversation needs
	// before it is worth remembering.
	minWindowMessages = 3
	// minWindowPeople is the fewest distinct speakers a loose conversation
	// needs, so one person thinking out loud is not an episode.
	minWindowPeople = 2
	// maxEpisodeText caps the text tokenized per episode, in bytes.
	maxEpisodeText = 2000
	// maxMessageText caps one message's contribution, so a pasted stack trace
	// cannot fill an episode's whole budget and crowd out the discussion.
	maxMessageText = 600
	// defaultMaxEpisodesPerChannel caps episodes kept per channel so one busy
	// channel cannot dominate the store.
	defaultMaxEpisodesPerChannel = 200
)

// Episodes returns the conversations seen by the most recent Fetch, newest
// first. It is empty unless SlackOptions.Episodes was set.
func (s *Slack) Episodes() []episode.Episode { return s.episodes }

// episodeOpts bounds how one channel's history becomes episodes.
type episodeOpts struct {
	// byID resolves message authors to people.
	byID map[string]slack.User
	// workspaceURL builds permalinks; empty means episodes carry no links.
	workspaceURL string
	// max caps episodes kept for the channel; zero uses the default.
	max int
	// archive retains the text of loose conversations, which are already in
	// hand. Threads need their replies fetched separately.
	archive bool
	// maxArchive caps retained messages per conversation; zero means the
	// connector default.
	maxArchive int
}

// collectEpisodes turns one channel's history into episodes: every thread with
// a reply, plus runs of loose messages where several people talked close
// together. Slack returns thread shape on the parent message, so this needs no
// extra API call and never reads a reply.
func collectEpisodes(ch slack.Channel, msgs []slack.Message, opts episodeOpts) []episode.Episode {
	byID, workspaceURL := opts.byID, opts.workspaceURL
	max := opts.max
	if max <= 0 {
		max = defaultMaxEpisodesPerChannel
	}
	ordered := make([]slack.Message, 0, len(msgs))
	for _, m := range msgs {
		if !m.FromPerson() {
			continue
		}
		ordered = append(ordered, m)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return slackSeconds(ordered[i].TS) < slackSeconds(ordered[j].TS)
	})

	var out []episode.Episode
	var window []slack.Message
	flush := func() {
		if ep, ok := windowEpisode(ch, window, byID, workspaceURL); ok {
			if opts.archive {
				capped := window
				if opts.maxArchive > 0 && len(capped) > opts.maxArchive {
					capped = capped[:opts.maxArchive]
				}
				ep.Archive = notesFrom(capped, byID)
			}
			out = append(out, ep)
		}
		window = nil
	}
	for _, m := range ordered {
		if isThreadParent(m) {
			// A thread stands on its own, and its parent does not belong to
			// whatever loose conversation surrounded it.
			flush()
			out = append(out, threadEpisode(ch, m, byID, workspaceURL))
			continue
		}
		if m.ThreadTS != "" {
			// A reply that arrived in the history page belongs to its thread,
			// which is already an episode.
			continue
		}
		if len(window) > 0 {
			gap := slackSeconds(m.TS) - slackSeconds(window[len(window)-1].TS)
			if gap > windowGapSeconds {
				flush()
			}
		}
		window = append(window, m)
	}
	flush()

	sort.SliceStable(out, func(i, j int) bool { return out[i].Occurred.After(out[j].Occurred) })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// isThreadParent reports whether m starts a thread that drew replies.
func isThreadParent(m slack.Message) bool {
	return m.ThreadTS != "" && m.ThreadTS == m.TS && m.ReplyCount > 0
}

// threadEpisode builds an episode from a thread parent. Participants come from
// the reply_users list Slack includes on the parent, so the people who helped
// are known without reading the replies. The parent's author is the asker, and
// the repliers are the ones who demonstrably knew the answer.
func threadEpisode(
	ch slack.Channel, m slack.Message, byID map[string]slack.User, workspaceURL string,
) episode.Episode {
	asked := model.ID(slackUserRef(m.User, byID))
	participants := []model.ID{asked}
	seen := map[model.ID]bool{asked: true}
	for _, u := range m.ReplyUsers {
		pid := model.ID(slackUserRef(u, byID))
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		participants = append(participants, pid)
	}
	occurred := slackTime(m.TS)
	if m.LatestReply != "" {
		if t := slackTime(m.LatestReply); !t.IsZero() {
			occurred = t
		}
	}
	return episode.Episode{
		ID:           "slack:" + ch.ID + ":" + m.ThreadTS,
		Source:       "slack",
		Kind:         episode.KindThread,
		Place:        ch.Name,
		PlaceID:      ch.ID,
		Participants: participants,
		Asked:        asked,
		Occurred:     occurred,
		Permalink:    slack.Permalink(workspaceURL, ch.ID, m.TS, ""),
		Messages:     m.ReplyCount + 1,
		// The parent states the problem. enrichThreads folds the replies in
		// afterward, so the stored body also carries how it was solved rather
		// than only how it was asked.
		Body: util.Truncate(m.SearchText(), maxMessageText),
	}
}

// windowEpisode builds an episode from a run of loose messages, which is the
// shape guidance takes when nobody starts a thread. It returns false when the
// run is too short or too solitary to be a conversation. The episode carries
// no asker: whoever spoke first in a stretch of channel chatter did not
// necessarily ask anything, and treating them as the asker would credit
// everyone who spoke after them with answering.
func windowEpisode(
	ch slack.Channel, window []slack.Message, byID map[string]slack.User, workspaceURL string,
) (episode.Episode, bool) {
	if len(window) < minWindowMessages {
		return episode.Episode{}, false
	}
	var participants []model.ID
	seen := make(map[model.ID]bool)
	for _, m := range window {
		pid := model.ID(slackUserRef(m.User, byID))
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		participants = append(participants, pid)
	}
	if len(participants) < minWindowPeople {
		return episode.Episode{}, false
	}
	first, last := window[0], window[len(window)-1]
	return episode.Episode{
		ID:           "slack:" + ch.ID + ":w" + first.TS,
		Source:       "slack",
		Kind:         episode.KindWindow,
		Place:        ch.Name,
		PlaceID:      ch.ID,
		Participants: participants,
		Occurred:     slackTime(last.TS),
		Permalink:    slack.Permalink(workspaceURL, ch.ID, first.TS, ""),
		Messages:     len(window),
		Body:         episodeText(window),
	}, true
}

// episodeText joins the messages of an episode into the text indexed against
// it, capped so one long conversation cannot bloat the store. The text is
// tokenized by the caller and discarded.
func episodeText(msgs []slack.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if b.Len() >= maxEpisodeText {
			break
		}
		b.WriteString(util.Truncate(m.SearchText(), maxMessageText))
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// slackSeconds returns the epoch seconds of a Slack timestamp, keeping the
// fractional part so messages sent in the same second keep their order. It
// returns zero when the timestamp does not parse.
func slackSeconds(ts string) float64 {
	sec, err := strconv.ParseFloat(ts, 64)
	if err != nil || sec <= 0 {
		return 0
	}
	return sec
}
