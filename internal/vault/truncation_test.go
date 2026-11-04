package vault

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// TestTruncatedFileIsRefused checks a sealed file cut short at each of the
// places it can be cut is refused rather than misread. A half-written index is
// what a machine that lost power leaves behind, and the failure has to be an
// error the caller can act on rather than a panic on a slice or, worse, a
// plausible-looking answer built from whatever bytes survived.
func TestTruncatedFileIsRefused(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{7}, keyLen)
	c, err := NewKeyCipher(key)
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	sealed, err := c.Encode([]byte("the whole conversation"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	version := versionOf(sealed)
	if version == "" {
		t.Fatal("the sealed file carries no version")
	}

	tests := []struct {
		Name string
		Keep int
	}{
		{"version only, nothing after it", len(version)},
		{"version and mode byte", len(version) + 1},
		{"part of the nonce", len(version) + 4},
		{"one byte short of the whole file", len(sealed) - 1},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			cut := sealed[:test.Keep]
			got, err := c.Decode(cut)
			if err == nil {
				t.Fatalf("a file cut to %d bytes decoded to %q", test.Keep, got)
				return
			}
			if got != nil {
				t.Errorf("decode returned %d bytes alongside its error, want none", len(got))
			}
		})
	}
}

// TestTruncatedPassphraseFileIsRefused covers the branch a passphrase file has
// and a key file does not: the salt, which is read before anything can be
// derived and is the first thing a cut file loses.
func TestTruncatedPassphraseFileIsRefused(t *testing.T) {
	t.Parallel()
	c := NewPassphraseCipher([]byte("open sesame please"))
	sealed, err := c.Encode([]byte("the whole conversation"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	version := versionOf(sealed)
	// Cut inside the salt, which follows the version and the mode byte.
	cut := sealed[:len(version)+1+saltLen-1]
	if _, err := c.Decode(cut); !errors.Is(err, ErrCorrupt) {
		t.Errorf("decode of a file cut inside its salt = %v, want ErrCorrupt", err)
	}
}

// TestWrongKeyIsRefused checks an intact file opened with the wrong key fails
// rather than returning whatever the cipher made of it.
func TestWrongKeyIsRefused(t *testing.T) {
	t.Parallel()
	sealer, err := NewKeyCipher(bytes.Repeat([]byte{1}, keyLen))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	sealed, err := sealer.Encode([]byte("secret"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	other, err := NewKeyCipher(bytes.Repeat([]byte{2}, keyLen))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	if got, err := other.Decode(sealed); err == nil {
		t.Errorf("the wrong key decoded the file to %q", got)
	}
}
