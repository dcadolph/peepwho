package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/vault"
)

// TestGuardArchive covers the gate on keeping the words of a conversation. It
// is the only thing standing between a run and full conversation text on disk.
func TestGuardArchive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Opts func(t *testing.T) *options
		Want error
	}{{ // Test 0: The organization's policy forbids retaining content.
		Opts: func(t *testing.T) *options {
			return &options{dataDir: t.TempDir(), pol: policy.New(policy.Open, false).WithoutArchive()}
		},
		Want: ErrBadArgs,
	}, { // Test 1: Allowed by policy, but no key is set to encrypt it.
		Opts: func(t *testing.T) *options {
			o := &options{dataDir: t.TempDir(), pol: policy.New(policy.Open, false)}
			o.setCodec(nil)
			return o
		},
		Want: ErrBadArgs,
	}, { // Test 2: Allowed by policy and encrypted, with no license anywhere.
		Opts: func(t *testing.T) *options {
			o := &options{dataDir: t.TempDir(), pol: policy.New(policy.Open, false)}
			o.setCodec(testArchiveCodec(t))
			return o
		},
		Want: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			cmd.SetErr(&bytes.Buffer{})
			err := guardArchive(cmd, test.Opts(t))
			if !errors.Is(err, test.Want) {
				t.Errorf("guardArchive = %v, want %v", err, test.Want)
			}
		})
	}
}

// testArchiveCodec returns an index cipher with a fixed test key.
func testArchiveCodec(t *testing.T) vault.Codec {
	t.Helper()
	c, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}
