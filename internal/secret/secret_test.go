package secret

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// errNoEntry is the fake keychain's not-found error.
var errNoEntry = errors.New("not found")

// fakeKeychain is an in-memory Keychain for tests.
type fakeKeychain struct {
	// data holds stored secrets by name.
	data map[string]string
	// failGet, when set, is returned by Get instead of a lookup, standing in for
	// a locked or unavailable keychain.
	failGet error
}

// Get returns the stored value, or errNoEntry when absent.
func (f *fakeKeychain) Get(name string) (string, error) {
	if f.failGet != nil {
		return "", f.failGet
	}
	v, ok := f.data[name]
	if !ok {
		return "", errNoEntry
	}
	return v, nil
}

// Set stores value under name.
func (f *fakeKeychain) Set(name, value string) error {
	f.data[name] = value
	return nil
}

// Delete removes name.
func (f *fakeKeychain) Delete(name string) error {
	delete(f.data, name)
	return nil
}

// TestResolve verifies the environment wins over the keychain, the keychain is
// the fallback, and a missing or failing lookup resolves to empty.
func TestResolve(t *testing.T) { //nolint:tparallel // Subtests call t.Setenv.
	const name = "WHOTOASK_TEST_TOKEN"
	tests := []struct {
		Keychain   map[string]string
		FailGet    error
		Env        string
		WantResult string
	}{{ // Test 0: Environment set wins even when the keychain also holds it.
		Env: "from-env", Keychain: map[string]string{name: "from-keychain"}, WantResult: "from-env",
	}, { // Test 1: Keychain used when the environment is empty.
		Env: "", Keychain: map[string]string{name: "from-keychain"}, WantResult: "from-keychain",
	}, { // Test 2: Neither set resolves to empty.
		Env: "", WantResult: "",
	}, { // Test 3: A keychain error is treated as absent.
		Env: "", FailGet: errors.New("locked"), WantResult: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Setenv(name, test.Env)
			data := test.Keychain
			if data == nil {
				data = map[string]string{}
			}
			s := NewWith(&fakeKeychain{data: data, failGet: test.FailGet})
			if diff := cmp.Diff(test.WantResult, s.Resolve(name)); diff != "" {
				t.Errorf("Resolve mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSource verifies Source names where a value would be read from.
func TestSource(t *testing.T) { //nolint:tparallel // Subtests call t.Setenv.
	const name = "WHOTOASK_TEST_TOKEN"
	tests := []struct {
		Env        string
		WantResult string
		InKeychain bool
	}{{ // Test 0: Environment wins.
		Env: "x", InKeychain: true, WantResult: "env",
	}, { // Test 1: Keychain when the environment is empty.
		Env: "", InKeychain: true, WantResult: "keychain",
	}, { // Test 2: Neither.
		Env: "", InKeychain: false, WantResult: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Setenv(name, test.Env)
			data := map[string]string{}
			if test.InKeychain {
				data[name] = "v"
			}
			s := NewWith(&fakeKeychain{data: data})
			if diff := cmp.Diff(test.WantResult, s.Source(name)); diff != "" {
				t.Errorf("Source mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSaveWritesToKeychain verifies Save stores a value the keychain then
// resolves when the environment is empty.
func TestSaveWritesToKeychain(t *testing.T) {
	const name = "WHOTOASK_TEST_TOKEN"
	t.Setenv(name, "")
	kc := &fakeKeychain{data: map[string]string{}}
	s := NewWith(kc)
	if err := s.Save(name, "stored"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := kc.data[name]; got != "stored" {
		t.Errorf("keychain[%s] = %q, want %q", name, got, "stored")
	}
	if got := s.Resolve(name); got != "stored" {
		t.Errorf("Resolve after Save = %q, want %q", got, "stored")
	}
}

// TestNewWithNilPanics verifies a nil keychain is a wiring error.
func TestNewWithNilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("NewWith(nil) did not panic")
		}
	}()
	NewWith(nil)
}
