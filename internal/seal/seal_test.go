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
		Product: "peepwho", ProductVersion: "test", CreatedAt: "2026-11-03T00:00:00Z",
		Subject: Subject{Type: "org", ID: "org:test"},
		Claim:   Claim{Type: "peepwho.knowledge-risk/1", Payload: json.RawMessage(`{"critical":2}`)},
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
	tests := []struct {
		Mutate       func(t *testing.T, dir string)
		Pin          string
		WantVerified []string
		WantAltered  []string
		WantMissing  []string
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
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := newTestSealDir(t, priv)
			test.Mutate(t, dir)
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
			if got.OK() != test.WantOK {
				t.Errorf("OK = %v, want %v", got.OK(), test.WantOK)
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
