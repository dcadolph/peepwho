// Package state persists per-source incremental watermarks so a re-index only
// fetches what changed since the last successful run. It lives in a sidecar
// beside the index, sealed with the same codec, since a scope names the
// repositories and projects a person reads.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vault"
)

// Version is the current state-file schema version.
const Version = 1

// State is the set of incremental watermarks, keyed by source and scope.
type State struct {
	// Version is the schema version of the file.
	Version int `json:"version"`
	// Watermarks maps a source-and-scope key to its watermark.
	Watermarks map[string]Watermark `json:"watermarks"`
}

// Watermark records how far a source-and-scope was indexed, so the next run can
// ask only for newer items.
type Watermark struct {
	// Source is the connector name, such as "jira".
	Source string `json:"source"`
	// Scope identifies what within the source was indexed, such as the sorted
	// project keys, so changing the scope starts a fresh watermark.
	Scope string `json:"scope"`
	// Cursor is the next "since" bound: the newest item time on a complete read,
	// or the oldest item time seen on a capped read, so a truncated run never
	// skips the items it did not reach.
	Cursor time.Time `json:"cursor"`
	// Complete reports whether the last read covered everything after the prior
	// cursor. A capped or interrupted read sets it false.
	Complete bool `json:"complete"`
	// RanAt is when the watermark was last written.
	RanAt time.Time `json:"ran_at"`
	// Mark is a position only the source understands, used where a time is not
	// a safe cursor. Git is the case that needs it: a pull request branched
	// three weeks ago and merged today carries three-week-old dates, so reading
	// "everything since the newest timestamp" would step straight over it. The
	// newest commit read is exact, and a history that was rewritten simply
	// fails to contain it, which is the signal to read the whole window again.
	Mark string `json:"mark,omitempty"`
}

// Key composes the map key for a source and scope. The separator is a byte that
// cannot appear in a source name or scope string, so distinct pairs never
// collide.
func Key(source, scope string) string { return source + "\x00" + scope }

// New returns an empty state.
func New() *State {
	return &State{Version: Version, Watermarks: map[string]Watermark{}}
}

// Get returns the watermark for a source and scope, and whether one exists.
func (s *State) Get(source, scope string) (Watermark, bool) {
	wm, ok := s.Watermarks[Key(source, scope)]
	return wm, ok
}

// Set stores a watermark under its own source and scope.
func (s *State) Set(wm Watermark) {
	if s.Watermarks == nil {
		s.Watermarks = map[string]Watermark{}
	}
	s.Watermarks[Key(wm.Source, wm.Scope)] = wm
}

// Delete removes the watermark for a source and scope, so the next run of that
// scope is a full index.
func (s *State) Delete(source, scope string) {
	delete(s.Watermarks, Key(source, scope))
}

// Load reads state from path under codec, returning an empty state when the file
// is absent so a first run is a full index rather than an error. With a nil codec
// a sealed file returns an error matching vault.ErrEncrypted.
func Load(path string, codec vault.Codec) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return nil, fmt.Errorf("state: open: %w", err)
	}
	if codec == nil {
		codec = vault.Plain{}
	}
	if raw, err = codec.Decode(raw); err != nil {
		return nil, fmt.Errorf("state: decode: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("state: decode: %w", err)
	}
	if s.Watermarks == nil {
		s.Watermarks = map[string]Watermark{}
	}
	if s.Version == 0 {
		s.Version = Version
	}
	return &s, nil
}

// Save writes state to path readable only by the owner, sealed under codec when
// it is set, through a temporary file and rename so a crash cannot truncate it.
func (s *State) Save(path string, codec vault.Codec) error {
	if s.Version == 0 {
		s.Version = Version
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	if codec != nil {
		if raw, err = codec.Encode(raw); err != nil {
			return fmt.Errorf("state: seal: %w", err)
		}
	}
	if err := util.WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("state: write: %w", err)
	}
	return nil
}
