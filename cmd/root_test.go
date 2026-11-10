package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestResolveClock checks that WHOTOASK_NOW pins the clock in UTC and that a
// value it cannot parse is an error, never a silent fallback.
func TestResolveClock(t *testing.T) {
	t.Parallel()
	pinned := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		Raw        string
		WantPinned bool
		WantNow    time.Time
		Want       error
	}{{ // Test 0: Unset leaves the system clock.
		Raw: "", WantPinned: false,
	}, { // Test 1: A UTC time pins the clock.
		Raw: "2026-11-01T12:00:00Z", WantPinned: true, WantNow: pinned,
	}, { // Test 2: An offset time pins the same instant in UTC.
		Raw: "2026-11-01T07:00:00-05:00", WantPinned: true, WantNow: pinned,
	}, { // Test 3: A date without a time is refused.
		Raw: "2026-11-01", Want: ErrBadArgs,
	}, { // Test 4: Garbage is refused.
		Raw: "garbage", Want: ErrBadArgs,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			opts := &options{}
			err := opts.resolveClock(test.Raw)
			if !errors.Is(err, test.Want) {
				t.Fatalf("resolveClock(%q) error = %v, want %v", test.Raw, err, test.Want)
			}
			if err != nil {
				if !strings.Contains(err.Error(), nowEnvVar) {
					t.Errorf("error %q does not name %s", err, nowEnvVar)
				}
				if opts.clock != nil {
					t.Error("a refused value set the clock")
				}
				return
			}
			if diff := cmp.Diff(test.WantPinned, opts.clock != nil); diff != "" {
				t.Fatalf("pinned mismatch (-want +got):\n%s", diff)
			}
			if !test.WantPinned {
				if got := opts.now(); time.Since(got) > time.Minute {
					t.Errorf("unpinned now() = %v, want the system clock", got)
				}
				return
			}
			got := opts.now()
			if !got.Equal(test.WantNow) || got.Location() != time.UTC {
				t.Errorf("now() = %v, want %v in UTC", got, test.WantNow)
			}
		})
	}
}

// TestNowEnvPinsIndexAndStatus runs index and status with WHOTOASK_NOW set and
// checks that the saved build time and the reported age come from the pinned
// clock, and that a bad value fails before any command runs. It sets the
// environment, so it cannot run in parallel.
func TestNowEnvPinsIndexAndStatus(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\nJane Roe,jane@x.com,Staff Engineer,Billing,retries\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	t.Setenv(nowEnvVar, "2026-11-01T12:00:00Z")
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	out, _, err := runCmd(t, "status", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var view struct {
		BuiltAt string `json:"built_at"`
		Age     string `json:"age"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		t.Fatalf("decode status: %v\n%s", err, out)
	}
	want := struct{ BuiltAt, Age string }{BuiltAt: "2026-11-01T12:00:00Z", Age: "0s"}
	if diff := cmp.Diff(want, struct{ BuiltAt, Age string }{view.BuiltAt, view.Age}); diff != "" {
		t.Errorf("status mismatch (-want +got):\n%s", diff)
	}

	t.Setenv(nowEnvVar, "garbage")
	_, _, err = runCmd(t, "status", "--data-dir", dir, "--json")
	if !errors.Is(err, ErrBadArgs) || !strings.Contains(err.Error(), nowEnvVar) {
		t.Errorf("status with a bad %s: error = %v, want ErrBadArgs naming it", nowEnvVar, err)
	}
}
