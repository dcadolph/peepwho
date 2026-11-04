package cmd

import (
	"sync"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
)

// TestUpdateWatermarkConcurrent verifies that concurrent index runs against
// different sources do not drop each other's cursor. The state file is a shared
// read-modify-write, so without the file lock two runs race and one source's
// watermark is lost.
func TestUpdateWatermarkConcurrent(t *testing.T) {
	t.Parallel()
	opts := &options{dataDir: t.TempDir()}
	sources := []string{"jira", "confluence", "slack", "github"}
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src string) {
			defer wg.Done()
			rec := connector.Record{Source: src, Time: base.Add(time.Duration(i) * time.Hour)}
			if err := updateWatermark(opts, src, "", false, []connector.Record{rec}); err != nil {
				t.Errorf("updateWatermark(%s): %v", src, err)
			}
		}(i, src)
	}
	wg.Wait()

	st, err := opts.loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	for i, src := range sources {
		wm, ok := st.Get(src, "")
		if !ok {
			t.Errorf("source %q cursor lost to a concurrent write", src)
			continue
		}
		if want := base.Add(time.Duration(i) * time.Hour); !wm.Cursor.Equal(want) {
			t.Errorf("source %q cursor = %v, want %v", src, wm.Cursor, want)
		}
	}
}
