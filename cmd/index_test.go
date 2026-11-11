package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/engine"
)

// TestIndexSlackExportHonorsPrivatePolicy verifies the private-channel policy
// governs a Slack export the way it governs the live source. A locked policy
// that denies private channels refuses --include-private before anything is
// read, the same policy leaves a run without the flag alone, and a policy that
// allows private channels reads the private one. Each case points the policy
// variable at its own file, so neither the test nor its cases run in parallel.
func TestIndexSlackExportHonorsPrivatePolicy(t *testing.T) {
	const deny = `{"mode":"strict","locked":true,"private_channels":"deny"}`
	tests := []struct {
		Policy       string
		WantMsg      string
		WantChannels []string
		Want         error
		Private      bool
	}{{ // Test 0: A locked deny policy refuses the private read and writes no index.
		Policy: deny, Private: true,
		WantMsg: "private-channel ingest is disabled by policy", Want: ErrBadArgs,
	}, { // Test 1: The same policy leaves a run without the flag alone.
		Policy: deny, WantChannels: []string{"engineering", "general"},
	}, { // Test 2: A policy that allows private channels reads the private one.
		Policy:  `{"mode":"strict","locked":true,"private_channels":"allow"}`,
		Private: true, WantChannels: []string{"engineering", "general", "private-ops"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(policyEnvVar, writePolicy(t, t.TempDir(), "policy.json", test.Policy))
			args := []string{
				"index", "--data-dir", dir, "--source", "slack-export", "--file", exportZip,
				"--since-days", "36500",
			}
			if test.Private {
				args = append(args, "--include-private")
			}
			_, stderr, err := runCmd(t, args...)
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v\n%s", err, test.Want, stderr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), test.WantMsg) {
					t.Errorf("message %q missing %q", err, test.WantMsg)
				}
				_, statErr := os.Stat(engine.Dir(dir).Index())
				if !errors.Is(statErr, fs.ErrNotExist) {
					t.Errorf("a refused run left an index behind: stat err = %v", statErr)
				}
				return
			}

			out, _, err := runCmd(t, "directory", "channels", "--data-dir", dir)
			if err != nil {
				t.Fatalf("directory channels: %v", err)
			}
			var listed struct {
				Channels []struct {
					Name string `json:"name"`
				} `json:"channels"`
			}
			if err := json.Unmarshal(out, &listed); err != nil {
				t.Fatalf("decode channels: %v\n%s", err, out)
			}
			var got []string
			for _, ch := range listed.Channels {
				got = append(got, ch.Name)
			}
			sort.Strings(got)
			if diff := cmp.Diff(test.WantChannels, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("indexed channels mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
