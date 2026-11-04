package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/spf13/pflag"

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

// RefreshResult reports one replayed scope of a refresh.
type RefreshResult struct {
	// Source is the registry name of the replayed source.
	Source string
	// Scope is the refresh key the flags were recorded under.
	Scope string
	// RanAt is when the refresh that ran this scope started.
	RanAt time.Time
	// OK is true when the scope indexed without error.
	OK bool
	// Err is the scope's failure, nil when OK.
	Err error
}

// Refresh replays every recorded scope of the named sources, all of them when
// sources is nil, merging each into the index in source then scope order. Each
// scope loads the index fresh and takes the index lock on its own, so one
// failing scope leaves the others' results in place. A replay is never
// recorded again: the record is what it replays. now stamps each result.
func (x *Indexer) Refresh(ctx context.Context, sources []string, now time.Time) ([]RefreshResult, error) {
	inv, err := NewStore(x.Dir, nil).LoadInvocations()
	if err != nil {
		return nil, err
	}
	if len(inv) == 0 {
		return nil, fmt.Errorf("%w: nothing to refresh; run `peepwho index --source ...` at least once first", ErrBadArgs)
	}
	names := make([]string, 0, len(inv))
	for s := range inv {
		if sources == nil || slices.Contains(sources, s) {
			names = append(names, s)
		}
	}
	sort.Strings(names)
	var results []RefreshResult
	failed := 0
	for _, src := range names {
		scopes := make([]string, 0, len(inv[src]))
		for scope := range inv[src] {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		for _, scope := range scopes {
			what := src
			if scope != "" {
				what += " (" + scope + ")"
			}
			fmt.Fprintf(x.log(), "refreshing %s...\n", what)
			err := x.replay(ctx, src, inv[src][scope])
			if err != nil {
				failed++
				fmt.Fprintf(x.log(), "refresh %s failed: %v\n", what, err)
			}
			results = append(results, RefreshResult{Source: src, Scope: scope, RanAt: now, OK: err == nil, Err: err})
		}
	}
	if failed > 0 {
		return results, fmt.Errorf("refresh: %d of %d run(s) failed", failed, len(results))
	}
	return results, nil
}

// replay parses one recorded flag list and indexes the source with it as a
// merge. The recorded --policy, or its absence, picks the policy through
// PolicyFor, as it did when each scope ran as its own process. --json and
// --human are accepted and change nothing, since an index run prints no
// result.
func (x *Indexer) replay(ctx context.Context, source string, args []string) error {
	fs := pflag.NewFlagSet("index", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	req := IndexRequest{Source: source}
	RegisterIndexFlags(fs, &req.IndexArgs)
	recordedPolicy := fs.String("policy", "", "")
	fs.Bool("json", false, "")
	fs.Bool("human", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: recorded flags: %w", ErrBadArgs, err)
	}
	req.Merge = true
	run := *x
	run.Record = nil
	if x.PolicyFor != nil {
		pol, err := x.PolicyFor(*recordedPolicy)
		if err != nil {
			return err
		}
		run.Policy = pol
	}
	err := run.Index(ctx, req)
	x.unlocked = run.unlocked
	return err
}
