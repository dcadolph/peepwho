package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/render"
)

// newFeedbackCmd builds the feedback command group: record a vote, review
// what has been recorded, and clear votes that no longer apply.
func newFeedbackCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feedback",
		Short: "Confirm, correct, review, or clear answer feedback",
		Long: `Votes teach the ranking: a helpful vote lifts a result for that question and
its close variants, a not-helpful vote lowers it, capped so feedback tunes
answers without burying the evidence. Votes live in feedback.json next to the
index and survive re-indexing.

  whotoask feedback record "billing retries" --person alice@example.com --helpful
  whotoask feedback list
  whotoask feedback clear --person alice@example.com`,
	}
	cmd.AddCommand(newFeedbackRecordCmd(opts), newFeedbackListCmd(opts), newFeedbackClearCmd(opts),
		newFeedbackBundleCmd(opts), newFeedbackSummaryCmd(opts))
	return cmd
}

// newFeedbackRecordCmd builds the record subcommand.
func newFeedbackRecordCmd(opts *options) *cobra.Command {
	var (
		person     string
		channel    string
		comment    string
		helpful    bool
		notHelpful bool
	)
	cmd := &cobra.Command{
		Use:   "record [question]",
		Short: "Record a vote on an answer",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if helpful == notHelpful {
				return fmt.Errorf("%w: pass exactly one of --helpful or --not-helpful", ErrBadArgs)
			}
			vote := feedback.Helpful
			if notHelpful {
				vote = feedback.NotHelpful
			}
			store, err := opts.loadFeedback()
			if err != nil {
				return err
			}
			entry := feedback.Entry{
				Query:   strings.Join(args, " "),
				Person:  person,
				Channel: channel,
				Vote:    vote,
				Comment: strings.TrimSpace(comment),
				Time:    opts.now(),
			}
			if err := store.Add(entry); err != nil {
				return fmt.Errorf("%w: %w", ErrBadArgs, err)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "feedback recorded")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&person, "person", "", "Person identifier from the answer.")
	f.StringVar(&channel, "channel", "", "Channel name from the answer.")
	f.StringVar(&comment, "comment", "", "Optional note explaining the vote.")
	f.BoolVar(&helpful, "helpful", false, "The result answered the question.")
	f.BoolVar(&notHelpful, "not-helpful", false, "The result was wrong for the question.")
	return cmd
}

// newFeedbackListCmd builds the list subcommand.
func newFeedbackListCmd(opts *options) *cobra.Command {
	var filter feedback.Filter
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show recorded votes as JSON",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := opts.loadFeedback()
			if err != nil {
				return err
			}
			entries := store.List(filter)
			if entries == nil {
				entries = []feedback.Entry{}
			}
			return opts.render(cmd.OutOrStdout(), entries, func(w io.Writer, s style) {
				render.RenderFeedback(w, entries, s)
			})
		},
	}
	addFeedbackFilterFlags(cmd, &filter)
	return cmd
}

// newFeedbackClearCmd builds the clear subcommand.
func newFeedbackClearCmd(opts *options) *cobra.Command {
	var (
		filter feedback.Filter
		all    bool
	)
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Remove recorded votes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !all && filter == (feedback.Filter{}) {
				return fmt.Errorf("%w: pass --query, --person, --channel, or --all", ErrBadArgs)
			}
			store, err := opts.loadFeedback()
			if err != nil {
				return err
			}
			removed, err := store.Clear(filter)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "cleared %d votes\n", removed)
			return nil
		},
	}
	addFeedbackFilterFlags(cmd, &filter)
	cmd.Flags().BoolVar(&all, "all", false, "Clear every recorded vote.")
	return cmd
}

// addFeedbackFilterFlags registers the shared vote filter flags.
func addFeedbackFilterFlags(cmd *cobra.Command, filter *feedback.Filter) {
	f := cmd.Flags()
	f.StringVar(&filter.Query, "query", "", "Match votes for this exact question.")
	f.StringVar(&filter.Person, "person", "", "Match votes on this person identifier.")
	f.StringVar(&filter.Channel, "channel", "", "Match votes on this channel name.")
}

