// Package seal signs a directory of files so a reader can check, offline, that
// every file is the one that shipped. A seal is two files beside the ones it
// covers: manifest.json lists each file by size and sha256, and manifest.sig is
// an ed25519 signature over the exact bytes of manifest.json. Nothing is
// canonicalized, so verifying is hashing plus one signature check.
package seal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	// Format names this version of the seal layout.
	Format = "whotoask-seal/1"
	// ManifestName is the file the manifest is written to.
	ManifestName = "manifest.json"
	// SignatureName is the file the signature is written to.
	SignatureName = "manifest.sig"
	// Algorithm names the signature scheme.
	Algorithm = "ed25519"
)

// Subject names what was assessed.
type Subject struct {
	// Type is the kind of thing the claim is about, such as org.
	Type string `json:"type"`
	// ID identifies the subject within its type.
	ID string `json:"id"`
}

// Claim is the signed statement the seal carries.
type Claim struct {
	// Type names the claim's schema, such as whotoask.knowledge-risk/1.
	Type string `json:"type"`
	// Payload is the claim body, sealed as written.
	Payload json.RawMessage `json:"payload"`
}

// File is one sealed file, identified by its path inside the sealed directory.
type File struct {
	// Path is the slash-separated path relative to the sealed directory.
	Path string `json:"path"`
	// Size is the file's length in bytes.
	Size int64 `json:"size"`
	// SHA256 is the hex sha256 of the file's bytes.
	SHA256 string `json:"sha256"`
	// MediaType is the file's media type.
	MediaType string `json:"media_type"`
	// Role says what the file is to the claim, such as summary or report.
	Role string `json:"role"`
}

// Manifest is the signed description of a sealed directory. It is a struct,
// never a map, so its encoding is deterministic.
type Manifest struct {
	// Format names the seal layout version.
	Format string `json:"format"`
	// Product is the program that wrote the seal.
	Product string `json:"product"`
	// ProductVersion is the version of that program.
	ProductVersion string `json:"product_version"`
	// CreatedAt is when the seal was written, RFC3339 in UTC.
	CreatedAt string `json:"created_at"`
	// KeyID is the short identifier of the signing key.
	KeyID string `json:"key_id"`
	// PublicKey is the base64 ed25519 public key that signed the manifest.
	PublicKey string `json:"public_key"`
	// Subject names what was assessed.
	Subject Subject `json:"subject"`
	// Claim is the signed statement.
	Claim Claim `json:"claim"`
	// Files lists every sealed file, sorted by path.
	Files []File `json:"files"`
}

// Signature is the detached signature over the manifest bytes.
type Signature struct {
	// Algorithm names the signature scheme, always ed25519.
	Algorithm string `json:"algorithm"`
	// KeyID is the short identifier of the signing key.
	KeyID string `json:"key_id"`
	// PublicKey is the base64 ed25519 public key.
	PublicKey string `json:"public_key"`
	// ManifestSHA256 is the hex sha256 of the manifest bytes.
	ManifestSHA256 string `json:"manifest_sha256"`
	// Signature is the base64 ed25519 signature over the manifest bytes.
	Signature string `json:"signature"`
}

// Report is the outcome of verifying a sealed directory.
type Report struct {
	// KeyID is the key the manifest was signed with.
	KeyID string `json:"key_id"`
	// PublicKey is the base64 public key the manifest was signed with.
	PublicKey string `json:"public_key"`
	// Claim is the claim type the seal carries.
	Claim string `json:"claim"`
	// Verified lists files whose size and digest match.
	Verified []string `json:"verified"`
	// Altered lists files present on disk whose size or digest differ.
	Altered []string `json:"altered"`
	// Missing lists files the manifest names that are not on disk.
	Missing []string `json:"missing"`
	// Unlisted lists what is on disk that the manifest does not name.
	Unlisted []string `json:"unlisted"`
}

// OK reports whether every sealed file is present and unaltered and the
// directory holds nothing the seal does not cover.
func (r Report) OK() bool {
	return len(r.Altered) == 0 && len(r.Missing) == 0 && len(r.Unlisted) == 0
}

// KeyID returns the short identifier of a public key: the hex of the first
// eight bytes of its sha256.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Write seals the files m lists inside dir. Each file's size and digest are
// read from disk, so the seal covers the bytes that shipped rather than any
// copy held in memory. It fills in the format, key, and file digests, writes
// manifest.json and manifest.sig into dir, and returns the manifest written.
func Write(dir string, m Manifest, priv ed25519.PrivateKey) (Manifest, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || len(priv) != ed25519.PrivateKeySize {
		return Manifest{}, fmt.Errorf("%w: not an ed25519 private key", ErrWrite)
	}
	m.Format = Format
	m.KeyID = KeyID(pub)
	m.PublicKey = base64.StdEncoding.EncodeToString(pub)
	files := make([]File, 0, len(m.Files))
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if err := checkPath(f.Path); err != nil {
			return Manifest{}, err
		}
		if seen[f.Path] {
			return Manifest{}, fmt.Errorf("%w: %s is listed twice", ErrWrite, f.Path)
		}
		seen[f.Path] = true
		size, sum, err := digest(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: %w", ErrWrite, err)
		}
		f.Size, f.SHA256 = size, sum
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	m.Files = files
	if len(m.Claim.Payload) == 0 {
		m.Claim.Payload = json.RawMessage("null")
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: encode manifest: %w", ErrWrite, err)
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	sig, err := json.MarshalIndent(Signature{
		Algorithm:      Algorithm,
		KeyID:          m.KeyID,
		PublicKey:      m.PublicKey,
		ManifestSHA256: hex.EncodeToString(sum[:]),
		Signature:      base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)),
	}, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: encode signature: %w", ErrWrite, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), body, 0o644); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrWrite, err)
	}
	if err := os.WriteFile(filepath.Join(dir, SignatureName), append(sig, '\n'), 0o644); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return m, nil
}

