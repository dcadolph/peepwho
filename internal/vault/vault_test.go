package vault

import (
	"bytes"
	"errors"
	"testing"

	"golang.org/x/crypto/argon2"
)

// TestKeyRoundTrip verifies a key-mode seal decrypts to the original and hides
// the plaintext.
func TestKeyRoundTrip(t *testing.T) {
	t.Parallel()
	c, err := NewKeyCipher(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatalf("NewKeyCipher: %v", err)
	}
	plaintext := []byte(`{"hello":"world"}`)
	enc, err := c.Encode(plaintext)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !IsEncrypted(enc) {
		t.Fatal("Encode output is not marked encrypted")
	}
	if bytes.Contains(enc, plaintext) {
		t.Fatal("plaintext leaked into the ciphertext")
	}
	got, err := c.Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if diff := bytes.Compare(got, plaintext); diff != 0 {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

// TestPassphraseRoundTrip verifies passphrase mode round-trips and that a wrong
// passphrase fails authentication.
func TestPassphraseRoundTrip(t *testing.T) {
	t.Parallel()
	c := NewPassphraseCipher([]byte("correct horse battery staple"))
	plaintext := []byte("secret people graph")
	enc, err := c.Encode(plaintext)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !IsEncrypted(enc) {
		t.Fatal("not marked encrypted")
	}
	got, err := c.Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
	if _, err := NewPassphraseCipher([]byte("wrong")).Decode(enc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong passphrase = %v, want ErrCorrupt", err)
	}
}

// TestWrongKeyAndTamper verifies a wrong key and a flipped byte both fail.
func TestWrongKeyAndTamper(t *testing.T) {
	t.Parallel()
	c, _ := NewKeyCipher(bytes.Repeat([]byte{1}, 32))
	enc, _ := c.Encode([]byte("data"))

	other, _ := NewKeyCipher(bytes.Repeat([]byte{2}, 32))
	if _, err := other.Decode(enc); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong key = %v, want ErrCorrupt", err)
	}
	tampered := bytes.Clone(enc)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := c.Decode(tampered); !errors.Is(err, ErrCorrupt) {
		t.Errorf("tampered = %v, want ErrCorrupt", err)
	}
}

// TestKeyModeMismatch verifies a key cannot read a passphrase file and the
// reverse, each reported as ErrKeyMode.
func TestKeyModeMismatch(t *testing.T) {
	t.Parallel()
	key, _ := NewKeyCipher(bytes.Repeat([]byte{3}, 32))
	keyFile, _ := key.Encode([]byte("data"))
	if _, err := NewPassphraseCipher([]byte("p")).Decode(keyFile); !errors.Is(err, ErrKeyMode) {
		t.Errorf("passphrase on key file = %v, want ErrKeyMode", err)
	}
	passFile, _ := NewPassphraseCipher([]byte("p")).Encode([]byte("data"))
	if _, err := key.Decode(passFile); !errors.Is(err, ErrKeyMode) {
		t.Errorf("key on passphrase file = %v, want ErrKeyMode", err)
	}
}

// TestKeySize verifies the constructor rejects a key that is not 32 bytes.
func TestKeySize(t *testing.T) {
	t.Parallel()
	if _, err := NewKeyCipher([]byte("short")); !errors.Is(err, ErrKeySize) {
		t.Errorf("short key = %v, want ErrKeySize", err)
	}
}

// TestPlain verifies the identity codec passes bytes through and refuses to read
// an encrypted file.
func TestPlain(t *testing.T) {
	t.Parallel()
	plaintext := []byte(`{"a":1}`)
	if enc, err := (Plain{}).Encode(plaintext); err != nil || !bytes.Equal(enc, plaintext) {
		t.Fatalf("Plain.Encode = %q, %v", enc, err)
	}
	if got, err := (Plain{}).Decode(plaintext); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Plain.Decode = %q, %v", got, err)
	}
	if IsEncrypted(plaintext) {
		t.Error("plain JSON marked as encrypted")
	}
	c, _ := NewKeyCipher(bytes.Repeat([]byte{4}, 32))
	sealed, _ := c.Encode(plaintext)
	if _, err := (Plain{}).Decode(sealed); !errors.Is(err, ErrEncrypted) {
		t.Errorf("Plain on encrypted = %v, want ErrEncrypted", err)
	}
}

// TestCipherReadsPlaintext verifies a cipher returns a pre-encryption plain file
// unchanged, so enabling a key migrates an existing index on the next write.
func TestCipherReadsPlaintext(t *testing.T) {
	t.Parallel()
	c, _ := NewKeyCipher(bytes.Repeat([]byte{5}, 32))
	plaintext := []byte(`{"legacy":true}`)
	got, err := c.Decode(plaintext)
	if err != nil {
		t.Fatalf("Decode plaintext: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

// sealV1 builds a file exactly as the original format did, so the current code
// can be checked against something it did not write.
func sealV1(t *testing.T, passphrase, plaintext []byte) []byte {
	t.Helper()
	salt := make([]byte, saltLen)
	for i := range salt {
		salt[i] = byte(i)
	}
	key := argon2.IDKey(passphrase, salt, argonTimeV1, argonMemory, argonThreads, keyLen)
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatalf("newGCM: %v", err)
	}
	head := header(magicV1, modePass, salt)
	nonce := make([]byte, gcm.NonceSize())
	out := append([]byte(nil), head...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, head)
}

// TestReadsOlderFormat verifies a file sealed by the original format still
// opens. The passphrase parameters were strengthened, which changes the key a
// passphrase derives, so an index encrypted before the change would be
// unreadable if the version were not honored on the way in.
func TestReadsOlderFormat(t *testing.T) {
	t.Parallel()
	pass := []byte("correct horse battery staple")
	want := []byte(`{"graph":{"people":{}}}`)
	old := sealV1(t, pass, want)

	if !IsEncrypted(old) {
		t.Fatal("a file in the older format is not recognized as encrypted")
	}
	got, err := NewPassphraseCipher(pass).Decode(old)
	if err != nil {
		t.Fatalf("older format did not open: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

// TestWritesCurrentFormat verifies new files carry the current version, so the
// stronger parameters actually take effect rather than being defined and
// ignored.
func TestWritesCurrentFormat(t *testing.T) {
	t.Parallel()
	sealed, err := NewPassphraseCipher([]byte("pass")).Encode([]byte("plaintext"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := versionOf(sealed); got != magicV2 {
		t.Errorf("new file version = %q, want %q", got, magicV2)
	}
	if argonTime <= argonTimeV1 {
		t.Errorf("argonTime = %d, want more passes than the original %d", argonTime, argonTimeV1)
	}
}

// TestOlderFormatRejectsWrongPassphrase verifies the older path still fails
// closed rather than returning something on a bad passphrase.
func TestOlderFormatRejectsWrongPassphrase(t *testing.T) {
	t.Parallel()
	old := sealV1(t, []byte("right"), []byte("secret"))
	if _, err := NewPassphraseCipher([]byte("wrong")).Decode(old); err == nil {
		t.Fatal("a wrong passphrase opened a file in the older format")
	}
}
