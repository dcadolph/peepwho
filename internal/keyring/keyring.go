// Package keyring resolves peepwho's at-rest encryption key from the environment
// and builds the file codec. PEEPWHO_INDEX_KEY holds a base64-encoded 32-byte key
// for automation, and PEEPWHO_INDEX_PASSPHRASE holds a passphrase for humans. When
// neither is set the index stays plain JSON. The OS keychain is a planned third
// source that will slot in behind the same codec.
package keyring

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/dcadolph/peepwho/internal/vault"
)

// Environment variables that supply the at-rest key.
const (
	// EnvKey holds a base64-encoded 32-byte key. It takes precedence.
	EnvKey = "PEEPWHO_INDEX_KEY"
	// EnvPassphrase holds a passphrase whose key is derived per file.
	EnvPassphrase = "PEEPWHO_INDEX_PASSPHRASE"
)

// FromEnv returns an encrypting codec configured from the environment, or a nil
// codec when neither variable is set, meaning the index stays plain JSON. A raw
// key wins over a passphrase when both are present.
func FromEnv() (vault.Codec, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvKey)); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("keyring: %s must be base64: %w", EnvKey, err)
		}
		cipher, err := vault.NewKeyCipher(key)
		if err != nil {
			return nil, fmt.Errorf("keyring: %s: %w", EnvKey, err)
		}
		return cipher, nil
	}
	// Trimmed, because the prompt trims what somebody types. A passphrase
	// exported with a stray leading or trailing space could be set from the
	// environment and then never entered by hand, which locks the files behind
	// whitespace nobody can see. The two ways of supplying it have to agree.
	if pass := strings.TrimSpace(os.Getenv(EnvPassphrase)); pass != "" {
		return vault.NewPassphraseCipher([]byte(pass)), nil
	}
	return nil, nil
}

// Source names where the configured key comes from, for status reporting.
func Source() string {
	switch {
	case strings.TrimSpace(os.Getenv(EnvKey)) != "":
		return EnvKey
	case os.Getenv(EnvPassphrase) != "":
		return EnvPassphrase
	default:
		return ""
	}
}

// GenerateKey returns a fresh random key encoded for PEEPWHO_INDEX_KEY.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("keyring: generate: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}
