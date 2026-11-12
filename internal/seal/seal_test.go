package seal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// testKey returns a fresh ed25519 key pair.
func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

// newTestSealDir writes three files into a temp dir and seals them.
func newTestSealDir(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"summary.md":    "# Summary\n",
		"findings.json": `{"critical":2}` + "\n",
		"sub/notes.txt": "notes\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Write(dir, Manifest{
		Product: "whotoask", ProductVersion: "test", CreatedAt: "2026-11-03T00:00:00Z",
		Subject: Subject{Type: "org", ID: "org:test"},
		Claim:   Claim{Type: "whotoask.knowledge-risk/1", Payload: json.RawMessage(`{"critical":2}`)},
		Files: []File{
			{Path: "summary.md", MediaType: "text/markdown", Role: "summary"},
			{Path: "findings.json", MediaType: "application/json", Role: "findings"},
			{Path: "sub/notes.txt", MediaType: "text/plain", Role: "notes"},
		},
	}, priv)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// TestVerify covers every verdict a sealed directory can reach.
func TestVerify(t *testing.T) {
	t.Parallel()
	pub, priv := testKey(t)
	other, _ := testKey(t)
	all := []string{"findings.json", "sub/notes.txt", "summary.md"}
	tests := []struct {
		Mutate       func(t *testing.T, dir string)
		Pin          string
		ViaLink      bool
		WantVerified []string
		WantAltered  []string
		WantMissing  []string
		WantUnlisted []string
		WantOK       bool
		Want         error
	}{{ // Test 0: An untouched seal verifies every file.
		Mutate:       func(*testing.T, string) {},
		WantVerified: []string{"findings.json", "sub/notes.txt", "summary.md"}, WantOK: true,
	}, { // Test 1: One byte appended to a file reads as altered.
		Mutate:       func(t *testing.T, dir string) { appendByte(t, filepath.Join(dir, "summary.md")) },
		WantVerified: []string{"findings.json", "sub/notes.txt"}, WantAltered: []string{"summary.md"},
	}, { // Test 2: A same-length edit reads as altered by digest.
		Mutate: func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "findings.json"), `{"critical":0}`+"\n")
		},
		WantVerified: []string{"sub/notes.txt", "summary.md"}, WantAltered: []string{"findings.json"},
	}, { // Test 3: A deleted file reads as missing, not altered.
		Mutate: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "sub", "notes.txt")); err != nil {
				t.Fatal(err)
			}
		},
		WantVerified: []string{"findings.json", "summary.md"}, WantMissing: []string{"sub/notes.txt"},
	}, { // Test 4: One character of the manifest changed breaks the signature.
		Mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, ManifestName)
			body := readFile(t, path)
			writeFile(t, path, string(bytes.Replace(body, []byte(`"critical": 2`), []byte(`"critical": 0`), 1)))
		},
		Want: ErrSignature,
	}, { // Test 5: A manifest edited and its digest updated still fails the signature.
		Mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, ManifestName)
			body := bytes.Replace(readFile(t, path), []byte("org:test"), []byte("org:fake"), 1)
			writeFile(t, path, string(body))
			rewriteSig(t, dir, func(s *Signature) { s.ManifestSHA256 = sha256Hex(body) })
		},
		Want: ErrSignature,
	}, { // Test 6: The reader pins the right key.
		Mutate: func(*testing.T, string) {}, Pin: base64.StdEncoding.EncodeToString(pub),
		WantVerified: []string{"findings.json", "sub/notes.txt", "summary.md"}, WantOK: true,
	}, { // Test 7: The reader pins a different key.
		Mutate: func(*testing.T, string) {}, Pin: base64.StdEncoding.EncodeToString(other),
		Want: ErrKey,
	}, { // Test 8: The pin is not base64.
		Mutate: func(*testing.T, string) {}, Pin: "WRONGKEY!",
		Want: ErrKey,
	}, { // Test 9: The key id does not match the public key.
		Mutate: func(t *testing.T, dir string) {
			rewriteSig(t, dir, func(s *Signature) { s.KeyID = "0000000000000000" })
		},
		Want: ErrKey,
	}, { // Test 10: The signature names another key entirely.
		Mutate: func(t *testing.T, dir string) {
			rewriteSig(t, dir, func(s *Signature) {
				s.PublicKey = base64.StdEncoding.EncodeToString(other)
				s.KeyID = KeyID(other)
			})
		},
		Want: ErrSignature,
	}, { // Test 11: No manifest at all.
		Mutate: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ManifestName)); err != nil {
				t.Fatal(err)
			}
		},
		Want: ErrManifest,
	}, { // Test 12: No signature at all.
		Mutate: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, SignatureName)); err != nil {
				t.Fatal(err)
			}
		},
		Want: ErrManifest,
	}, { // Test 13: A file replaced by a directory reads as altered.
		Mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, "summary.md")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		WantVerified: []string{"findings.json", "sub/notes.txt"}, WantAltered: []string{"summary.md"},
	}, { // Test 14: A file added at the top is unlisted.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "extra.txt") },
		WantVerified: all, WantUnlisted: []string{"extra.txt"},
	}, { // Test 15: A file added beside a sealed file in a subdirectory is unlisted.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "sub/extra.txt") },
		WantVerified: all, WantUnlisted: []string{"sub/extra.txt"},
	}, { // Test 16: A file added in a new nested directory is unlisted.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "new/deep/extra.txt") },
		WantVerified: all, WantUnlisted: []string{"new/deep/extra.txt"},
	}, { // Test 17: An added directory holding no file is not reported.
		Mutate: func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "empty", "deeper"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		WantVerified: all, WantOK: true,
	}, { // Test 18: A symbolic link to a file outside the directory is reported.
		Mutate: func(t *testing.T, dir string) {
			link(t, plant(t, t.TempDir(), "outside.json"), filepath.Join(dir, "systems.json"))
		},
		WantVerified: all, WantUnlisted: []string{"systems.json"},
	}, { // Test 19: A symbolic link to a directory is reported itself and never followed.
		Mutate: func(t *testing.T, dir string) {
			outside := t.TempDir()
			plant(t, outside, "inside.txt")
			link(t, outside, filepath.Join(dir, "linked"))
		},
		WantVerified: all, WantUnlisted: []string{"linked"},
	}, { // Test 20: A symbolic link that points nowhere is reported.
		Mutate: func(t *testing.T, dir string) {
			link(t, filepath.Join(t.TempDir(), "absent"), filepath.Join(dir, "sub", "dangling"))
		},
		WantVerified: all, WantUnlisted: []string{"sub/dangling"},
	}, { // Test 21: Finder metadata at any depth is left out.
		Mutate: func(t *testing.T, dir string) {
			plant(t, dir, ".DS_Store")
			plant(t, dir, "sub/.DS_Store")
		},
		WantVerified: all, WantOK: true,
	}, { // Test 22: The Explorer thumbnail cache is left out.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "Thumbs.db") },
		WantVerified: all, WantOK: true,
	}, { // Test 23: The Explorer folder settings file is left out.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "desktop.ini") },
		WantVerified: all, WantOK: true,
	}, { // Test 24: A symbolic link under a metadata name is still reported.
		Mutate: func(t *testing.T, dir string) {
			link(t, plant(t, t.TempDir(), "outside.json"), filepath.Join(dir, ".DS_Store"))
		},
		WantVerified: all, WantUnlisted: []string{".DS_Store"},
	}, { // Test 25: A file inside a directory with a metadata name is still reported.
		Mutate:       func(t *testing.T, dir string) { plant(t, dir, "Thumbs.db/extra.txt") },
		WantVerified: all, WantUnlisted: []string{"Thumbs.db/extra.txt"},
	}, { // Test 26: Names that only resemble folder metadata are reported.
		Mutate: func(t *testing.T, dir string) {
			plant(t, dir, "._summary.md")
			plant(t, dir, ".DS_Store.json")
			plant(t, dir, "__MACOSX/summary.md")
		},
		WantVerified: all,
		WantUnlisted: []string{".DS_Store.json", "._summary.md", "__MACOSX/summary.md"},
	}, { // Test 27: Seal files below the top of the directory are reported.
		Mutate: func(t *testing.T, dir string) {
			plant(t, dir, "sub/"+ManifestName)
			plant(t, dir, "sub/"+SignatureName)
		},
		WantVerified: all, WantUnlisted: []string{"sub/manifest.json", "sub/manifest.sig"},
	}, { // Test 28: The files of a version-control directory are reported.
		Mutate: func(t *testing.T, dir string) {
			plant(t, dir, ".git/HEAD")
			plant(t, dir, ".git/refs/heads/main")
		},
		WantVerified: all, WantUnlisted: []string{".git/HEAD", ".git/refs/heads/main"},
	}, { // Test 29: An added file is reported together with an altered one.
		Mutate: func(t *testing.T, dir string) {
			appendByte(t, filepath.Join(dir, "summary.md"))
			plant(t, dir, "extra.txt")
		},
		WantVerified: []string{"findings.json", "sub/notes.txt"},
		WantAltered:  []string{"summary.md"}, WantUnlisted: []string{"extra.txt"},
	}, { // Test 30: A renamed file is missing under the old name and unlisted under the new.
		Mutate: func(t *testing.T, dir string) {
			from, to := filepath.Join(dir, "summary.md"), filepath.Join(dir, "summary-v2.md")
			if err := os.Rename(from, to); err != nil {
				t.Fatal(err)
			}
		},
		WantVerified: []string{"findings.json", "sub/notes.txt"},
		WantMissing:  []string{"summary.md"}, WantUnlisted: []string{"summary-v2.md"},
	}, { // Test 31: Unlisted paths are sorted as strings, not in walk order.
		Mutate: func(t *testing.T, dir string) {
			plant(t, dir, "sub/extra.txt")
			plant(t, dir, "sub.txt")
			plant(t, dir, "a.txt")
		},
		WantVerified: all, WantUnlisted: []string{"a.txt", "sub.txt", "sub/extra.txt"},
	}, { // Test 32: A directory named through a symbolic link is still walked.
		Mutate:  func(t *testing.T, dir string) { plant(t, dir, "sub/extra.txt") },
		ViaLink: true, WantVerified: all, WantUnlisted: []string{"sub/extra.txt"},
	}, { // Test 33: A directory that cannot be listed fails the check outright.
		Mutate: func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a directory whatever its mode")
			}
			locked := filepath.Dir(plant(t, dir, "locked/extra.txt"))
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		},
		Want: ErrManifest,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := newTestSealDir(t, priv)
			test.Mutate(t, dir)
			if test.ViaLink {
				through := filepath.Join(t.TempDir(), "through")
				link(t, dir, through)
				dir = through
			}
			got, err := Verify(dir, test.Pin)
			if !errors.Is(err, test.Want) {
				t.Fatalf("Verify error = %v, want %v", err, test.Want)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(test.WantVerified, got.Verified, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("verified mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantAltered, got.Altered, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("altered mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantMissing, got.Missing, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("missing mismatch (-want +got):\n%s", diff)
			}
			diff := cmp.Diff(test.WantUnlisted, got.Unlisted, cmpopts.EquateEmpty())
			if diff != "" {
				t.Errorf("unlisted mismatch (-want +got):\n%s", diff)
			}
			if got.OK() != test.WantOK {
				t.Errorf("OK = %v, want %v", got.OK(), test.WantOK)
			}
		})
	}
}