// applyFeedback loads stored votes onto the engine's ranking, warning instead
// of failing when the feedback file is unreadable. It returns the store, or nil
// when the file could not be read.
func applyFeedback(eng *engine.Engine, opts *options, errOut io.Writer) *feedback.Store {
	store, err := opts.loadFeedback()
	if err != nil {
		fmt.Fprintf(errOut, "feedback ignored: %v\n", err)
		return nil
	}
	eng.SetFeedback(store)
	return store
}

// newFeedbackBundleCmd builds the bundle subcommand, which composes the
// redacted report a user can choose to hand to whotoask's makers.
//
// Composing and sending are deliberately two different acts. This command only
// writes a file, so the claim that every byte can be read before it leaves is
// not a promise about behavior but a property of the design: there is nothing
// here that could send.
func newFeedbackBundleCmd(opts *options) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write the redacted feedback report, to read and send by hand",
		Long: `Compose a file summarizing the feedback recorded on this machine: vote counts,
the shape of the questions voted on, and the comments typed with the votes.
Queries, names, addresses, and message text never appear in it; a question
asked of whotoask is itself a fact about your organization, so only its word
count leaves. The file is written to disk and nothing more: read it, then
attach it to a GitHub issue or an email yourself, or do neither.

An organization can forbid even this with "feedback_bundle": "deny" in its
policy file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !opts.pol.AllowFeedbackBundle() {
				return fmt.Errorf("%w: the organization policy pins the feedback bundle off", ErrPolicy)
			}
			store, err := opts.loadFeedback()
			if err != nil {
				return err
			}
			b := feedback.NewBundle(version, store.All())
			data, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				return fmt.Errorf("%w: encode bundle: %w", ErrFeedback, err)
			}
			if out == "" {
				out = "whotoask-feedback.json"
			}
			if err := os.WriteFile(out, append(data, '\n'), 0o600); err != nil {
				return fmt.Errorf("%w: write %s: %w", ErrFeedback, out, err)
			}
			return opts.render(cmd.OutOrStdout(), b, func(w io.Writer, s style) {
				fmt.Fprintln(w, s.Bold("Wrote "+out))
				fmt.Fprintf(w, "  %d vote%s, %d comment%s. Queries appear only as word counts.\n",
					b.Votes.Total, plural(b.Votes.Total), len(b.Comments), plural(len(b.Comments)))
				fmt.Fprintln(w, s.Dim("  Read the whole file, then attach it to a GitHub issue or an email"))
				fmt.Fprintln(w, s.Dim("  yourself. It sends nothing on its own."))
			})
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "Where to write the bundle (default whotoask-feedback.json).")
	return cmd
}

// newFeedbackSummaryCmd builds the summary subcommand: the same arithmetic the
// bundle carries, shown in place for whoever runs the instance.
func newFeedbackSummaryCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show what the recorded feedback amounts to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := opts.loadFeedback()
			if err != nil {
				return err
			}
			b := feedback.NewBundle(version, store.All())
			return opts.render(cmd.OutOrStdout(), b, func(w io.Writer, s style) {
				if b.Votes.Total == 0 {
					fmt.Fprintln(w, s.Dim("No feedback recorded yet. Votes on answers land here."))
					return
				}
				fmt.Fprintln(w, s.Bold(fmt.Sprintf("%d votes  %d helpful, %d not",
					b.Votes.Total, b.Votes.Helpful, b.Votes.NotHelpful)))
				fmt.Fprintf(w, "  %d on people, %d on channels\n", b.Votes.OnPeople, b.Votes.OnChannels)
				for _, c := range b.Comments {
					fmt.Fprintf(w, "  %s %s\n", s.Dim("["+c.Vote+"]"), c.Comment)
				}
			})
		},
	}
	return cmd
}
