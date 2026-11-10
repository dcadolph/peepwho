// Package secret resolves whotoask's connector credentials from the OS keychain,
// falling back to the environment. A credential is named by its environment
// variable, such as WHOTOASK_SLACK_TOKEN, so the same name addresses it whether it
// lives in the keychain or the environment, and existing env-based setups keep
// working unchanged. An explicit environment variable always wins, so a one-off
// run can override a stored value without touching the keychain.
package secret

import (
	"os"

	"github.com/zalando/go-keyring"
)

// service is the keychain service every whotoask credential is stored under, so
// they group together and never collide with another program's entries.
const service = "whotoask"

// Keychain is the OS secret store a Store reads and writes. The default is
// backed by the login keychain on macOS, the Credential Manager on Windows, and
// the Secret Service on Linux; tests supply a fake.
type Keychain interface {
	// Get returns the secret stored under name, or an error when it is absent.
	Get(name string) (string, error)
	// Set stores value under name.
	Set(name, value string) error
	// Delete removes name.
	Delete(name string) error
}

// Store resolves credentials from a keychain with an environment fallback.
type Store struct {
	// kc is the backing OS secret store.
	kc Keychain
}

// New returns a Store backed by the OS keychain.
func New() *Store {
	return &Store{kc: osKeychain{}}
}

// NewWith returns a Store backed by kc. It panics on a nil keychain, since that
// is a programming error at wiring time.
func NewWith(kc Keychain) *Store {
	if kc == nil {
		panic("secret.NewWith: Keychain required")
	}
	return &Store{kc: kc}
}

// Resolve returns the value for name: the environment variable when set,
// otherwise the keychain entry, otherwise the empty string. The environment
// wins so an explicit export overrides a stored credential for a single run. A
// keychain error, including a missing entry, is treated as absent, exactly as
// an unset environment variable would be.
func (s *Store) Resolve(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	v, err := s.kc.Get(name)
	if err != nil {
		return ""
	}
	return v
}

// Source reports where Resolve would read name from: "env", "keychain", or the
// empty string when neither holds it.
func (s *Store) Source(name string) string {
	if os.Getenv(name) != "" {
		return "env"
	}
	if _, err := s.kc.Get(name); err == nil {
		return "keychain"
	}
	return ""
}

// Stored reports whether the keychain holds an entry under name, ignoring the
// environment. A keychain error, including a missing entry, reads as absent.
func (s *Store) Stored(name string) bool {
	_, err := s.kc.Get(name)
	return err == nil
}

// Save stores value in the keychain under name, so a later run resolves it
// without an environment variable.
func (s *Store) Save(name, value string) error {
	return s.kc.Set(name, value)
}

// Delete removes name from the keychain.
func (s *Store) Delete(name string) error {
	return s.kc.Delete(name)
}

// osKeychain is the default Keychain, backed by the OS secret store through
// go-keyring. It stays pure Go, so cross-compiled builds keep CGO off.
type osKeychain struct{}

// Get returns the secret stored under name in the OS keychain.
func (osKeychain) Get(name string) (string, error) { return keyring.Get(service, name) }

// Set stores value under name in the OS keychain.
func (osKeychain) Set(name, value string) error { return keyring.Set(service, name, value) }

// Delete removes name from the OS keychain.
func (osKeychain) Delete(name string) error { return keyring.Delete(service, name) }

// defaultStore backs the package-level helpers with the OS keychain, so a
// credential read site can call secret.Resolve as a drop-in for os.Getenv.
var defaultStore = New() //nolint:gochecknoglobals // One process-wide keychain, mirrors os.Getenv.

// Resolve returns the value for name from the default keychain-backed store,
// falling back to the environment. It is the drop-in for os.Getenv at a
// credential read site.
func Resolve(name string) string { return defaultStore.Resolve(name) }

// Save stores value under name in the default keychain-backed store.
func Save(name, value string) error { return defaultStore.Save(name, value) }

// Source reports where Resolve reads name from via the default store: "env",
// "keychain", or the empty string.
func Source(name string) string { return defaultStore.Source(name) }

// Delete removes name from the default keychain-backed store. It cannot unset
// an environment variable; the caller says so when one is still set.
func Delete(name string) error { return defaultStore.Delete(name) }
