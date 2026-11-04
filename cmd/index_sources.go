// What surrounds a source read: fetch errors explained in the source's own
// vocabulary, the change report, and the git position saved between runs.

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/slack"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/util"
)

// explainSourceError turns a low-level fetch failure into an actionable message
// naming the source and the credential to check. A token that expired or lost
// a scope otherwise surfaces as a bare status code next to an internal API
// path, which tells a user nothing about what to do. Other errors pass through.
func explainSourceError(source, tokenEnv string, err error) error {
	var se *httputil.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf(
			"%w: %s rejected the credentials (HTTP %d). The token in %s is missing, expired, or "+
				"lacks the read access this needs. Recreate it and set %s, then try again",
			ErrAuth, source, se.Code, tokenEnv, tokenEnv)
	case http.StatusNotFound:
		return fmt.Errorf(
			"%w: %s returned not found (HTTP 404). Check the site URL is the root with no path, and "+
				"that the names you scoped to exist", ErrAuth, source)
	}
	return err
}

// explainSlackError turns a Slack auth or scope failure into an actionable
// message. Slack signals these as an ok=false code such as invalid_auth rather
// than an HTTP status, so explainSourceError, which reads status codes, never
// catches them; without this a descoped token surfaces as a bare code next to an
// internal method name.
func explainSlackError(err error) error {
	if err == nil || !errors.Is(err, slack.ErrAPI) {
		return err
	}
	code := "error"
	if _, c, ok := strings.Cut(err.Error(), "api error: "); ok {
		code = strings.TrimSpace(c)
	}
	switch {
	case strings.Contains(code, "invalid_auth"), strings.Contains(code, "not_authed"),
		strings.Contains(code, "token_revoked"), strings.Contains(code, "account_inactive"):
		return fmt.Errorf(
			"%w: Slack rejected the token in %s (%s). It is missing, expired, or revoked. Recreate "+
				"the bot token and set %s, then try again", ErrAuth, slackTokenEnv, code, slackTokenEnv)
	case strings.Contains(code, "missing_scope"), strings.Contains(code, "not_allowed_token_type"):
		return fmt.Errorf(
			"%w: the Slack token in %s is missing a required scope (%s). Add users:read, "+
				"users:read.email, channels:read, and channels:history in your Slack app and reinstall",
			ErrAuth, slackTokenEnv, code)
	}
	return err
}

// explainFetchError explains a fetch failure in the vocabulary of the source
// that produced it: a Slack API code, or a rejected credential for a source
// whose registry entry names one. Other errors pass through.
func explainFetchError(spec engine.SourceSpec, err error) error {
	if errors.Is(err, slack.ErrAPI) {
		return explainSlackError(err)
	}
	if spec.AuthEnv == "" {
		return err
	}
	return explainSourceError(spec.AuthName, spec.AuthEnv, err)
}

// reportChanges prints a one-line summary and capped lists of who and what
// joined or left since the last index.
func reportChanges(w io.Writer, c index.Changes) {
	if c.Empty() {
		return
	}
	fmt.Fprintf(w, "changes since last index: %s\n", c.Summary())
	printChangeList(w, "joined", c.PeopleJoined)
	printChangeList(w, "left", c.PeopleLeft)
	printChangeList(w, "new channels", c.ChannelsAdded)
	printChangeList(w, "gone channels", c.ChannelsRemoved)
}

// printChangeList prints up to a fixed number of items under a label, noting
// any remainder.
func printChangeList(w io.Writer, label string, items []string) {
	const limit = 15
	if len(items) == 0 {
		return
	}
	shown := items
	if len(shown) > limit {
		shown = shown[:limit]
	}
	fmt.Fprintf(w, "  %s: %s", label, strings.Join(shown, ", "))
	if len(items) > limit {
		fmt.Fprintf(w, ", and %d more", len(items)-limit)
	}
	fmt.Fprintln(w)
}

// writeChangesFile writes the changes as indented JSON to path atomically.
func writeChangesFile(path string, c index.Changes) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("changes file: %w", err)
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("changes file: %w", err)
	}
	return nil
}

// gitMarks returns where a previous run stopped in each repository, so reading
// resumes there. It is empty unless this is a merge, since a full read is meant
// to start over, and empty on the first run, when there is nothing to resume
// from.
func gitMarks(opts *options, merge bool, paths []string) map[string]string {
	if !merge || !indexExists(opts) {
		return nil
	}
	st, err := opts.loadState()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if wm, ok := st.Get("git", "repo:"+p); ok && wm.Mark != "" {
			out[p] = wm.Mark
		}
	}
	return out
}

// saveGitMarks records where reading stopped in each repository. Each is its own
// watermark, since one run may cover several repositories and they advance
// independently.
func saveGitMarks(opts *options, marks map[string]string) error {
	if len(marks) == 0 {
		return nil
	}
	unlock, err := util.LockFile(opts.statePath() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := opts.loadState()
	if err != nil {
		return err
	}
	for path, mark := range marks {
		st.Set(state.Watermark{
			Source: "git", Scope: "repo:" + path, Mark: mark,
			Complete: true, RanAt: opts.now(),
		})
	}
	return opts.saveState(st)
}