// TestReportOK checks any altered, missing, or unlisted entry fails a report.
func TestReportOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In     Report
		WantOK bool
	}{
		{In: Report{}, WantOK: true},                        // Test 0: Nothing recorded.
		{In: Report{Verified: []string{"a"}}, WantOK: true}, // Test 1: Only verified files.
		{In: Report{Altered: []string{"a"}}},                // Test 2: An altered file.
		{In: Report{Missing: []string{"a"}}},                // Test 3: A missing file.
		{In: Report{Unlisted: []string{"a"}}},               // Test 4: An unlisted file.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := test.In.OK(); got != test.WantOK {
				t.Errorf("OK = %v, want %v", got, test.WantOK)
			}
		})
	}
}

// TestReportJSON checks the report names unlisted entries under the unlisted
// key, and writes an empty list rather than null when there are none.
func TestReportJSON(t *testing.T) {
	t.Parallel()
	_, priv := testKey(t)
	tests := []struct {
		Extra        string
		WantUnlisted string
	}{
		{WantUnlisted: `[]`}, // Test 0: Nothing added.
		{Extra: "extra.txt", WantUnlisted: `["extra.txt"]`}, // Test 1: One added file.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := newTestSealDir(t, priv)
			if test.Extra != "" {
				plant(t, dir, test.Extra)
			}
			report, err := Verify(dir, "")
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.WantUnlisted, string(fields["unlisted"])); diff != "" {
				t.Errorf("unlisted field mismatch (-want +got):\n%s\nreport: %s", diff, data)
			}
		})
	}
}

