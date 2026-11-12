package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/seal"
)

// newTestSealedDir seals one file into a temp dir with a fresh key.
func newTestSealedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, riskJSONName), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seal.Write(dir, seal.Manifest{
		Files: []seal.File{{Path: riskJSONName, MediaType: "application/json", Role: "report"}},
	}, priv)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return dir
}

// TestRenderVerify checks the human verdict names every altered, missing, and
// unlisted entry and counts each in the failed line.
func TestRenderVerify(t *testing.T) {
	t.Parallel()
	const named, given = ", signed by the key it names, k1\n", ", signed by the key you gave, k1\n"
	tests := []struct {
		In      seal.Report
		Pinned  bool
		WantOut string
	}{{ // Test 0: A clean report says verified and nothing else.
		In:      seal.Report{KeyID: "k1", Verified: []string{"risk.json"}},
		WantOut: "verified: 1 file matches the seal" + named,
	}, { // Test 1: An unlisted entry gets its own line and fails the verdict.
		In: seal.Report{
			KeyID: "k1", Verified: []string{"risk.json"}, Unlisted: []string{"extra.txt"},
		},
		WantOut: "unlisted: extra.txt\n" +
			"failed: 1 verified, 0 altered, 0 missing, 1 unlisted" + named,
	}, { // Test 2: Altered, missing, and unlisted entries print in that order and are counted.
		In: seal.Report{
			KeyID: "k1", Verified: []string{"a", "b"}, Altered: []string{"c"},
			Missing: []string{"d"}, Unlisted: []string{"e", "sub/f"},
		},
		Pinned: true,
		WantOut: "altered: c\nmissing: d\nunlisted: e\nunlisted: sub/f\n" +
			"failed: 2 verified, 1 altered, 1 missing, 2 unlisted" + given,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			renderVerify(&out, test.In, test.Pinned, style{})
			if diff := cmp.Diff(test.WantOut, out.String()); diff != "" {
				t.Errorf("output mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestVerifyUnlisted runs the verify command over a sealed directory that had
// something added to it, in both output forms.
func TestVerifyUnlisted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Extra   string
		Flag    string
		WantOut string
		WantErr string
		Want    error
	}{{ // Test 0: Nothing added verifies.
		Flag: "--human", WantOut: "verified: 1 file matches the seal",
	}, { // Test 1: An added file is named and fails the check.
		Extra: "extra.txt", Flag: "--human", WantOut: "unlisted: extra.txt\n",
		WantErr: "0 altered, 0 missing, 1 unlisted", Want: ErrSeal,
	}, { // Test 2: An added file below a new directory is named by its full path.
		Extra: "more/systems.json", Flag: "--human", WantOut: "unlisted: more/systems.json\n",
		WantErr: "0 altered, 0 missing, 1 unlisted", Want: ErrSeal,
	}, { // Test 3: The JSON form carries the added file under the unlisted key.
		Extra: "extra.txt", Flag: "--json", WantOut: `"unlisted":["extra.txt"]`,
		WantErr: "0 altered, 0 missing, 1 unlisted", Want: ErrSeal,
	}, { // Test 4: The JSON form of a clean seal carries an empty unlisted list.
		Flag: "--json", WantOut: `"unlisted":[]`,
	}, { // Test 5: Folder metadata alone does not fail the check.
		Extra: ".DS_Store", Flag: "--human", WantOut: "verified: 1 file matches the seal",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := newTestSealedDir(t)
			if test.Extra != "" {
				path := filepath.Join(dir, filepath.FromSlash(test.Extra))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("planted\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, _, err := runCmd(t, "verify", dir, "--data-dir", t.TempDir(), test.Flag)
			if !errors.Is(err, test.Want) {
				t.Fatalf("verify error = %v, want %v", err, test.Want)
			}
			if err != nil && !strings.Contains(err.Error(), test.WantErr) {
				t.Errorf("verify error = %q, want it to hold %q", err, test.WantErr)
			}
			if !bytes.Contains(out, []byte(test.WantOut)) {
				t.Errorf("verify output lacks %q:\n%s", test.WantOut, out)
			}
		})
	}
}
