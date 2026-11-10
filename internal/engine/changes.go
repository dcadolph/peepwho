package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/util"
	"github.com/dcadolph/whotoask/internal/vault"
)

// MaxChangeEntries caps the change log. A save keeps the newest entries.
const MaxChangeEntries = 100

// ChangeEntry is what one fold changed, stamped with when the fold ran.
type ChangeEntry struct {
	// At is the indexer clock's time when the fold ran.
	At time.Time `json:"at"`
	// Changes lists who and what joined or left in that fold.
	Changes index.Changes `json:"changes"`
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

// readChanges reads the change log at path under codec, oldest entry first. A
// missing file is an empty log. With a nil codec a sealed file returns an
// error matching vault.ErrEncrypted, so the caller can point at the key.
func readChanges(path string, codec vault.Codec) ([]ChangeEntry, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("change log: read %s: %w", path, err)
	}
	if codec == nil {
		codec = vault.Plain{}
	}
	if raw, err = codec.Decode(raw); err != nil {
		return nil, fmt.Errorf("change log: decode %s: %w", path, err)
	}
	var entries []ChangeEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("change log: parse %s: %w", path, err)
	}
	return entries, nil
}

// writeChanges writes the newest MaxChangeEntries entries to path atomically,
// sealed under codec when it is set.
func writeChanges(path string, entries []ChangeEntry, codec vault.Codec) error {
	if len(entries) > MaxChangeEntries {
		entries = entries[len(entries)-MaxChangeEntries:]
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("change log: encode: %w", err)
	}
	if codec != nil {
		if raw, err = codec.Encode(raw); err != nil {
			return fmt.Errorf("change log: seal: %w", err)
		}
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("change log: write %s: %w", path, err)
	}
	return nil
}

// ForgetChangeLabels strips every person label matching one of labels, after
// trimming and case folding, from the joined and left lists of each entry, and
// drops entries the strip leaves empty. It returns the kept entries and how
// many entries it edited or dropped.
func ForgetChangeLabels(entries []ChangeEntry, labels []string) ([]ChangeEntry, int) {
	drop := make(map[string]bool, len(labels))
	for _, l := range labels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			drop[l] = true
		}
	}
	strip := func(in []string) ([]string, bool) {
		var out []string
		for _, l := range in {
			if !drop[strings.ToLower(strings.TrimSpace(l))] {
				out = append(out, l)
			}
		}
		return out, len(out) != len(in)
	}
	kept := make([]ChangeEntry, 0, len(entries))
	edited := 0
	for _, e := range entries {
		var joined, left bool
		e.Changes.PeopleJoined, joined = strip(e.Changes.PeopleJoined)
		e.Changes.PeopleLeft, left = strip(e.Changes.PeopleLeft)
		if joined || left {
			edited++
		}
		if !e.Changes.Empty() {
			kept = append(kept, e)
		}
	}
	return kept, edited
}
