package keyring

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/dcadolph/whotoask/internal/vault"
)

// TestFromEnvKey verifies a base64 key produces a working codec and is reported
// as the source.
func TestFromEnvKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	t.Setenv(EnvKey, key)
	t.Setenv(EnvPassphrase, "")

	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c == nil {
		t.Fatal("nil codec with a key set")
		return
	}
	enc, err := c.Encode([]byte("data"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(enc)
	if err != nil || string(got) != "data" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if Source() != EnvKey {
		t.Errorf("Source = %q, want %q", Source(), EnvKey)
	}
}

// TestFromEnvBadKey verifies a non-base64 or wrong-length key is rejected.
func TestFromEnvBadKey(t *testing.T) {
	t.Setenv(EnvPassphrase, "")
	t.Setenv(EnvKey, "not-base64!!!")
	if _, err := FromEnv(); err == nil {
		t.Error("bad base64: want error")
	}
	t.Setenv(EnvKey, base64.StdEncoding.EncodeToString([]byte("too short")))
	if _, err := FromEnv(); err == nil {
		t.Error("wrong size: want error")
	}
}

// TestFromEnvPassphrase verifies a passphrase produces a codec and source.
func TestFromEnvPassphrase(t *testing.T) {
	t.Setenv(EnvKey, "")
	t.Setenv(EnvPassphrase, "hunter2")
	c, err := FromEnv()
	if err != nil || c == nil {
		t.Fatalf("FromEnv: codec=%v err=%v", c, err)
	}
	if Source() != EnvPassphrase {
		t.Errorf("Source = %q, want %q", Source(), EnvPassphrase)
	}
}

// TestFromEnvNone verifies no key yields a nil codec, meaning plain JSON.
func TestFromEnvNone(t *testing.T) {
	t.Setenv(EnvKey, "")
	t.Setenv(EnvPassphrase, "")
	c, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c != nil {
		t.Errorf("codec = %v, want nil with no key", c)
	}
	if Source() != "" {
		t.Errorf("Source = %q, want empty", Source())
	}
}

// TestSourceAgreesWithFromEnv verifies Source names a variable only when FromEnv
// builds a codec from it, so status never reports a key that encrypts nothing.
// The cases run in sequence because each one sets the environment.
func TestSourceAgreesWithFromEnv(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tests := []struct {
		Key        string
		Passphrase string
		WantSource string
		WantCodec  bool
	}{{ // Test 0: Nothing set.
		Key: "", Passphrase: "",
	}, { // Test 1: A passphrase of only spaces counts as unset.
		Passphrase: "   ",
	}, { // Test 2: A passphrase of tabs and a newline counts as unset.
		Passphrase: "\t \n",
	}, { // Test 3: A real passphrase is reported.
		Passphrase: "hunter2", WantSource: EnvPassphrase, WantCodec: true,
	}, { // Test 4: A padded passphrase is reported.
		Passphrase: "  hunter2  ", WantSource: EnvPassphrase, WantCodec: true,
	}, { // Test 5: A key of only spaces counts as unset.
		Key: "   ",
	}, { // Test 6: A blank key falls through to the passphrase.
		Key: "  ", Passphrase: "hunter2", WantSource: EnvPassphrase, WantCodec: true,
	}, { // Test 7: A key wins over a passphrase.
		Key: key, Passphrase: "hunter2", WantSource: EnvKey, WantCodec: true,
	}, { // Test 8: A key beside a blank passphrase is reported as the key.
		Key: key, Passphrase: "   ", WantSource: EnvKey, WantCodec: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Setenv(EnvKey, test.Key)
			t.Setenv(EnvPassphrase, test.Passphrase)
			c, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			if got := c != nil; got != test.WantCodec {
				t.Errorf("codec present = %v, want %v", got, test.WantCodec)
			}
			if got := Source(); got != test.WantSource {
				t.Errorf("Source = %q, want %q", got, test.WantSource)
			}
		})
	}
}

// TestGenerateKey verifies the generated key is a valid 32-byte cipher key.
func TestGenerateKey(t *testing.T) {
	t.Parallel()
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(k)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("key length = %d, want 32", len(raw))
	}
	if _, err := vault.NewKeyCipher(raw); err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
}
