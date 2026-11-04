package github

import (
	"context"
	"encoding/json"
	"net/url"
	"time"
)

// Issue is the subset of an issue peepwho reads. The GitHub issues endpoint also
// returns pull requests; IsPullRequest distinguishes them.
type Issue struct {
	// Title is the issue title.
	Title string `json:"title"`
	// User is the author.
	User account `json:"user"`
	// Assignees are users assigned to the issue.
	Assignees []account `json:"assignees"`
	// Labels are the applied labels.
	Labels []label `json:"labels"`
	// PullRequest is present when the issue is actually a pull request.
	PullRequest json.RawMessage `json:"pull_request"`
	// UpdatedAt is when the issue last changed.
	UpdatedAt time.Time `json:"updated_at"`
}

// IsPullRequest reports whether this issue is really a pull request.
func (i Issue) IsPullRequest() bool { return len(i.PullRequest) > 0 }

// Author returns the issue author's login.
func (i Issue) Author() string { return i.User.Login }

// LabelNames returns the label names.
func (i Issue) LabelNames() []string { return labelNames(i.Labels) }

// AssigneeLogins returns the assignees' logins.
func (i Issue) AssigneeLogins() []string { return accountLogins(i.Assignees) }

// Issues returns a repository's issues of any state, most recently updated
// first, following pagination up to maxPages pages of 100. The result includes
// pull requests, which the caller can filter with IsPullRequest. A non-zero
// since restricts the result to issues updated at or after it, which an
// incremental re-index passes to fetch only what changed.
func (c *Client) Issues(ctx context.Context, owner, repo string, since time.Time) ([]Issue, error) {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	return getAll[Issue](ctx, c, repoPath(owner, repo, "issues"), q)
}