// Verify checks the seal in dir. It verifies the signature over the exact
// manifest bytes, checks every listed file by size and sha256, then walks dir
// for anything the manifest does not name. When pin is set it must be the
// base64 public key the reader obtained out of band, and the seal's key must
// equal it. An error means the manifest cannot be trusted or dir cannot be read
// in full; a nil error with a Report that is not OK means the manifest is
// genuine and the directory no longer matches it.
func Verify(dir, pin string) (Report, error) {
	body, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrManifest, err)
	}
	rawSig, err := os.ReadFile(filepath.Join(dir, SignatureName))
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrManifest, err)
	}
	var sig Signature
	if err := json.Unmarshal(rawSig, &sig); err != nil {
		return Report{}, fmt.Errorf("%w: %s: %w", ErrManifest, SignatureName, err)
	}
	if sig.Algorithm != Algorithm {
		return Report{}, fmt.Errorf("%w: algorithm %q is not %s", ErrSignature, sig.Algorithm, Algorithm)
	}
	sum := sha256.Sum256(body)
	if !strings.EqualFold(sig.ManifestSHA256, hex.EncodeToString(sum[:])) {
		return Report{}, fmt.Errorf("%w: %s does not hash to the digest the signature names", ErrSignature, ManifestName)
	}
	pubBytes, err := base64.StdEncoding.DecodeString(sig.PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return Report{}, fmt.Errorf("%w: the signature carries no usable public key", ErrKey)
	}
	pub := ed25519.PublicKey(pubBytes)
	if KeyID(pub) != sig.KeyID {
		return Report{}, fmt.Errorf("%w: key id %s does not match the public key", ErrKey, sig.KeyID)
	}
	if pin != "" {
		pinned, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pin))
		if err != nil || !bytes.Equal(pinned, pubBytes) {
			return Report{}, fmt.Errorf("%w: signed by %s, not the key given", ErrKey, sig.KeyID)
		}
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil || !ed25519.Verify(pub, body, sigBytes) {
		return Report{}, fmt.Errorf("%w: %s was changed after it was signed", ErrSignature, ManifestName)
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Report{}, fmt.Errorf("%w: %s: %w", ErrManifest, ManifestName, err)
	}
	if m.Format != Format {
		return Report{}, fmt.Errorf("%w: format %q is not %s", ErrManifest, m.Format, Format)
	}
	if m.PublicKey != sig.PublicKey || m.KeyID != sig.KeyID {
		return Report{}, fmt.Errorf("%w: the manifest names a different key than signed it", ErrKey)
	}
	for _, f := range m.Files {
		if err := checkPath(f.Path); err != nil {
			return Report{}, err
		}
	}

	report := Report{
		KeyID: m.KeyID, PublicKey: m.PublicKey, Claim: m.Claim.Type,
		Verified: []string{}, Altered: []string{}, Missing: []string{}, Unlisted: []string{},
	}
	for _, f := range m.Files {
		size, digest, err := digest(filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case os.IsNotExist(err):
			report.Missing = append(report.Missing, f.Path)
		case err != nil, size != f.Size, !strings.EqualFold(digest, f.SHA256):
			report.Altered = append(report.Altered, f.Path)
		default:
			report.Verified = append(report.Verified, f.Path)
		}
	}
	if report.Unlisted, err = unlisted(dir, m.Files); err != nil {
		return Report{}, err
	}
	return report, nil
}

// unlisted returns the slash-separated path of everything under dir that the
// seal does not cover, sorted. Files at any depth count. Directories are walked
// and never reported themselves. A symbolic link or any other entry that is not
// a regular file is reported by its own path and never followed, so the walk
// reads nothing outside dir. Only manifest.json and manifest.sig at the top of
// dir, the files the manifest names, and folder metadata are left out. A
// directory that cannot be read is an error, since nothing under it can be
// ruled out.
func unlisted(dir string, files []File) ([]string, error) {
	listed := map[string]bool{ManifestName: true, SignatureName: true}
	for _, f := range files {
		listed[f.Path] = true
	}
	found := []string{}
	err := fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir(), listed[p]:
			return nil
		case d.Type().IsRegular() && isFolderMetadata(path.Base(p)):
			return nil
		}
		found = append(found, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrManifest, err)
	}
	slices.Sort(found)
	return found, nil
}

// isFolderMetadata reports whether name is a file an operating system writes
// into a folder a person merely opens: the Finder's view settings, or the
// Explorer's thumbnail cache and view settings. The match is exact, so a name
// that only resembles one of these is still reported.
func isFolderMetadata(name string) bool {
	switch name {
	case ".DS_Store", "Thumbs.db", "desktop.ini":
		return true
	}
	return false
}

// checkPath rejects any manifest path that could name a file outside the
// sealed directory, or the seal's own files.
func checkPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: empty path", ErrPath)
	case strings.Contains(p, ".."), strings.HasPrefix(p, "/"), strings.Contains(p, `\`):
		return fmt.Errorf("%w: %q", ErrPath, p)
	case !filepath.IsLocal(filepath.FromSlash(p)):
		return fmt.Errorf("%w: %q", ErrPath, p)
	case p == ManifestName, p == SignatureName:
		return fmt.Errorf("%w: a seal cannot cover %s", ErrPath, p)
	}
	return nil
}

// digest returns a file's size and hex sha256, streaming rather than holding
// the file in memory.
func digest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
