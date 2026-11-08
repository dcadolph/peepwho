package simorg

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/dcadolph/peepwho/internal/model"
)

// IdentitySpec describes a company built to break identity resolution. Every
// field plants a specific trap that a people graph gets wrong in a real
// organization, at whatever volume is asked for.
type IdentitySpec struct {
	// People is how many ordinary humans work here. Each appears in the org
	// chart, in Slack, and in CODEOWNERS under a different identifier, and all
	// three must resolve to one person.
	People int
	// SharedNames is how many pairs of DIFFERENT people are given the same
	// display name. Merging either pair is the worst failure a people tool
	// has: it hands one person's work and conversations to another.
	SharedNames int
	// RoleAccounts is how many shared mailboxes, such as deploy@ or oncall@,
	// post like people. They must never merge into a human.
	RoleAccounts int
	// CrossTeamTalkers is how many people are made active in channels
	// belonging to teams other than their own. Their team must not drift.
	CrossTeamTalkers int
	// Seed fixes the randomness.
	Seed int64
}

// withDefaults fills a spec out.
func (s IdentitySpec) withDefaults() IdentitySpec {
	if s.People <= 0 {
		s.People = 60
	}
	if s.SharedNames <= 0 {
		s.SharedNames = 8
	}
	if s.RoleAccounts <= 0 {
		s.RoleAccounts = 4
	}
	if s.CrossTeamTalkers <= 0 {
		s.CrossTeamTalkers = 12
	}
	if s.Seed == 0 {
		s.Seed = 1
	}
	return s
}

// IdentityOrg is a company with identity traps, and the truth about who is
// actually who.
type IdentityOrg struct {
	// Slack serves the workspace.
	Slack *httptest.Server
	// CSV is the org chart.
	CSV string
	// CodeOwners is a CODEOWNERS file naming people by handle only, which is
	// the identifier style that has to be matched by name rather than email.
	CodeOwners string
	// MustMerge lists identifier sets that are one human. Every identifier in
	// a set must resolve to the same person.
	MustMerge [][]model.ID
	// MustNotMerge lists identifiers belonging to different humans. No two may
	// ever resolve to the same person.
	MustNotMerge [][]model.ID
	// WantTeam is the team each person belongs to according to the org chart,
	// which no amount of talking elsewhere may change.
	WantTeam map[model.ID]string
}

// Close shuts down the servers.
func (o *IdentityOrg) Close() {
	if o.Slack != nil {
		o.Slack.Close()
	}
}

