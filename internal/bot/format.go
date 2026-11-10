package bot

import (
	"strings"

	"github.com/dcadolph/whotoask/internal/resolve"
)

// Format renders an answer as Slack mrkdwn. Every value drawn from the query or
// the graph is escaped, so text like <!channel> or <@Uxxx> renders as literal
// characters rather than a workspace-wide broadcast or a mention.
func Format(query string, ans resolve.Answer) string {
	if len(ans.People) == 0 && len(ans.Channels) == 0 {
		return "No matches for *" + escapeMrkdwn(query) + "*. Try different words."
	}

	var b strings.Builder
	if ans.Summary != "" {
		b.WriteString(escapeMrkdwn(ans.Summary))
		b.WriteString("\n\n")
	}

	if len(ans.People) > 0 {
		b.WriteString("*People*\n")
		for _, m := range ans.People {
			name := m.Person.Name
			if name == "" {
				name = m.Person.Email
			}
			line := "• " + escapeMrkdwn(name)
			if m.Person.Title != "" {
				line += " - " + escapeMrkdwn(m.Person.Title)
			}
			if m.Team != nil && m.Team.Name != "" {
				line += " (" + escapeMrkdwn(m.Team.Name) + ")"
			}
			if label := resolve.StrengthLabel(m.Strength); label != "" {
				line += " · " + label + " match"
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if len(ans.Channels) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("*Channels*\n")
		for _, c := range ans.Channels {
			line := "• #" + escapeMrkdwn(c.Channel.Name)
			if c.Channel.Topic != "" {
				line += " - " + escapeMrkdwn(c.Channel.Topic)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// escapeMrkdwn escapes the three characters Slack treats as mrkdwn control
// characters. The ampersand is replaced first, so the entities the other two
// introduce are not escaped again.
func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
