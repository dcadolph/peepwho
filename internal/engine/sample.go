package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dcadolph/peepwho/internal/util"
)

// sampleMarker is the JSON the sample marker holds.
type sampleMarker struct {
	// LoadedAt is when the sample data was loaded.
	LoadedAt time.Time `json:"loaded_at"`
}

// LoadSample reads when sample data was loaded into the directory. The zero time
// means the directory holds no sample data.
func (s *Store) LoadSample() (time.Time, error) {
	path := s.dir.Sample()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("sample: read %s: %w", path, err)
	}
	var m sampleMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		return time.Time{}, fmt.Errorf("sample: parse %s: %w", path, err)
	}
	return m.LoadedAt, nil
}

// SaveSample marks the directory as holding sample data loaded at now. The marker
// holds only that time, so it is never sealed.
func (s *Store) SaveSample(now time.Time) error {
	raw, err := json.Marshal(sampleMarker{LoadedAt: now.UTC()})
	if err != nil {
		return fmt.Errorf("sample: encode: %w", err)
	}
	if err := os.MkdirAll(string(s.dir), 0o700); err != nil {
		return fmt.Errorf("sample: %w", err)
	}
	if err := util.WriteFileAtomic(s.dir.Sample(), raw, 0o600); err != nil {
		return fmt.Errorf("sample: write %s: %w", s.dir.Sample(), err)
	}
	return nil
}
