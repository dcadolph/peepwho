package cmd

import (
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// identityView is the audit of inferred identity merges, grouped by person.
type identityView struct {
	// Merges is how many inferred joins the index holds.
	Merges int `json:"merges"`
	// People are the people with at least one inferred join, in name order.
	People []identityPerson `json:"people"`
}

// identityPerson is one person and the aliases inferred to be them.
type identityPerson struct {
	// ID is the person's canonical identifier.
	ID string `json:"id"`
	// Name is the person's display name.
	Name string `json:"name,omitempty"`
	// Email is the person's work email.
	Email string `json:"email,omitempty"`
	// Joins are the aliases inferred to be this person, in alias order.
	Joins []identityJoin `json:"joins"`
}

// identityJoin is one inferred alias with its confidence and evidence.
type identityJoin struct {
	// Alias is the identifier that was folded in, such as "github:kim-doe".
	Alias string `json:"alias"`
	// Confidence is how sure the merge is, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Reason names the evidence, such as "unique name match".
	Reason string `json:"reason"`
}

// unlinkedView is the worklist of declared owners with nothing recorded
// against them: the people a source of record names but whose work peepwho
// cannot find, almost always because their handle was never tied to the address
// they commit under.
type unlinkedView struct {
	// Unlinked is how many declared owners have no recorded work.
	Unlinked int `json:"unlinked"`
	// Declared is how many declared owners there are in total.
	Declared int `json:"declared"`
	// Owners are those without work, the ones owning most first.
	Owners []unlinkedOwner `json:"owners"`
}

// unlinkedOwner is one declared owner with nothing recorded against them.
type unlinkedOwner struct {
	// ID is the identifier the source of record knows them by.
	ID string `json:"id"`
	// Name is their display name, when a source gave one.
	Name string `json:"name,omitempty"`
	// Owns are the areas they are the owner of record for.
	Owns []string `json:"owns"`
}

// namesATeam reports whether an owner id names a group rather than a person.
func namesATeam(id string) bool { return util.NamesATeam(id) }

// buildUnlinkedView collects the declared owners with no work recorded against
// them. Weight a source of record assigned is not work: without that
// distinction every owner looks busy the moment their ownership file is read.
func buildUnlinkedView(ix *index.Index) unlinkedView {
	var view unlinkedView
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
		view.Owners = append(view.Owners, unlinkedOwner{ID: string(id), Name: p.Name, Owns: owns})
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
					renderUnlinked(w, missing, s)
				})
			}
			view := buildIdentityView(ix)
			if len(args) == 1 {
				view = filterIdentityView(view, args[0])
			}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				renderIdentity(w, view, s)
			})
		},
	}
	cmd.Flags().BoolVar(&unlinked, "unlinked", false,
		"List declared owners with no work recorded against them, the worklist for an alias file.")
	return cmd
}

// buildIdentityView groups the index's inferred joins by the person they resolve
// into, each person's joins in alias order and the people in name order.
func buildIdentityView(ix *index.Index) identityView {
	byPerson := make(map[model.ID]*identityPerson)
	for _, j := range ix.Joins() {
		canon := ix.CanonicalID(j.Alias)
		p := byPerson[canon]
		if p == nil {
			p = &identityPerson{ID: string(canon)}
			if person := ix.Graph.People[canon]; person != nil {
				p.Name, p.Email = person.Name, person.Email
			}
			byPerson[canon] = p
		}
		p.Joins = append(p.Joins, identityJoin{
			Alias: string(j.Alias), Confidence: j.Confidence, Reason: j.Reason,
		})
	}
	view := identityView{}
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
func filterIdentityView(v identityView, query string) identityView {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return v
	}
	out := identityView{}
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
