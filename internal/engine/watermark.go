package engine

import (
	"errors"
	"time"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/state"
	"github.com/dcadolph/whotoask/internal/util"
	"github.com/dcadolph/whotoask/internal/vault"
)

// loadState reads the watermarks, calling Unlock when they are sealed under a key
// the store does not have, since a run reads them before it loads the index.
func (x *Indexer) loadState() (*state.State, error) {
	st, err := x.store()
	if err != nil {
		return nil, err
	}
	wm, err := st.LoadState()
	if !errors.Is(err, vault.ErrEncrypted) || x.Unlock == nil {
		return wm, err
	}
	c, err := x.Unlock(IndexPassphrase, func(c vault.Codec) error {
		var lerr error
		wm, lerr = st.WithCodec(c).LoadState()
		return lerr
	})
	if err != nil {
		return nil, err
	}
	x.unlocked = c
	return wm, nil
}

// incrementalCapable reports whether a source supports incremental
// re-indexing, fetching only what changed since a watermark. Other sources
// read in full.
func incrementalCapable(source string) bool {
	spec, ok := SourceByName(source)
	return ok && spec.Incremental
}

// watermark decides whether this run is incremental: a merge into an existing
// index of a source that supports it, with a saved watermark and no --full.
// When so it returns the watermark, so the run fetches only what changed since
// and folds it in rather than re-reading the whole source.
func (x *Indexer) watermark(req *IndexRequest, canInc bool, scope string) (time.Time, bool, error) {
	if !req.Merge || req.Full || !canInc || !x.indexExists() {
		return time.Time{}, false, nil
	}
	st, err := x.loadState()
	if err != nil {
		return time.Time{}, false, err
	}
	if wm, ok := st.Get(req.Source, scope); ok {
		return wm.Cursor, true, nil
	}
	return time.Time{}, false, nil
}

// updateWatermark advances a source's incremental watermark to the newest
// activity just indexed, after a successful run. It never moves the cursor
// backward, and does nothing for a source without incremental support or a
// read that saw nothing dated.
func (x *Indexer) updateWatermark(source, scope string, full bool, recs []connector.Record) error {
	if !incrementalCapable(source) {
		return nil
	}
	var cursor time.Time
	for _, r := range recs {
		if r.Time.After(cursor) {
			cursor = r.Time
		}
	}
	if cursor.IsZero() {
		return nil
	}
	// Serialize the read-modify-write on the state file, so two concurrent
	// index runs against different sources cannot drop each other's cursor.
	unlock, err := util.LockFile(x.Dir.State() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := x.loadState()
	if err != nil {
		return err
	}
	store, err := x.store()
	if err != nil {
		return err
	}
	// A partial read only ever adds newer activity, so never let its cursor fall
	// behind the stored one; a full read is authoritative and replaces it.
	if prev, ok := st.Get(source, scope); ok && !full && prev.Cursor.After(cursor) {
		cursor = prev.Cursor
	}
	st.Set(state.Watermark{Source: source, Scope: scope, Cursor: cursor, Complete: true, RanAt: x.now()})
	return store.SaveState(st)
}

// gitMarks returns where a previous run stopped in each repository, so reading
// resumes there. It is empty unless this is a merge, since a full read is meant
// to start over, and empty on the first run, when there is nothing to resume
// from.
func (x *Indexer) gitMarks(merge bool, paths []string) map[string]string {
	if !merge || !x.indexExists() {
		return nil
	}
	st, err := x.loadState()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if wm, ok := st.Get("git", "repo:"+p); ok && wm.Mark != "" {
			out[p] = wm.Mark
		}
	}
	return out
}

// saveGitMarks records where reading stopped in each repository. Each is its
// own watermark, since one run may cover several repositories and they advance
// independently.
func (x *Indexer) saveGitMarks(marks map[string]string) error {
	if len(marks) == 0 {
		return nil
	}
	unlock, err := util.LockFile(x.Dir.State() + util.LockSuffix)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := x.loadState()
	if err != nil {
		return err
	}
	store, err := x.store()
	if err != nil {
		return err
	}
	for path, mark := range marks {
		st.Set(state.Watermark{
			Source: "git", Scope: "repo:" + path, Mark: mark,
			Complete: true, RanAt: x.now(),
		})
	}
	return store.SaveState(st)
}
