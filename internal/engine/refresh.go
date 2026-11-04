package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dcadolph/peepwho/internal/util"
)

// Invocations records, per source and per scope within it, the flags an index
// run used, so refresh replays every distinct scope rather than only the last
// one indexed.
type Invocations map[string]map[string][]string

// RefreshScope keys one indexing run for the refresh config. Two runs with the
// same key describe the same slice of a source and the newer flags replace the
// older; two runs with different keys, such as two repositories indexed in
// separate git runs, are both kept and both replayed.
func RefreshScope(source, watermarkScope, file string, repoPaths []string) string {
	if source == "git" {
		return "repo:" + sortedJoin(repoPaths)
	}
	if spec, ok := SourceByName(source); ok && spec.FileKeyed {
		return "file:" + file
	}
	return watermarkScope
}

// LoadInvocations reads the refresh config. A missing file yields an empty map.
// A file in the older one-invocation-per-source shape is folded into the keyed
// shape under an empty scope, so nothing already recorded is dropped.
func (s *Store) LoadInvocations() (Invocations, error) {
	path := s.dir.Refresh()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Invocations{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refresh: read %s: %w", path, err)
	}
	inv := Invocations{}
	if err := json.Unmarshal(raw, &inv); err == nil {
		return inv, nil
	}
	old := map[string][]string{}
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("refresh: parse %s: %w", path, err)
	}
	// The failed decode above can leave partial entries behind, so the keyed
	// map is rebuilt from nothing.
	inv = Invocations{}
	for source, args := range old {
		inv[source] = map[string][]string{"": args}
	}
	return inv, nil
}

// SaveInvocations writes the refresh config atomically.
func (s *Store) SaveInvocations(inv Invocations) error {
	path := s.dir.Refresh()
	raw, err := json.Marshal(inv)
	if err != nil {
		return fmt.Errorf("refresh: encode: %w", err)
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("refresh: write %s: %w", path, err)
	}
	return nil
}
