package index

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestWithNow checks that the clock given to New is the build time Save records,
// that the clock given to Load is the one a loaded index reads, and that a nil
// clock falls back to the system clock.
func TestWithNow(t *testing.T) {
	t.Parallel()
	built := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	later := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		NewClock    func() time.Time
		LoadClock   func() time.Time
		WantBuiltAt time.Time
		WantNow     time.Time
	}{{ // Test 0: A pinned clock on both sides is used on both sides.
		NewClock:    func() time.Time { return built },
		LoadClock:   func() time.Time { return later },
		WantBuiltAt: built, WantNow: later,
	}, { // Test 1: A nil clock on load leaves the system clock.
		NewClock:    func() time.Time { return built },
		LoadClock:   nil,
		WantBuiltAt: built,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "index.json")
			if err := New(WithNow(test.NewClock)).Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			ix, err := Load(path, WithNow(test.LoadClock))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := ix.BuiltAt(); !got.Equal(test.WantBuiltAt) {
				t.Errorf("BuiltAt() = %v, want %v", got, test.WantBuiltAt)
			}
			got := ix.now()
			if test.WantNow.IsZero() {
				if time.Since(got) > time.Minute {
					t.Errorf("now() = %v, want the system clock", got)
				}
				return
			}
			if !got.Equal(test.WantNow) {
				t.Errorf("now() = %v, want %v", got, test.WantNow)
			}
		})
	}
}
