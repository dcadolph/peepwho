// Package vault encrypts peepwho's on-disk index at rest. A Codec transforms file
// bytes on their way to and from disk: Plain passes them through, and Cipher
// seals them with authenticated encryption under a 32-byte key or a passphrase.
// Keys are held only in memory, never serialized, and never logged.
//
// The on-disk format is a fixed magic prefix, a one-byte key mode, an optional
// passphrase salt, the AES-256-GCM nonce, and the ciphertext with its tag. The
// header bytes are authenticated as additional data, so tampering with the mode
// or salt fails the open. A file without the magic prefix is treated as plain
// JSON, so an index written before encryption was enabled still loads.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Format versions. The prefix marks an encrypted file, and its trailing
// character is the version. Plain JSON starts with '{', so the two are never
// confused. Every version is the same length, which is what lets a caller peek
// a fixed number of bytes to tell an encrypted file from a plain one.
const (
	// magicV1 is the original format. Its passphrase keys were derived with
	// weaker Argon2id parameters, so it is still read but never written.
	magicV1 = "PEEPWHOv1"
	// magicV2 derives passphrase keys with the parameters argonTime names. It
	// is what every write produces.
	magicV2 = "PEEPWHOv2"
	// magic is the version new files are written in.
	magic = magicV2
)

// MagicLen is the number of leading bytes IsEncrypted inspects, so a caller can
// peek a file's prefix instead of reading it whole.
const MagicLen = len(magic)

// Key modes distinguish a raw-key file from a passphrase-derived one.
const (
	// modeKey seals under a 32-byte key supplied directly.
	modeKey byte = 1
	// modePass seals under a key derived from a passphrase and a stored salt.
	modePass byte = 2
)

// keyLen is the AES-256 key length. saltLen is the passphrase salt length.
const (
	keyLen  = 32
	saltLen = 16
)

// Argon2id derivation parameters for passphrase mode. They are bound into the
// format version, so a change here needs a new version that keeps reading the
// old one. These are RFC 9106's second recommended profile: 64 MiB of memory
// with three passes. The first version used a single pass, which is the memory
// of this profile with the time cost of the other, and weaker than either.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
)

// argonTimeV1 is the single pass the original format used. Files written then
// still open with it.
const argonTimeV1 = 1

// argonTimeFor returns the pass count a file of the given version was sealed
// with.
func argonTimeFor(version string) uint32 {
	if version == magicV1 {
		return argonTimeV1
	}
	return argonTime
}

// Codec transforms file contents at rest.
type Codec interface {
	// Encode returns the on-disk form of plaintext.
	Encode(plaintext []byte) ([]byte, error)
	// Decode returns the plaintext for stored bytes. Stored may be the encoded
	// form this codec writes or an older plain file, which is returned as is.
	Decode(stored []byte) ([]byte, error)
}

// IsEncrypted reports whether data carries a vault magic prefix of any version.
func IsEncrypted(data []byte) bool { return versionOf(data) != "" }

// ModeLen is the number of leading bytes NeedsKey inspects: the magic prefix
// plus the key-mode byte behind it.
const ModeLen = MagicLen + 1

// NeedsKey reports whether data is an encrypted file sealed under a raw key,
// which no passphrase can ever open. A caller about to prompt for a passphrase
// checks this first, so the person is pointed at the key variable instead of
// being asked for something that cannot work.
func NeedsKey(data []byte) bool {
	version := versionOf(data)
	if version == "" || len(data) <= len(version) {
		return false
	}
	return data[len(version)] == modeKey
}

// versionOf returns the format version prefixing data, or empty when data is
// not an encrypted file.
func versionOf(data []byte) string {
	for _, v := range []string{magicV2, magicV1} {
		if bytes.HasPrefix(data, []byte(v)) {
			return v
		}
	}
	return ""
}

// Plain is the identity codec: it writes and reads bytes unchanged. Decoding
// encrypted data returns ErrEncrypted so a caller can prompt for a key.
type Plain struct{}

// Encode returns plaintext unchanged.
func (Plain) Encode(plaintext []byte) ([]byte, error) { return plaintext, nil }

