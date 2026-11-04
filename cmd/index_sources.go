// What surrounds a source read: fetch errors explained in the source's own
// vocabulary, the change report, and the git position saved between runs.

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/util"
)

// explainSourceError turns a low-level fetch failure into an actionable message
// naming the source and the credential to check. It is the engine's
// ExplainSourceError, named here for the commands that share it.
func explainSourceError(source, tokenEnv string, err error) error {
	return engine.ExplainSourceError(source, tokenEnv, err)
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
