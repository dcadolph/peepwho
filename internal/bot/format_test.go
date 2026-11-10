package bot

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
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

// TestFormatTeams verifies the teams of record get their own section, with the
// org chart team beside a handle when one matches, and that an answer holding
// only teams is an answer rather than a miss.
func TestFormatTeams(t *testing.T) {
	t.Parallel()
	squad := model.Match{
		Person: &model.Person{ID: "codeowners:acme/legal-squad", Name: "@acme/legal-squad"},
		Team:   &model.Team{Name: "Legal"},
	}
	bare := model.Match{
		Person: &model.Person{ID: "codeowners:acme/default-maintainers", Name: "@acme/default-maintainers"},
	}
	person := model.Match{Person: &model.Person{Name: "Carla Counsel"}}
	tests := []struct {
		WantResult string
		Ans        resolve.Answer
	}{{ // Test 0: Teams alone.
		Ans:        resolve.Answer{Teams: []model.Match{squad, bare}},
		WantResult: "*Teams (owners of record)*\n• @acme/legal-squad (Legal)\n• @acme/default-maintainers",
	}, { // Test 1: Teams follow the people.
		Ans: resolve.Answer{People: []model.Match{person}, Teams: []model.Match{squad}},
		WantResult: "*People*\n• Carla Counsel\n\n" +
			"*Teams (owners of record)*\n• @acme/legal-squad (Legal)",
	}, { // Test 2: No teams, no section.
		Ans:        resolve.Answer{People: []model.Match{person}},
		WantResult: "*People*\n• Carla Counsel",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantResult, Format("legal", test.Ans)); diff != "" {
				t.Errorf("Format mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
