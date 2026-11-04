package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// SplitName is one display name the index holds under more than one person,
// which is what an identity the rules could not join looks like from outside.
type SplitName struct {
	// Name is the display name two or more records sign with.
	Name string `json:"name"`
	// IDs are the identifiers carrying it, alphabetically.
	IDs []string `json:"ids"`
}

// SplitNames finds the people the index kept apart who sign with the same full
// name.
//
// It is the one error that moves every finding the same way. A person split
// across two mailboxes reads as two people covering each other, so coverage
// looks better than it is and the subjects resting on one person disappear
// from the report entirely. An assessment that cannot join them has to say so
// rather than quietly hand over the flattering number.
func SplitNames(ix *index.Index) []SplitName {
	byName := make(map[string][]string)
	for id, p := range ix.Graph.People {
		name := strings.TrimSpace(p.Name)
		// A full name only. Two records both called Sam are not evidence of
		// anything, and listing them would bury the ones that are.
		if name == "" || !strings.Contains(name, " ") {
			continue
		}
		key := strings.ToLower(name)
		byName[key] = append(byName[key], string(id))
	}
	var out []SplitName
	for _, key := range util.SortedKeys(byName) {
		ids := byName[key]
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		name := key
		if p := ix.Graph.People[model.ID(ids[0])]; p != nil && p.Name != "" {
			name = p.Name
		}
		out = append(out, SplitName{Name: name, IDs: ids})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].IDs) != len(out[j].IDs) {
			return len(out[i].IDs) > len(out[j].IDs)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// AliasSuggestion is a group of records that look like one person, offered for
// a human to accept or reject.
//
// It is deliberately a suggestion and not a join. The identity rules already
// merge what they can defend, and what reaches here is what they refused:
// several records of one full name that the evidence could not settle. Merging
// those automatically is the one error that moves every finding the same way,
// since a person split in two reads as two people covering each other. Refusing
// to guess is right, and leaving the reader to discover the problem is not, so
// the assessment hands over the worklist with the reason for each.
type AliasSuggestion struct {
	// Canonical is the identifier the others would fold into: the one already
	// carrying the most work, so accepting the suggestion moves the fewest
	// records.
	Canonical string `json:"canonical"`
	// Aliases are the other identifiers, alphabetically.
	Aliases []string `json:"aliases"`
	// Name is the display name they share.
	Name string `json:"name"`
	// Why says what the records have in common and what the rules wanted but
	// did not find, so a reader can judge rather than trust.
	Why string `json:"why"`
	// Shared is how many real subjects the group holds between them, the
	// evidence a reader weighs the suggestion on.
	Shared int `json:"shared"`
}

// AliasFile renders suggestions in the format LoadAliases reads, so the
// worklist and the fix are the same document.
func AliasFile(suggestions []AliasSuggestion) map[string][]string {
	out := make(map[string][]string, len(suggestions))
	for _, s := range suggestions {
		out[s.Canonical] = s.Aliases
	}
	return out
}

// SuggestAliases proposes the joins the identity rules declined to make on
// their own, worst first.
//
// A group qualifies when several records share one full name. That is not
// enough to merge on, which is exactly why the rules left them alone, but it
// is enough to ask a human about: the person reviewing an assessment knows
// whether their client employs two people called the same thing, and the rules
// never can.
func SuggestAliases(ix *index.Index, split []SplitName) []AliasSuggestion {
	holders := topicHolders(ix)
	subjects := make(map[model.ID]map[string]bool)
	for topic, people := range holders {
		for id := range people {
			if subjects[id] == nil {
				subjects[id] = make(map[string]bool)
			}
			subjects[id][topic] = true
		}
	}

	out := make([]AliasSuggestion, 0, len(split))
	for _, s := range split {
		if len(s.IDs) < 2 {
			continue
		}
		// The record already carrying the most work leads, so accepting the
		// suggestion rewrites the fewest identifiers and the canonical id is
		// the one a reader is most likely to recognize.
		ids := append([]string(nil), s.IDs...)
		sort.Slice(ids, func(i, j int) bool {
			ni, nj := len(subjects[model.ID(ids[i])]), len(subjects[model.ID(ids[j])])
			if ni != nj {
				return ni > nj
			}
			return ids[i] < ids[j]
		})
		union := make(map[string]bool)
		for _, id := range ids {
			for topic := range subjects[model.ID(id)] {
				union[topic] = true
			}
		}
		aliases := append([]string(nil), ids[1:]...)
		sort.Strings(aliases)
		out = append(out, AliasSuggestion{
			Canonical: ids[0],
			Aliases:   aliases,
			Name:      s.Name,
			Shared:    len(union),
			Why:       aliasWhy(ids),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Shared != out[j].Shared {
			return out[i].Shared > out[j].Shared
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// aliasWhy states what the records have in common and what stopped the rules
// from acting on it, so a reader judges the suggestion instead of trusting it.
func aliasWhy(ids []string) string {
	var mailboxes, handles, noreply int
	for _, id := range ids {
		switch {
		case util.IsRoleEmail(id):
			mailboxes++
		case strings.Contains(id, "@"):
			if _, ok := util.GitHubNoreplyLogin(id); ok {
				noreply++
			} else {
				mailboxes++
			}
		default:
			handles++
		}
	}
	switch {
	case noreply > 0 && mailboxes > 0:
		return fmt.Sprintf(
			"%d records sign with this name, mixing a provider's private commit "+
				"address with a real mailbox. That is the commonest shape of one "+
				"person, and the addresses share nothing the rules can verify",
			len(ids))
	case handles > 0:
		return fmt.Sprintf(
			"%d records sign with this name across a handle and an address, which "+
				"nothing links but the name itself", len(ids))
	default:
		return fmt.Sprintf(
			"%d records sign with this name at different mailboxes. The rules "+
				"decline a bare name because two people genuinely can share one; "+
				"whether this client employs two is a question only you can answer",
			len(ids))
	}
}
