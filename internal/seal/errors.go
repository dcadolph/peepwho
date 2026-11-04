package seal

import "errors"

var (
	// ErrPath indicates a manifest path that could escape the sealed directory.
	ErrPath = errors.New("unsafe path")
	// ErrManifest indicates a manifest or signature file that cannot be read or parsed.
	ErrManifest = errors.New("unreadable seal")
	// ErrSignature indicates the signature does not verify over the manifest bytes.
	ErrSignature = errors.New("signature does not verify")
	// ErrKey indicates the signing key does not match the key the seal or the reader names.
	ErrKey = errors.New("key mismatch")
	// ErrWrite indicates the seal could not be written.
	ErrWrite = errors.New("seal write failed")
)