// TestWriteRejectsUnsafePaths checks no listed path can reach outside the
// sealed directory or name the seal's own files.
func TestWriteRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	_, priv := testKey(t)
	tests := []struct {
		Path string
		Want error
	}{
		{Path: "../outside", Want: ErrPath},  // Test 0: Parent escape.
		{Path: "a/../../b", Want: ErrPath},   // Test 1: Nested parent escape.
		{Path: "/etc/passwd", Want: ErrPath}, // Test 2: Absolute path.
		{Path: `sub\file`, Want: ErrPath},    // Test 3: Backslash.
		{Path: "", Want: ErrPath},            // Test 4: Empty path.
		{Path: ManifestName, Want: ErrPath},  // Test 5: The manifest itself.
		{Path: SignatureName, Want: ErrPath}, // Test 6: The signature itself.
		{Path: "absent.txt", Want: ErrWrite}, // Test 7: A file that does not exist.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			_, err := Write(dir, Manifest{Files: []File{{Path: test.Path}}}, priv)
			if !errors.Is(err, test.Want) {
				t.Errorf("Write(%q) error = %v, want %v", test.Path, err, test.Want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ManifestName)); statErr == nil {
				t.Error("a refused seal still wrote a manifest")
			}
		})
	}
}

// TestVerifyRejectsSignedEscape checks a validly signed manifest that names a
// path outside the directory is refused before any file is read.
func TestVerifyRejectsSignedEscape(t *testing.T) {
	t.Parallel()
	pub, priv := testKey(t)
	dir := t.TempDir()
	body, err := json.MarshalIndent(Manifest{
		Format: Format, KeyID: KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub),
		Files: []File{{Path: "../secret", Size: 1, SHA256: "00"}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ManifestName), string(body))
	sig, err := json.Marshal(Signature{
		Algorithm: Algorithm, KeyID: KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub),
		ManifestSHA256: sha256Hex(body),
		Signature:      base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, SignatureName), string(sig))
	if _, err := Verify(dir, ""); !errors.Is(err, ErrPath) {
		t.Errorf("Verify error = %v, want %v", err, ErrPath)
	}
}

