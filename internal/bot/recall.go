package bot

import (
	"context"
	"errors"
	"strings"

	"github.com/dcadolph/peepwho/internal/recall"
)

// recallPrefix is the word that turns a question about who knows something
// into a question about what you worked through before.
const recallPrefix = "recall"

// noPrivateReply is sent when a recall answer cannot be delivered privately.
// The answer lists the conversations one person took part in, so posting it
// where others can read it is never the fallback.
const noPrivateReply = "I can only answer recall privately, and I cannot send a private " +
	"message here. Ask me in a direct message, or use the /peepwho slash command."

// RecallFunc answers what a user worked through before. The user is the
// identifier the transport knows them by, which the resolver maps to a person,
// so an answer can never be scoped to somebody else.
type RecallFunc func(ctx context.Context, user, query string, limit int) (recall.Answer, error)

// PrivateReplier sends a reply only the asking user can see. A transport that
// cannot do this gets no recall answers at all.
type PrivateReplier interface {
	// ReplyPrivately posts text visible only to user in channel.
	ReplyPrivately(ctx context.Context, channel, user, text string) error
}

// WithRecall enables recall answers, which the bot refuses to give without it.
func WithRecall(fn RecallFunc) EngineOption {
	return func(e *Engine) {
		if fn != nil {
			e.recall = fn
		}
	}
}

// parseRecall reports whether text asks for recall and returns the question
// with the keyword removed. A bare keyword is a recall request with no
// question yet.
func parseRecall(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < len(recallPrefix) ||
		!strings.EqualFold(trimmed[:len(recallPrefix)], recallPrefix) ||
		!isBoundary(trimmed, len(recallPrefix)) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(trimmed[len(recallPrefix):], " \t\n:,")), true
}

// isBoundary reports whether the keyword ends at a word boundary, so a
// question about "recalls" is not mistaken for a recall request.
func isBoundary(s string, at int) bool {
	if at >= len(s) {
		return true
	}
	switch s[at] {
	case ' ', '\t', '\n', ':', ',':
		return true
	default:
		return false
	}
}

// HandleRecall answers a recall request and replies where only the asker can
// see it. A transport that cannot reply privately gets a refusal in its place,
// because the answer describes one person's own conversations.
func (e *Engine) HandleRecall(ctx context.Context, ev Event, r Replier, query string) error {
	private, ok := r.(PrivateReplier)
	if !ok {
		return r.Reply(ctx, ev.Channel, ev.ThreadTS, noPrivateReply)
	}
	if query == "" {
		return private.ReplyPrivately(ctx, ev.Channel, ev.User,
			"Tell me what to look for, like `recall certificate renewal`.")
	}
	allowed, warn := e.allow(ev.User)
	if warn {
		return private.ReplyPrivately(ctx, ev.Channel, ev.User, rateWarning)
	}
	if !allowed {
		return nil
	}
	text, err := e.Recall(ctx, ev.User, query, recallLimit)
	if text == "" {
		return err
	}
	return errors.Join(err, private.ReplyPrivately(ctx, ev.Channel, ev.User, text))
}

// Recall answers what user worked through before. An empty reply means there
// is nothing to say. A non-nil error reports a failure the reply already
// apologizes for.
func (e *Engine) Recall(ctx context.Context, user, query string, limit int) (string, error) {
	if e.recall == nil {
		return "Recall is not enabled on this bot.", nil
	}
	if limit <= 0 {
		limit = e.limit
	}
	ans, err := e.recall(ctx, user, query, limit)
	if err != nil {
		return sorryReply, err
	}
	return FormatRecall(query, ans), nil
}

// FormatRecall renders a recall answer as Slack mrkdwn: who was with you,
// where, when, and a link back. Every value from the graph is escaped, and the
// only links are the ones peepwho built itself.
func FormatRecall(query string, ans recall.Answer) string {
	if len(ans.Episodes) == 0 {
		return "Nothing found for *" + escapeMrkdwn(query) + "* in the conversations you took " +
			"part in. " + escapeMrkdwn(ans.Scope.Note)
	}
	var b strings.Builder
	b.WriteString("*You worked on this before*\n")
	for _, ep := range ans.Episodes {
		b.WriteString("• ")
		b.WriteString(peopleLine(ep))
		if ep.Place != "" {
			b.WriteString(" in #" + escapeMrkdwn(ep.Place))
		}
		if !ep.When.IsZero() {
			b.WriteString(" on " + ep.When.Format("January 2, 2006"))
		}
		if link := mrkdwnLink(ep.Permalink, "open the conversation"); link != "" {
			b.WriteString(" · " + link)
		}
		if ep.LinkMayHaveExpired {
			b.WriteString(" _(old enough that the link may no longer resolve)_")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n_" + escapeMrkdwn(ans.Scope.Note) + "_")
	return b.String()
}

// peopleLine names who else was in a conversation, or says it was just the
// asker when nobody else took part.
func peopleLine(ep recall.Episode) string {
	if len(ep.People) == 0 {
		return "on your own"
	}
	names := make([]string, 0, len(ep.People))
	for _, p := range ep.People {
		name := p.Name
		if name == "" {
			name = p.Email
		}
		if name == "" {
			name = p.ID
		}
		names = append(names, escapeMrkdwn(name))
	}
	if len(names) == 1 {
		return "with " + names[0]
	}
	return "with " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// mrkdwnLink renders a Slack link, and nothing at all for a URL that is not a
// plain https link, so no value can break out of the link syntax.
func mrkdwnLink(url, label string) string {
	if !strings.HasPrefix(url, "https://") || strings.ContainsAny(url, "<>|") {
		return ""
	}
	return "<" + url + "|" + escapeMrkdwn(label) + ">"
}

// recallLimit is how many past conversations a Slack answer lists, kept short
// because the reader is scanning for the one they half remember.
const recallLimit = 3
