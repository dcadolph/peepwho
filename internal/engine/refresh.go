package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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
		scopes := slices.Sorted(maps.Keys(inv[src]))
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

// IndexRun is one index run parsed from a flag list, the form the refresh
// config records an index run in.
type IndexRun struct {
	// Request is the source and every index flag value the list sets.
	Request IndexRequest
	// Policy is the --policy value the list carries, empty when it has none.
	Policy string
	// Replay is the list as a refresh replays it: each flag the list set, in
	// flag name order, a repeatable flag as one name and value pair per value
	// and any other as name=value, with the flags that steer only this run
	// (--merge, --full, --allow-shrink, --changes-file) and the output flags
	// left out. It is the shape the command line records.
	Replay []string
}

// replaySkipFlags are flags a recorded run leaves out: they steer one run or
// its output, not what a source reads.
//
//nolint:gochecknoglobals // Read-only lookup table.
var replaySkipFlags = map[string]bool{
	"merge": true, "full": true, "allow-shrink": true, "changes-file": true, "json": true, "human": true,
}

// IndexFromFlags parses an index flag list for source with the flags the
// index command takes, plus --policy, --json, and --human, which a recorded
// command line run may carry. --json and --human change nothing, since an
// index run prints no result. A refresh replays recorded lists through the
// same parse, so an app indexing through it and a refresh cannot read a flag
// differently. A bare argument that is not a flag is refused.
func IndexFromFlags(source string, args []string) (IndexRun, error) {
	run, rest, err := parseIndexFlags(source, args, "flags")
	if err != nil {
		return IndexRun{}, err
	}
	if len(rest) > 0 {
		return IndexRun{}, fmt.Errorf("%w: flags: unexpected argument %q", ErrBadArgs, rest[0])
	}
	return run, nil
}

// parseIndexFlags parses args for source and returns the run and any bare
// arguments left over. label names the list in a parse error.
func parseIndexFlags(source string, args []string, label string) (IndexRun, []string, error) {
	run := IndexRun{Request: IndexRequest{Source: source}}
	fs := indexFlagSet(&run)
	if err := fs.Parse(args); err != nil {
		return IndexRun{}, nil, fmt.Errorf("%w: %s: %w", ErrBadArgs, label, err)
	}
	run.Replay = ReplayFlags(fs)
	return run, fs.Args(), nil
}

// indexFlagSet returns a flag set holding every flag a recorded index run may
// carry, bound to run.
func indexFlagSet(run *IndexRun) *pflag.FlagSet {
	fs := pflag.NewFlagSet("index", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	RegisterIndexFlags(fs, &run.Request.IndexArgs)
	fs.StringVar(&run.Policy, "policy", "", "")
	fs.Bool("json", false, "")
	fs.Bool("human", false, "")
	return fs
}

// ReplayFlags returns the flags set on fs that a recorded run replays, in flag
// name order: a repeatable flag as one name and value pair per value, any other
// as name=value. Only flags an index run takes are kept, less the ones that
// steer one run or its output, so a command line's own flags such as
// --data-dir never reach the record. The command line and the core record
// through it, so both write the same list for the same run.
func ReplayFlags(fs *pflag.FlagSet) []string {
	known := indexFlagSet(&IndexRun{})
	var out []string
	fs.Visit(func(f *pflag.Flag) {
		if known.Lookup(f.Name) == nil || replaySkipFlags[f.Name] {
			return
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			for _, v := range sv.GetSlice() {
				out = append(out, "--"+f.Name, v)
			}
			return
		}
		out = append(out, "--"+f.Name+"="+f.Value.String())
	})
	return out
}

// replay parses one recorded flag list and indexes the source with it as a
// merge. The recorded --policy, or its absence, picks the policy through
// PolicyFor, as it did when each scope ran as its own process.
func (x *Indexer) replay(ctx context.Context, source string, args []string) error {
	parsed, _, err := parseIndexFlags(source, args, "recorded flags")
	if err != nil {
		return err
	}
	req := parsed.Request
	req.Merge = true
	run := *x
	run.Record = nil
	if x.PolicyFor != nil {
		pol, err := x.PolicyFor(parsed.Policy)
		if err != nil {
			return err
		}
		run.Policy = pol
	}
	err = run.Index(ctx, req)
	x.unlocked = run.unlocked
	return err
}

// RecordInvocation saves flags as what source was indexed with under scope,
// replacing any earlier flags for the same scope. The read-modify-write runs
// under a cross-process lock so two concurrent index runs keep each other's
// entries.
func (s *Store) RecordInvocation(source, scope string, flags []string) error {
	unlock, err := util.LockFile(s.dir.Refresh() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	inv, err := s.LoadInvocations()
	if err != nil {
		return err
	}
	if inv[source] == nil {
		inv[source] = map[string][]string{}
	}
	inv[source][scope] = flags
	return s.SaveInvocations(inv)
}