// GenerateIdentity builds a company designed to break identity resolution.
// Peepwho cannot be tested against a real workspace, so the traps a real
// workspace contains are planted deliberately and at volume: people who share
// a name, people scattered across three identifier styles, shared mailboxes
// that act like people, and people who talk everywhere but belong to one team.
func GenerateIdentity(spec IdentitySpec) *IdentityOrg {
	spec = spec.withDefaults()

	var (
		csv        strings.Builder
		owners     strings.Builder
		users      []map[string]any
		history    = map[string][]map[string]any{}
		org        = &IdentityOrg{WantTeam: map[model.ID]string{}}
		distinct   []model.ID
		channelIDs []string
	)
	csv.WriteString("name,email,title,team,org,manager,topics\n")

	// Channels named after teams, so a person talking in the wrong one is a
	// realistic way for team assignment to drift.
	for i := range teams {
		id := fmt.Sprintf("C%03d", i)
		channelIDs = append(channelIDs, id)
		history[id] = nil
	}

	// Ordinary people: org chart by email, Slack by user id, CODEOWNERS by a
	// bare handle. All three are the same human and must end up as one.
	for i := range spec.People {
		given := givenNames[i%len(givenNames)]
		family := familyNames[(i/len(givenNames)+i)%len(familyNames)]
		name := given + " " + family
		email := fmt.Sprintf("%s.%s%d@example.com",
			strings.ToLower(asciiFold(given)), strings.ToLower(asciiFold(family)), i)
		handle := fmt.Sprintf("%s-%s%d",
			strings.ToLower(asciiFold(given)), strings.ToLower(asciiFold(family)), i)
		team := teams[i%len(teams)]
		slackID := fmt.Sprintf("U%04d", i)

		fmt.Fprintf(&csv, "%s,%s,Engineer,%s,Engineering,,\n", name, email, team)
		fmt.Fprintf(&owners, "/svc/%s/ @%s\n", handle, handle)
		users = append(users, slackUser(slackID, name, email, "Engineer"))

		home := channelIDs[i%len(channelIDs)]
		history[home] = append(history[home],
			slackMessage(slackID, "working on the "+strings.ToLower(team)+" side", daysAgo(i%30+1)))

		org.MustMerge = append(org.MustMerge, []model.ID{
			model.ID(email), model.ID("slack:" + slackID), model.ID("codeowners:" + handle),
		})
		org.WantTeam[model.ID(email)] = team
		distinct = append(distinct, model.ID(email))
	}

	// People who share a display name. They are different humans with
	// different emails, and one of them has a matching handle in CODEOWNERS,
	// which is exactly the bait for a name-based join.
	for i := range spec.SharedNames {
		name := "Sam Taylor"
		if i%2 == 1 {
			name = "Alex Rivera"
		}
		name = fmt.Sprintf("%s%d", name, i/2)
		flat := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		aEmail := fmt.Sprintf("twin.a%d@example.com", i)
		bEmail := fmt.Sprintf("twin.b%d@example.com", i)
		aSlack := fmt.Sprintf("T%04da", i)
		bSlack := fmt.Sprintf("T%04db", i)

		fmt.Fprintf(&csv, "%s,%s,Engineer,%s,Engineering,,\n", name, aEmail, teams[i%len(teams)])
		fmt.Fprintf(&csv, "%s,%s,Engineer,%s,Engineering,,\n", name, bEmail, teams[(i+1)%len(teams)])
		users = append(users, slackUser(aSlack, name, aEmail, "Engineer"))
		users = append(users, slackUser(bSlack, name, bEmail, "Engineer"))
		fmt.Fprintf(&owners, "/twin/%d/ @%s\n", i, flat)

		home := channelIDs[i%len(channelIDs)]
		history[home] = append(history[home],
			slackMessage(aSlack, "shipping the change", daysAgo(3)),
			slackMessage(bSlack, "reviewing the change", daysAgo(4)))

		org.MustNotMerge = append(org.MustNotMerge, []model.ID{model.ID(aEmail), model.ID(bEmail)})
		org.WantTeam[model.ID(aEmail)] = teams[i%len(teams)]
		org.WantTeam[model.ID(bEmail)] = teams[(i+1)%len(teams)]
		distinct = append(distinct, model.ID(aEmail), model.ID(bEmail))
	}

	// Shared mailboxes that talk like people. They are not humans and must not
	// absorb one.
	roles := []string{"deploy", "oncall", "alerts", "releases", "security-bot", "status"}
	for i := range spec.RoleAccounts {
		// Names wrap by region once the list runs out, so asking for more role
		// accounts than there are names still yields distinct mailboxes rather
		// than the same one twice.
		role := roles[i%len(roles)]
		if i >= len(roles) {
			role = fmt.Sprintf("%s-%d", role, i/len(roles))
		}
		email := role + "@example.com"
		slackID := fmt.Sprintf("R%04d", i)
		users = append(users, slackUser(slackID, role, email, "Service Account"))
		home := channelIDs[i%len(channelIDs)]
		history[home] = append(history[home],
			slackMessage(slackID, "automated notice for every team", daysAgo(1)))
		distinct = append(distinct, model.ID(email))
	}

	// People who talk everywhere. Their team comes from the org chart and must
	// survive being active in seven other teams' channels.
	for i := range spec.CrossTeamTalkers {
		if i >= spec.People {
			break
		}
		slackID := fmt.Sprintf("U%04d", i)
		for _, ch := range channelIDs {
			history[ch] = append(history[ch],
				slackMessage(slackID, "chiming in from elsewhere", daysAgo(i%20+1)))
		}
	}

	// Every distinct person must stay distinct from every other one.
	org.MustNotMerge = append(org.MustNotMerge, distinct)
	org.CSV = csv.String()
	org.CodeOwners = owners.String()
	org.Slack = identitySlackServer(users, channelIDs, history)
	return org
}

// identitySlackServer serves the identity workspace.
func identitySlackServer(
	users []map[string]any, channelIDs []string, history map[string][]map[string]any,
) *httptest.Server {
	channels := make([]map[string]any, 0, len(channelIDs))
	for i, id := range channelIDs {
		channels = append(channels, slackChannel(id, strings.ToLower(
			strings.ReplaceAll(teams[i%len(teams)], " ", "-")), teams[i%len(teams)]+" team channel"))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "members": users})
	})
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "channels": channels})
	})
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "user_id": "U9999", "url": "https://corp.slack.com/"})
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"ok": true, "has_more": false, "messages": history[r.Form.Get("channel")],
		})
	})
	return httptest.NewServer(mux)
}
