package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttestKeyNamesTheDirectoryItCannotUse checks the failure says which path
// was refused.
//
// The default data directory sits under the user's home, and a hardened service
// unit usually cannot reach it: the public demo ran for days signing with a
// throwaway key because the message said only "permission denied" without
// saying where, so nobody could see that one flag fixed it.
func TestAttestKeyNamesTheDirectoryItCannotUse(t *testing.T) {
	t.Parallel()
	// A path under a file can never be created, on any platform.
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	o := &options{dataDir: filepath.Join(blocker, "nope")}
	_, err := o.attestKey()
	if err == nil {
		t.Fatal("a key was made under an impossible path")
		return
	}
	if !strings.Contains(err.Error(), o.dataDir) {
		t.Errorf("error = %q, want it to name %q so the remedy is visible", err, o.dataDir)
	}
}

// TestAttestKeyIsStableAcrossRuns checks a writable data directory keeps one
// key, which is the whole basis of an attestation identifying an install.
func TestAttestKeyIsStableAcrossRuns(t *testing.T) {
	t.Parallel()
	o := &options{dataDir: t.TempDir()}
	first, err := o.attestKey()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := o.attestKey()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !first.Equal(second) {
		t.Error("two runs produced different keys, so a sealed finding cannot identify the install")
	}
}
