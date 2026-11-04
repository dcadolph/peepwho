package cmd

import (
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/render"
	"github.com/dcadolph/peepwho/internal/util"
)

// namesATeam reports whether an owner id names a group rather than a person.
func namesATeam(id string) bool { return util.NamesATeam(id) }

// buildUnlinkedView collects the declared owners with no work recorded against
// them. Weight a source of record assigned is not work: without that
// distinction every owner looks busy the moment their ownership file is read.
func buildUnlinkedView(ix *index.Index) render.UnlinkedView {
	var view render.UnlinkedView
	for id, p := range ix.Graph.People {
		if len(p.Owns) == 0 || namesATeam(string(id)) {
			continue
		}
		view.Declared++
		worked := false
		for tid, w := range p.Topics {
			if w-p.Stated[tid] > 0 {
				worked = true
				break
			}
		}
		if worked {
			continue
		}
		owns := make([]string, 0, len(p.Owns))
		for _, t := range p.Owns {
			owns = append(owns, string(t))
		}
		sort.Strings(owns)
		view.Owners = append(view.Owners, render.UnlinkedOwner{ID: string(id), Name: p.Name, Owns: owns})
	}
	view.Unlinked = len(view.Owners)
	sort.Slice(view.Owners, func(i, j int) bool {
		if len(view.Owners[i].Owns) != len(view.Owners[j].Owns) {
			return len(view.Owners[i].Owns) > len(view.Owners[j].Owns)
		}
		return view.Owners[i].ID < view.Owners[j].ID
	})
	return view
}

// newIdentityCmd builds the identity command, an audit of the inferred merges
// that fold a handle such as github:kim-doe into a person. Joins by shared email
// or provider id are identity, not inference, and are not listed.
func newIdentityCmd(opts *options) *cobra.Command {
	var unlinked bool
	cmd := &cobra.Command{
		Use:   "identity [person]",
		Short: "Show how identities were merged across sources",
		Long: `List the inferred identity merges: each handle folded into a person, how
confident the merge is, and the evidence for it. Joins by shared email or
provider id are certain and are not listed. Correct a wrong merge by editing the
alias file and re-indexing.

Ownership is stated by handle and work is recorded by address, so an owner
whose two never met looks exactly like one who does nothing. --unlinked lists
those owners, worst first, which is the worklist for an alias file.

Examples:
  peepwho identity
  peepwho identity kim
  peepwho identity --unlinked
  peepwho identity --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			if unlinked {
				missing := buildUnlinkedView(ix)
				return opts.render(cmd.OutOrStdout(), missing, func(w io.Writer, s style) {
					render.RenderUnlinked(w, missing, s)
				})
			}
			view := buildIdentityView(ix)
			if len(args) == 1 {
				view = filterIdentityView(view, args[0])
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				render.RenderIdentity(w, view, s)
			})
		},
	}
	cmd.Flags().BoolVar(&unlinked, "unlinked", false,
		"List declared owners with no work recorded against them, the worklist for an alias file.")
	return cmd
}

// buildIdentityView groups the index's inferred joins by the person they resolve
// into, each person's joins in alias order and the people in name order.
func buildIdentityView(ix *index.Index) render.IdentityView {
	byPerson := make(map[model.ID]*render.IdentityPerson)
	for _, j := range ix.Joins() {
		canon := ix.CanonicalID(j.Alias)
		p := byPerson[canon]
		if p == nil {
			p = &render.IdentityPerson{ID: string(canon)}
			if person := ix.Graph.People[canon]; person != nil {
				p.Name, p.Email = person.Name, person.Email
			}
			byPerson[canon] = p
		}
		p.Joins = append(p.Joins, render.IdentityJoin{
			Alias: string(j.Alias), Confidence: j.Confidence, Reason: j.Reason,
		})
	}
	view := render.IdentityView{}
	for _, p := range byPerson {
		sort.Slice(p.Joins, func(i, j int) bool { return p.Joins[i].Alias < p.Joins[j].Alias })
		view.Merges += len(p.Joins)
		view.People = append(view.People, *p)
	}
	sort.Slice(view.People, func(i, j int) bool {
		if view.People[i].Name != view.People[j].Name {
			return view.People[i].Name < view.People[j].Name
		}
		return view.People[i].ID < view.People[j].ID
	})
	return view
}

// filterIdentityView narrows the view to people whose id, email, or name
// contains the query, so `peepwho identity kim` audits one person's merges.
func filterIdentityView(v render.IdentityView, query string) render.IdentityView {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return v
	}
	out := render.IdentityView{}
	for _, p := range v.People {
		if strings.Contains(strings.ToLower(p.ID), q) ||
			strings.Contains(strings.ToLower(p.Email), q) ||
			strings.Contains(strings.ToLower(p.Name), q) {
			out.People = append(out.People, p)
			out.Merges += len(p.Joins)
		}
	}
	return out
}
