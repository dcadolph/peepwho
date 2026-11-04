package bot

import (
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/resolve"
)

// TestFormatEscapesMrkdwn verifies user-derived text cannot inject Slack
// broadcasts or mentions: the raw query, the summary, and person and channel
// fields are all escaped before they reach the mrkdwn reply.
func TestFormatEscapesMrkdwn(t *testing.T) {
	t.Parallel()

	// The no-match path still echoes the query and must escape it.
	noMatch := Format("<!channel> ping everyone", resolve.Answer{})
	if strings.Contains(noMatch, "<!channel>") {
		t.Errorf("no-match reply leaked a raw broadcast: %q", noMatch)
	}
	if !strings.Contains(noMatch, "&lt;!channel&gt;") {
		t.Errorf("no-match reply did not escape the query: %q", noMatch)
	}

	ans := resolve.Answer{
		Summary: "Talk to <!here>.",
		People: []model.Match{{
			Person: &model.Person{Name: "<!channel>", Title: "R&D <lead>"},
			Team:   &model.Team{Name: "A&B"},
		}},
		Channels: []model.ChannelMatch{{
			Channel: &model.Channel{Name: "<!everyone>", Topic: "1 > 0"},
		}},
	}
	got := Format("who knows <@U123>", ans)
	for _, raw := range []string{"<!channel>", "<!here>", "<!everyone>", "<@U123>", "<lead>"} {
		if strings.Contains(got, raw) {
			t.Errorf("formatted reply leaked raw mrkdwn %q:\n%s", raw, got)
		}
	}
	// A bare & from user data must become an entity, not stay a live control char.
	if strings.Contains(got, "A&B") || strings.Contains(got, "R&D") {
		t.Errorf("formatted reply left a raw ampersand:\n%s", got)
	}
	if !strings.Contains(got, "&lt;!channel&gt;") || !strings.Contains(got, "A&amp;B") {
		t.Errorf("formatted reply missing expected escapes:\n%s", got)
	}
}
