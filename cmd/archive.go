package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/episode"
)

// newArchiveCmd builds the archive command group, which reports and prunes the
// conversation content peepwho keeps.
func newArchiveCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Report and prune the conversations peepwho keeps",
		Long: `Report and prune the conversations peepwho keeps.

Recall points back at past conversations on every install. With --archive at
index time the words of those conversations are kept too, on this machine,
beside the index, so an answer can show how something was worked out after the
source has aged the messages out. Set a key with peepwho vault and that file is
encrypted alongside the index; without one it is plain JSON readable only by you.

Retention is yours to set. Nothing here contacts a server, and nothing is
deleted unless you ask.`,
	}
	cmd.AddCommand(newArchiveStatusCmd(opts), newArchivePruneCmd(opts))
	return cmd
}

// newArchiveStatusCmd builds the status subcommand.
func newArchiveStatusCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report what is kept and how far back",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			kept, oldest, newest := archiveStats(store)
			return writeJSON(cmd.OutOrStdout(), struct {
				// Conversations is how many are remembered.
				Conversations int `json:"conversations"`
				// WithContent is how many keep their content, not just a link.
				WithContent int `json:"with_content"`
				// Oldest is the earliest conversation held.
				Oldest string `json:"oldest,omitempty"`
				// Newest is the most recent conversation held.
				Newest string `json:"newest,omitempty"`
				// Path is where the conversations are stored.
				Path string `json:"path"`
			}{
				Conversations: store.Len(),
				WithContent:   kept,
				Oldest:        dateText(oldest),
				Newest:        dateText(newest),
				Path:          opts.episodePath(),
			}, opts.pretty)
		},
	}
}

// newArchivePruneCmd builds the prune subcommand.
func newArchivePruneCmd(opts *options) *cobra.Command {
	var (
		olderThan int
		content   bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete remembered conversations, or just their content",
		Long: `Delete remembered conversations, or just their content.

  peepwho archive prune --older-than-days 365   forget conversations over a year old
  peepwho archive prune --content-only          keep the links, drop the content

This is the only command that deletes remembered conversations, and it deletes
exactly what you name.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if olderThan <= 0 && !content {
				return fmt.Errorf(
					"%w: name what to prune: --older-than-days or --content-only", ErrBadArgs)
			}
			store, err := opts.loadEpisodes(cmd)
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			if content {
				fmt.Fprintf(out, "dropped the content of %d conversations, keeping their links\n",
					store.PurgeArchive())
			}
			if olderThan > 0 {
				cutoff := time.Now().AddDate(0, 0, -olderThan)
				fmt.Fprintf(out, "forgot %d conversations older than %s\n",
					store.PurgeBefore(cutoff), cutoff.Format(time.DateOnly))
			}
			return opts.saveEpisodes(store)
		},
	}
	f := cmd.Flags()
	f.IntVar(&olderThan, "older-than-days", 0, "Forget conversations older than this many days.")
	f.BoolVar(&content, "content-only", false, "Drop retained content but keep the links.")
	return cmd
}

// archiveStats counts conversations that keep their content and the span they
// cover.
func archiveStats(store *episode.Store) (withContent int, oldest, newest time.Time) {
	for _, ep := range store.All() {
		if ep.Archived() {
			withContent++
		}
		if ep.Occurred.IsZero() {
			continue
		}
		if oldest.IsZero() || ep.Occurred.Before(oldest) {
			oldest = ep.Occurred
		}
		if ep.Occurred.After(newest) {
			newest = ep.Occurred
		}
	}
	return withContent, oldest, newest
}

// dateText renders a date, or the empty string for the zero time.
func dateText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.DateOnly)
}
