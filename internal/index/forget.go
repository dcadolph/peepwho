package index

import (
	"strings"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// ForgetResult reports what purging a person removed.
type ForgetResult struct {
	// Records is how many stored source records were dropped.
	Records int
	// Mentions is how many references to the person were removed from other
	// records, such as channel member lists.
	Mentions int
}

// Forget removes a person from the index: every source record that is theirs,
// and every reference to them in other records, then rebuilds. The next save
// persists an index with no trace of them; a person removed here can only
// return by re-indexing a source that still contains them.
func (ix *Index) Forget(p *model.Person) ForgetResult {
	ids := forgetIDs(p)
	var res ForgetResult
	for name, recs := range ix.sources {
		kept := make([]connector.Record, 0, len(recs))
		for _, rec := range recs {
			if recordIsPerson(rec, ids) {
				res.Records++
				continue
			}
			if n := stripMentions(&rec, ids); n > 0 {
				res.Mentions += n
			}
			kept = append(kept, rec)
		}
		ix.sources[name] = kept
		ix.sourceCounts[name] = len(kept)
	}
	// The alias table would otherwise keep the one fact being erased: that
	// these identifiers were one person.
	gone := make(map[model.ID]bool, len(ids))
	for id := range ids {
		gone[model.ID(id)] = true
	}
	ix.identityResolver().Forget(gone)
	ix.rebuild()
	// rebuild resets the graph, the postings, and the reason text, but the
	// embedding vectors and the join ledger live outside all three and are
	// persisted by name. On an install built with --embed, a purged person's
	// address stayed in index.json as a vector key, and their join sat beside
	// it naming the alias, the canonical id, and the evidence. The command
	// promises no trace of them, so it has to mean the file too.
	for id := range ix.personVecs {
		if ids[strings.ToLower(string(id))] {
			delete(ix.personVecs, id)
		}
	}
	ix.joins = pruneJoins(ix.joins, ix)
	return res
}

// forgetIDs collects every identifier the person is known by, lowercased.
func forgetIDs(p *model.Person) map[string]bool {
	ids := make(map[string]bool)
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			ids[s] = true
		}
	}
	add(string(p.ID))
	add(p.Email)
	add(util.NormalizeEmail(p.Email))
	for _, id := range p.Identities {
		add(string(id))
	}
	return ids
}

// recordIsPerson reports whether a stored record belongs to the person.
func recordIsPerson(rec connector.Record, ids map[string]bool) bool {
	if rec.Kind != connector.KindPerson {
		return false
	}
	if ids[strings.ToLower(rec.PersonID)] || ids[util.NormalizeEmail(rec.Email)] {
		return true
	}
	for _, alt := range rec.AltIDs {
		if ids[strings.ToLower(alt)] {
			return true
		}
	}
	return false
}

// stripMentions removes references to the person from a record that is not
// theirs, and reports how many were removed.
func stripMentions(rec *connector.Record, ids map[string]bool) int {
	removed := 0
	if len(rec.Members) > 0 {
		kept := make([]string, 0, len(rec.Members))
		for _, m := range rec.Members {
			// Membership references fold the same way buildChannel keys them, so
			// a member listed with a plus tag is still recognized as the person
			// being purged rather than surviving as a leftover mention.
			if ids[normalizeRef(m)] {
				removed++
				continue
			}
			kept = append(kept, m)
		}
		rec.Members = kept
	}
	if ids[strings.ToLower(rec.Manager)] || ids[util.NormalizeEmail(rec.Manager)] {
		rec.Manager = ""
		removed++
	}
	// A tie between two subjects remembers who the only person to cross between
	// them was, and that identifier is not a membership list anyone thought to
	// sweep. It survived a purge, was re-derived into the graph, and the risk
	// view went on naming the forgotten person as the sole witness. Clearing it
	// keeps the tie and its witness count, which are facts about the subjects,
	// and drops the name, which is a fact about a person who was erased.
	for i := range rec.Links {
		if ids[normalizeRef(rec.Links[i].Sole)] {
			rec.Links[i].Sole = ""
			removed++
		}
	}
	return removed
}
