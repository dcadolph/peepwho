// Package identity resolves the many identifiers a person accumulates across
// sources (email, GitHub login, Jira account id) to one canonical identifier,
// so one human stays one node in the graph.
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/dcadolph/whotoask/internal/model"
)

// Resolver unions identifiers that belong to the same person and resolves any
// of them to a single canonical identifier. The canonical pick prefers an
// email over a bare name slug, and either over a source-prefixed identifier
// such as "github:alice"; ties break lexically so resolution is deterministic.
//
// Canonical path-compresses on read, so every access mutates the shared maps.
// The serving process resolves identities from multiple request goroutines at
// once, so all access is guarded by mu.
type Resolver struct {
	// mu guards parent and rep against concurrent access, including the writes
	// that Canonical performs while path-compressing.
	mu sync.Mutex
	// parent implements union-find; each identifier points toward its root.
	parent map[model.ID]model.ID
	// rep maps a root to the canonical representative of its set.
	rep map[model.ID]model.ID
}

// NewResolver returns an empty resolver where every identifier is its own
// canonical form.
func NewResolver() *Resolver {
	return &Resolver{
		parent: make(map[model.ID]model.ID),
		rep:    make(map[model.ID]model.ID),
	}
}

// Union records that a and b identify the same person.
func (r *Resolver) Union(a, b model.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.union(a, b)
}

// union records that a and b identify the same person. The caller holds mu.
func (r *Resolver) union(a, b model.ID) {
	a, b = normalize(a), normalize(b)
	if a == "" || b == "" || a == b {
		return
	}
	ra, rb := r.find(a), r.find(b)
	if ra == rb {
		return
	}
	best := better(r.rep[ra], r.rep[rb])
	r.parent[rb] = ra
	r.rep[ra] = best
	delete(r.rep, rb)
}

// Canonical returns the canonical identifier for id, or id itself when it has
// never been unioned with anything.
func (r *Resolver) Canonical(id model.ID) model.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canonical(id)
}

// canonical returns the canonical identifier for id. The caller holds mu.
func (r *Resolver) canonical(id model.ID) model.ID {
	id = normalize(id)
	if _, ok := r.parent[id]; !ok {
		return id
	}
	return r.rep[r.find(id)]
}

// Pairs returns every known identifier mapped to its canonical form, omitting
// identifiers that are already canonical. The result is nil when the resolver
// holds nothing worth persisting.
func (r *Resolver) Pairs() map[model.ID]model.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out map[model.ID]model.ID
	for id := range r.parent {
		c := r.canonical(id)
		if c == id {
			continue
		}
		if out == nil {
			out = make(map[model.ID]model.ID)
		}
		out[id] = c
	}
	return out
}

// Restore replays persisted alias pairs, mapping each identifier back to its
// canonical form.
func (r *Resolver) Restore(pairs map[model.ID]model.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for alias, canonical := range pairs {
		r.union(canonical, alias)
	}
}

// LoadFile merges an alias file into the resolver. The file is a JSON object
// mapping a canonical identifier to the list of identifiers that belong to
// the same person, for example {"alice@example.com": ["github:alice"]}.
func (r *Resolver) LoadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAliases, err)
	}
	var groups map[string][]string
	if err := json.Unmarshal(raw, &groups); err != nil {
		return fmt.Errorf("%w: parse %s: %w", ErrAliases, path, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for canonical, aliases := range groups {
		for _, alias := range aliases {
			r.union(model.ID(canonical), model.ID(alias))
		}
	}
	return nil
}

// find returns the root of id, creating a singleton set on first sight and
// compressing the path as it walks. The caller holds mu.
func (r *Resolver) find(id model.ID) model.ID {
	if _, ok := r.parent[id]; !ok {
		r.parent[id] = id
		r.rep[id] = id
		return id
	}
	root := id
	for r.parent[root] != root {
		root = r.parent[root]
	}
	for r.parent[id] != root {
		r.parent[id], id = root, r.parent[id]
	}
	return root
}

// better picks the preferred canonical form of two identifiers.
func better(a, b model.ID) model.ID {
	ra, rb := rank(a), rank(b)
	if ra != rb {
		if ra < rb {
			return a
		}
		return b
	}
	return min(a, b)
}

// rank orders identifier forms by how good a canonical they make: emails
// first, bare name slugs next, source-prefixed identifiers last.
func rank(id model.ID) int {
	switch {
	case strings.Contains(string(id), "@"):
		return 0
	case strings.Contains(string(id), ":"):
		return 2
	default:
		return 1
	}
}

// normalize lowercases and trims an identifier so unions match the graph's
// identifier form.
func normalize(id model.ID) model.ID {
	return model.ID(strings.ToLower(strings.TrimSpace(string(id))))
}

// Forget removes a set of identifiers from the resolver entirely, rebuilding
// the union-find over what remains. A purged person's identifiers must not
// survive as aliases in a saved index, which is where a purge would otherwise
// quietly keep the one fact being erased: that these identities were one
// person.
func (r *Resolver) Forget(gone map[model.ID]bool) {
	if len(gone) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Collect the surviving sets before touching anything, since find mutates.
	groups := make(map[model.ID][]model.ID)
	for id := range r.parent {
		if gone[id] {
			continue
		}
		root := r.find(id)
		key := r.rep[root]
		if gone[key] {
			key = root
		}
		groups[key] = append(groups[key], id)
	}
	r.parent = make(map[model.ID]model.ID)
	r.rep = make(map[model.ID]model.ID)
	for _, ids := range groups {
		for i := 1; i < len(ids); i++ {
			r.union(ids[0], ids[i])
		}
	}
}
