package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/fact"
)

// newFactCmd builds the fact command group: record a typed statement a crawl
// cannot find, review what has been recorded, forget a mistake, and import a
// batch from another system.
func newFactCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fact",
		Short: "Record, review, forget, or import typed facts a crawl cannot find",
		Long: fmt.Sprintf(`Facts state what no crawl can: which team owns a service, who to escalate to,
and above all what a team does NOT own. Each is a subject, a relation, and an
object, labeled with the source that asserted it, and lives in facts.json next
to the index so it survives re-indexing.

Relations: %s

  peepwho fact record team:payments not_owned_by service:checkout --detail "moved to Checkout team"
  peepwho fact list team:payments
  peepwho fact forget --source catalog
  cat facts.json | peepwho fact import --source catalog -`, strings.Join(fact.Relations(), ", ")),
	}
	cmd.AddCommand(newFactRecordCmd(opts), newFactListCmd(opts), newFactForgetCmd(opts), newFactImportCmd(opts))
	return cmd
}

// newFactRecordCmd builds the record subcommand.
func newFactRecordCmd(opts *options) *cobra.Command {
	var detail, source string
	cmd := &cobra.Command{
		Use:   "record SUBJECT RELATION OBJECT",
		Short: "Record one typed fact",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := opts.loadFacts()
			if err != nil {
				return err
			}
			f := fact.Fact{Subject: args[0], Relation: args[1], Object: args[2], Detail: detail, Source: source}
			if err := store.Add(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "recorded: %s %s %s\n", f.Subject, f.Relation, f.Object)
			return nil
		},
	}
	cmd.Flags().StringVar(&detail, "detail", "", "A human note explaining the fact.")
	cmd.Flags().StringVar(&source, "source", "curated", "Who asserted the fact.")
	return cmd
}

// newFactListCmd builds the list subcommand. Output follows the global
// contract: human lines at a terminal or under --human, JSON when piped or
// under --json or --pretty.
func newFactListCmd(opts *options) *cobra.Command {
	var relation, source string
	cmd := &cobra.Command{
		Use:   "list [SUBJECT]",
		Short: "List recorded facts, newest evidence labeled with source and age",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := opts.loadFacts()
			if err != nil {
				return err
			}
			f := fact.Filter{Relation: relation, Source: source}
			if len(args) == 1 {
				f.Subject = args[0]
			}
			facts := store.List(f)
			return opts.render(cmd.OutOrStdout(), facts, func(w io.Writer, _ style) {
				writeFactLines(w, facts)
			})
		},
	}
	cmd.Flags().StringVar(&relation, "relation", "", "Only facts with this relation.")
	cmd.Flags().StringVar(&source, "source", "", "Only facts from this source.")
	return cmd
}

// newFactForgetCmd builds the forget subcommand.
func newFactForgetCmd(opts *options) *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "forget [SUBJECT [RELATION [OBJECT]]]",
		Short: "Forget facts by subject, relation, object, or whole source",
		Args:  cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := fact.Filter{Source: source}
			if len(args) > 0 {
				f.Subject = args[0]
			}
			if len(args) > 1 {
				f.Relation = args[1]
			}
			if len(args) > 2 {
				f.Object = args[2]
			}
			if len(args) == 0 && source == "" {
				return fmt.Errorf("%w: name a subject or pass --source, so a bare forget cannot wipe everything", ErrBadArgs)
			}
			store, err := opts.loadFacts()
			if err != nil {
				return err
			}
			n, err := store.Forget(f)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "forgot %d fact(s)\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "Forget every fact from this source.")
	return cmd
}

// newFactImportCmd builds the import subcommand.
func newFactImportCmd(opts *options) *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "import [FILE]",
		Short: "Import a JSON array of facts from a file or stdin",
		Long: `Read a JSON array of facts from a file, or from stdin when the file is - or
omitted. With --source, every existing fact from that source is replaced, so an
import is the whole of what that source currently asserts.

  cat catalog.json | peepwho fact import --source catalog -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := ""
			if len(args) == 1 {
				file = args[0]
			}
			rc, closeJSON, err := jsonInput(cmd, file)
			if err != nil {
				return err
			}
			defer closeJSON()
			store, err := opts.loadFacts()
			if err != nil {
				return err
			}
			n, err := store.Import(rc, source)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "imported %d fact(s)\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "Label imported facts with this source and replace that source's existing facts.")
	return cmd
}

// writeFactLines prints facts one per line with the source and recorded date.
// Fields are escaped: facts arrive through imports as well as by hand.
func writeFactLines(w io.Writer, facts []fact.Fact) {
	for _, f := range facts {
		line := fmt.Sprintf("%s %s %s",
			escapeControls(f.Subject), escapeControls(f.Relation), escapeControls(f.Object))
		meta := escapeControls(f.Source)
		if !f.Time.IsZero() {
			if meta != "" {
				meta += " "
			}
			meta += f.Time.Format(time.DateOnly)
		}
		if meta != "" {
			line += fmt.Sprintf("  [%s]", meta)
		}
		if f.Detail != "" {
			line += "  " + escapeControls(f.Detail)
		}
		fmt.Fprintln(w, line)
	}
}