// TestWriteIsDeterministic checks the same inputs produce the same manifest
// bytes, which is what makes a struct manifest safe to sign without
// canonicalization.
func TestWriteIsDeterministic(t *testing.T) {
	t.Parallel()
	_, priv := testKey(t)
	a := readFile(t, filepath.Join(newTestSealDir(t, priv), ManifestName))
	b := readFile(t, filepath.Join(newTestSealDir(t, priv), ManifestName))
	if diff := cmp.Diff(string(a), string(b)); diff != "" {
		t.Errorf("manifest bytes differ (-first +second):\n%s", diff)
	}
}

// TestKeyID checks the key id is sixteen hex characters and stable.
func TestKeyID(t *testing.T) {
	t.Parallel()
	pub, _ := testKey(t)
	id := KeyID(pub)
	if len(id) != 16 {
		t.Errorf("KeyID length = %d, want 16", len(id))
	}
	if KeyID(pub) != id {
		t.Error("KeyID is not stable for the same key")
	}
}

// plant writes a file the seal does not cover at the slash-separated name
// under dir, creating its parent directories, and returns the file's path.
func plant(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "planted\n")
	return path
}

// link creates a symbolic link at path that points to target.
func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// appendByte adds one byte to the end of a file.
func appendByte(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, string(readFile(t, path))+"x")
}

// readFile returns a file's bytes or fails the test.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// writeFile replaces a file's contents or fails the test.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// rewriteSig edits the signature file in place.
func rewriteSig(t *testing.T, dir string, edit func(*Signature)) {
	t.Helper()
	path := filepath.Join(dir, SignatureName)
	var s Signature
	if err := json.Unmarshal(readFile(t, path), &s); err != nil {
		t.Fatal(err)
	}
	edit(&s)
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(out))
}

// sha256Hex returns the hex sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