// Decode returns stored unchanged, or ErrEncrypted when it is encrypted.
func (Plain) Decode(stored []byte) ([]byte, error) {
	if IsEncrypted(stored) {
		return nil, ErrEncrypted
	}
	return stored, nil
}

// Cipher seals data with authenticated encryption. Exactly one of a raw key or
// a passphrase is set, chosen by the constructor.
type Cipher struct {
	// key is the 32-byte raw key for key mode; nil in passphrase mode.
	key []byte
	// passphrase derives the key per file in passphrase mode; nil in key mode.
	passphrase []byte
}

// NewKeyCipher returns a Cipher that seals under key, which must be 32 bytes.
func NewKeyCipher(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, ErrKeySize
	}
	return &Cipher{key: bytes.Clone(key)}, nil
}

// NewPassphraseCipher returns a Cipher that derives its key from passphrase with
// Argon2id and a per-file salt.
func NewPassphraseCipher(passphrase []byte) *Cipher {
	return &Cipher{passphrase: bytes.Clone(passphrase)}
}

// Encode seals plaintext. In key mode it uses the raw key; in passphrase mode it
// draws a fresh salt, derives a key, and stores the salt in the header.
func (c *Cipher) Encode(plaintext []byte) ([]byte, error) {
	if c.key != nil {
		return seal(c.key, modeKey, nil, plaintext)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("vault: salt: %w", err)
	}
	key := argon2.IDKey(c.passphrase, salt, argonTime, argonMemory, argonThreads, keyLen)
	return seal(key, modePass, salt, plaintext)
}

// Decode returns plaintext for stored. Data without the magic prefix predates
// encryption and is returned unchanged, so enabling a key migrates on the next
// write. A key that cannot read the file's mode returns ErrKeyMode; a wrong key
// or tampered data returns ErrCorrupt.
func (c *Cipher) Decode(stored []byte) ([]byte, error) {
	version := versionOf(stored)
	if version == "" {
		return stored, nil
	}
	buf := stored[len(version):]
	if len(buf) < 1 {
		return nil, ErrCorrupt
	}
	mode := buf[0]
	buf = buf[1:]

	var key, salt []byte
	switch mode {
	case modeKey:
		if c.key == nil {
			return nil, fmt.Errorf("%w: file needs a key, a passphrase is set", ErrKeyMode)
		}
		key = c.key
	case modePass:
		if c.passphrase == nil {
			return nil, fmt.Errorf("%w: file needs a passphrase, a key is set", ErrKeyMode)
		}
		if len(buf) < saltLen {
			return nil, ErrCorrupt
		}
		salt = buf[:saltLen]
		buf = buf[saltLen:]
		// The pass count is whatever the file was sealed with, so a file
		// written by an older version still opens.
		key = argon2.IDKey(
			c.passphrase, salt, argonTimeFor(version), argonMemory, argonThreads, keyLen)
	default:
		return nil, ErrCorrupt
	}

	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(buf) < gcm.NonceSize() {
		return nil, ErrCorrupt
	}
	nonce := buf[:gcm.NonceSize()]
	ciphertext := buf[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, header(version, mode, salt))
	if err != nil {
		// The underlying error is always "message authentication failed",
		// which is crypto-internal noise to a user, so it is not surfaced.
		return nil, ErrCorrupt
	}
	return plaintext, nil
}

// seal builds an encrypted file in the current format: the header, a fresh
// nonce, and the ciphertext. The header is authenticated as additional data,
// so the version, the key mode, and the salt cannot be altered.
func seal(key []byte, mode byte, salt, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("vault: nonce: %w", err)
	}
	head := header(magic, mode, salt)
	out := append([]byte(nil), head...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, head), nil
}

// header returns the authenticated prefix: the format version, the key mode,
// and any salt.
func header(version string, mode byte, salt []byte) []byte {
	out := make([]byte, 0, len(version)+1+len(salt))
	out = append(out, version...)
	out = append(out, mode)
	out = append(out, salt...)
	return out
}

// newGCM returns an AES-256-GCM AEAD for a 32-byte key.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm: %w", err)
	}
	return gcm, nil
}
