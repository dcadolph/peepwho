package connector

import (
	"strings"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/jira"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// Episodes returns the resolved issues seen by the most recent Fetch. It is
// empty unless JiraOptions.Episodes was set.
func (j *Jira) Episodes() []episode.Episode { return j.episodes }

// jiraPersonID resolves a Jira user to the identifier the index keys them by:
// their email when the site exposes it, otherwise their account id.
func jiraPersonID(u jira.User) string {
	if u.EmailAddress != "" {
		return util.NormalizeEmail(u.EmailAddress)
	}
	if id := u.Identity(); id != "" {
		return "jira:" + id
	}
	return ""
}

// issueEpisode records a resolved issue: what was wrong, who settled it, and
// a link to the ticket. Open issues are skipped, since they say what is
// wanted rather than how anything was done.
func issueEpisode(baseURL string, is jira.Issue) (episode.Episode, bool) {
	if !is.Resolved() {
		return episode.Episode{}, false
	}
	var participants []model.ID
	seen := make(map[model.ID]bool)
	candidates := []*jira.User{is.Fields.Assignee, is.Fields.Reporter}
	// Comment authors helped work the issue even when they were never assigned,
	// so recall should find them too.
	for _, u := range is.CommentAuthors() {
		candidates = append(candidates, &u)
	}
	for _, u := range candidates {
		if u == nil {
			continue
		}
		id := model.ID(jiraPersonID(*u))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		participants = append(participants, id)
	}
	if len(participants) == 0 {
		return episode.Episode{}, false
	}
	// The reporter raised the issue; the assignee and the comment authors are
	// the ones who answered it. Participants lead with the assignee, so the ask
	// is recorded rather than inferred from a position in that list.
	var asked model.ID
	if is.Fields.Reporter != nil {
		asked = model.ID(jiraPersonID(*is.Fields.Reporter))
	}
	when := jiraTime(is.Fields.ResolutionDate)
	if when.IsZero() {
		when = jiraTime(is.Fields.Updated)
	}
	return episode.Episode{
		ID:           "jira:" + is.Key,
		Source:       "jira",
		Kind:         episode.KindIssue,
		Place:        is.Fields.Project.Key,
		Title:        is.Key + " " + is.Fields.Summary,
		Participants: participants,
		Asked:        asked,
		Occurred:     when,
		Permalink:    strings.TrimSuffix(baseURL, "/") + "/browse/" + is.Key,
		Body: strings.TrimSpace(is.Fields.Summary + " " +
			strings.Join(issueTopics(is), " ") + " " + is.Description()),
	}, true
}
