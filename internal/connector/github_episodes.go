package connector

import (
	"strconv"
	"strings"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/github"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/util"
)

// maxChangeBody caps the pull request description taken into the searchable
// text. Descriptions carry the reason for a change, but they also carry
// templates and generated checklists, so they are cut rather than indexed
// whole.
const maxChangeBody = 8000

// Episodes returns the merged changes seen by the most recent Fetch, newest
// first. It is empty unless GitHubOptions.Episodes was set.
func (g *GitHub) Episodes() []episode.Episode { return g.episodes }

// changeEpisode records a merged pull request: a change that actually landed,
// who wrote and reviewed it, and a link to the discussion. Unmerged work is
// skipped, since it is a proposal rather than a record of how something was
// done.
func changeEpisode(owner, repo string, pr github.PullRequest, extra []string) (episode.Episode, bool) {
	if !pr.Merged() {
		return episode.Episode{}, false
	}
	// extra holds the people who actually reviewed or commented, past the list
	// object's author, requested reviewers, and assignees. Dedup across both.
	participants := make([]model.ID, 0, len(pr.People())+len(extra))
	seen := make(map[model.ID]bool)
	for _, login := range append(pr.People(), extra...) {
		if login == "" {
			continue
		}
		id := model.ID("github:" + strings.ToLower(login))
		if seen[id] {
			continue
		}
		seen[id] = true
		participants = append(participants, id)
	}
	if len(participants) == 0 {
		return episode.Episode{}, false
	}
	// The author put the change up for judgment; the reviewers are the ones who
	// answered. pr.People() already leads with the author, but that is an order,
	// not a statement, and an order is what breaks silently when it changes.
	var asked model.ID
	if login := pr.Author(); login != "" {
		asked = model.ID("github:" + strings.ToLower(login))
	}
	return episode.Episode{
		ID:           "github:" + owner + "/" + repo + ":" + strconv.Itoa(pr.Number),
		Source:       "github",
		Kind:         episode.KindChange,
		Place:        owner + "/" + repo,
		Title:        pr.Title,
		Participants: participants,
		Asked:        asked,
		Occurred:     pr.MergedAt,
		Permalink:    pr.HTMLURL,
		Body: strings.TrimSpace(pr.Title + " " + strings.Join(pr.LabelNames(), " ") +
			" " + util.Truncate(pr.Body, maxChangeBody)),
	}, true
}
