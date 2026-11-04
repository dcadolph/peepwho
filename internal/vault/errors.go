package vault

import "errors"

// ErrEncrypted indicates the stored data is encrypted but no key is configured
// to read it. Callers detect it to prompt for a passphrase or point at the key
// environment variables.
var ErrEncrypted = errors.New("vault: data is encrypted but no key is configured")

// ErrKeySize indicates a raw key that is not the required length.
var ErrKeySize = errors.New("vault: key must be 32 bytes")

// ErrKeyMode indicates the configured key cannot read this file: a passphrase
// for a key-sealed file, or a key for a passphrase-sealed file.
var ErrKeyMode = errors.New("vault: wrong key type for this file")

// ErrCorrupt indicates authentication failed on decryption. The everyday cause
// is a wrong key or passphrase, so the message leads with that rather than with
// data corruption, which is rare and indistinguishable at this layer.
var ErrCorrupt = errors.New(
	"vault: wrong key or passphrase (or the file was damaged); could not decrypt")
