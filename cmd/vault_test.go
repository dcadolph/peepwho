package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/engine"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/keyring"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/state"
	"github.com/dcadolph/peepwho/internal/vault"
)

// writeOrgCSV writes a small org chart and returns its path.
func writeOrgCSV(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Jane Roe,jane@x.com,Staff Engineer,Billing,retries;idempotency\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return path
}

// TestEncryptedIndexRoundTrip indexes with a key set, confirms the file on disk
// is encrypted with no plaintext email, then reads it back through ask.
func TestEncryptedIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)

	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Fatal("index on disk is not encrypted")
	}
	if strings.Contains(string(raw), "jane@x.com") {
		t.Fatal("plaintext email found in encrypted index")
	}

	out, _, err := runCmd(t, "ask", "--data-dir", dir, "who owns billing retries")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(string(out), "jane@x.com") {
		t.Fatalf("ask did not decrypt the index:\n%s", out)
	}
}

// TestEncryptedIndexNeedsKey verifies a read without the key fails clearly and
// points at the key variables, rather than exposing anything.
func TestEncryptedIndexNeedsKey(t *testing.T) {
	dir := t.TempDir()
	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	t.Setenv(keyring.EnvKey, "")
	err := runCmdStdin(t, "ask", "--data-dir", dir, "who owns billing")
	if err == nil {
		t.Fatal("ask without key: want error")
		return
	}
	if !strings.Contains(err.Error(), keyring.EnvKey) {
		t.Errorf("error does not name %s: %v", keyring.EnvKey, err)
	}
}

// TestVaultStatusAndKeygen exercises the status and keygen subcommands.
func TestVaultStatusAndKeygen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")

	out, _, err := runCmd(t, "vault", "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(string(out), "no index yet") || !strings.Contains(string(out), "none") {
		t.Errorf("status without key or index:\n%s", out)
	}

	kout, _, err := runCmd(t, "vault", "keygen")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(kout)), "export "+keyring.EnvKey+"=") {
		t.Errorf("keygen output: %s", kout)
	}
}

// TestVaultEncryptDecrypt builds a plain index, encrypts it in place with a key,
// then decrypts it back and confirms it still answers.
func TestVaultEncryptDecrypt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}
	idx := filepath.Join(dir, "index.json")
	if raw, _ := os.ReadFile(idx); vault.IsEncrypted(raw) {
		t.Fatal("index unexpectedly encrypted with no key")
	}

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	if _, _, err := runCmd(t, "vault", "encrypt", "--data-dir", dir); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if raw, _ := os.ReadFile(idx); !vault.IsEncrypted(raw) {
		t.Fatal("index not encrypted after vault encrypt")
	}

	if _, _, err := runCmd(t, "vault", "decrypt", "--data-dir", dir); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if raw, _ := os.ReadFile(idx); vault.IsEncrypted(raw) {
		t.Fatal("index still encrypted after vault decrypt")
	}
	out, _, err := runCmd(t, "ask", "--data-dir", dir, "who owns retries")
	if err != nil {
		t.Fatalf("ask after decrypt: %v", err)
	}
	if !strings.Contains(string(out), "jane@x.com") {
		t.Fatalf("ask after decrypt:\n%s", out)
	}
}

// TestVaultCoversConversations verifies encrypt, decrypt, and status all cover
// the conversation store and not only the index. That file holds retained
// message text, so a key that sealed the index alone would leave the more
// sensitive of the two readable while status called the install encrypted.
func TestVaultCoversConversations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	const secret = "we rotated the signing key by hand"
	store := episode.New()
	store.Add(episode.Episode{
		ID: "slack:C1:1", Source: "slack", Kind: episode.KindThread, Place: "billing",
		Participants: []model.ID{"jane@x.com"},
		Occurred:     time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Archive:      []episode.Note{{Author: "jane@x.com", Text: secret}},
	})
	eps := filepath.Join(dir, "episodes.json")
	if err := store.Save(eps); err != nil {
		t.Fatalf("save conversations: %v", err)
	}
	if raw, _ := os.ReadFile(eps); !bytes.Contains(raw, []byte(secret)) {
		t.Fatal("setup: the conversation text should start readable on disk")
	}

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	if _, _, err := runCmd(t, "vault", "encrypt", "--data-dir", dir); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	raw, err := os.ReadFile(eps)
	if err != nil {
		t.Fatalf("read conversations: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Error("conversation store not encrypted after vault encrypt")
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Error("retained message text still readable on disk after vault encrypt")
	}

	out, _, err := runCmd(t, "vault", "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Count(string(out), "encrypted") < 2 {
		t.Errorf("status does not report both files as encrypted:\n%s", out)
	}

	if _, _, err := runCmd(t, "vault", "decrypt", "--data-dir", dir); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if raw, _ := os.ReadFile(eps); !bytes.Contains(raw, []byte(secret)) {
		t.Error("conversation store not readable again after vault decrypt")
	}
}

// TestVaultCoversFactsAndFeedback verifies encrypt, decrypt, and status cover
// the fact and feedback stores. A fact's detail states in plain words what the
// index encryption exists to hide, and feedback is sealed on its normal writes,
// so an encrypt that skipped the first left secrets plaintext beside the
// ciphertext index, and a decrypt that skipped the second stranded the votes
// sealed under a key the user was about to unset.
func TestVaultCoversFactsAndFeedback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	const detail = "Jane holds the prod signing keys"
	if _, _, err := runCmd(t, "fact", "record", "--data-dir", dir,
		"team:billing", "escalates_to", "person:jane@x.com", "--detail", detail); err != nil {
		t.Fatalf("fact record: %v", err)
	}
	if _, _, err := runCmd(t, "feedback", "record", "--data-dir", dir,
		"who owns retries", "--person", "jane@x.com", "--helpful"); err != nil {
		t.Fatalf("feedback record: %v", err)
	}
	factsPath := filepath.Join(dir, "facts.json")
	fbPath := filepath.Join(dir, "feedback.json")

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	if _, _, err := runCmd(t, "vault", "encrypt", "--data-dir", dir); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	for name, path := range map[string]string{"facts": factsPath, "feedback": fbPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !vault.IsEncrypted(raw) {
			t.Errorf("%s not encrypted after vault encrypt", name)
		}
	}
	if raw, _ := os.ReadFile(factsPath); bytes.Contains(raw, []byte(detail)) {
		t.Error("fact detail still readable on disk after vault encrypt")
	}

	out, _, err := runCmd(t, "vault", "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, label := range []string{"index:", "facts:", "feedback:"} {
		line := ""
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, label) {
				line = l
			}
		}
		if line == "" {
			t.Errorf("status has no %q line:\n%s", label, out)
			continue
		}
		if !strings.Contains(line, "encrypted") {
			t.Errorf("status does not call %q encrypted: %q", label, line)
		}
	}

	if _, _, err := runCmd(t, "vault", "decrypt", "--data-dir", dir); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if raw, _ := os.ReadFile(factsPath); !bytes.Contains(raw, []byte(detail)) {
		t.Error("facts not readable again after vault decrypt")
	}
	if raw, _ := os.ReadFile(fbPath); vault.IsEncrypted(raw) {
		t.Error("feedback still sealed after vault decrypt")
	}
}

// TestVaultCoversChangeLog verifies encrypt seals the change log, status
// lists it, and decrypt opens it so a later index with no key still appends.
func TestVaultCoversChangeLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	indexCSV := func(rows string) error {
		if err := os.WriteFile(csv, []byte("name,email,title,team,topics\n"+rows), 0o600); err != nil {
			t.Fatalf("write csv: %v", err)
		}
		_, stderr, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv)
		if err != nil {
			return fmt.Errorf("%w: %s", err, stderr)
		}
		return nil
	}
	if err := indexCSV("Jane Roe,jane@x.com,Eng,Billing,retries\n"); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if err := indexCSV("Jane Roe,jane@x.com,Eng,Billing,retries\nAl Poe,al@x.com,Eng,Billing,ledger\n"); err != nil {
		t.Fatalf("second index: %v", err)
	}
	logPath := filepath.Join(dir, "changes.json")
	if raw, err := os.ReadFile(logPath); err != nil || !bytes.Contains(raw, []byte("Al Poe")) {
		t.Fatalf("change log missing the joiner before encrypt (err %v): %s", err, raw)
	}

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	if _, _, err := runCmd(t, "vault", "encrypt", "--data-dir", dir); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if raw, _ := os.ReadFile(logPath); !vault.IsEncrypted(raw) || bytes.Contains(raw, []byte("Al Poe")) {
		t.Error("change log not sealed after vault encrypt")
	}
	out, _, err := runCmd(t, "vault", "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !regexp.MustCompile(`(?m)^changes:\s+encrypted`).Match(out) {
		t.Errorf("status does not call the change log encrypted:\n%s", out)
	}

	if _, _, err := runCmd(t, "vault", "decrypt", "--data-dir", dir); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	t.Setenv(keyring.EnvKey, "")
	if err := indexCSV("Jane Roe,jane@x.com,Eng,Billing,retries\n"); err != nil {
		t.Fatalf("index with no key after decrypt: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read change log: %v", err)
	}
	var entries []engine.ChangeEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("change log not plain JSON after decrypt: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("change log holds %d entries, want 2: %s", len(entries), raw)
	}
}

// TestFactWritesSealedWithKey verifies a fact recorded while a key is set
// lands encrypted, matching the vault promise that every write is encrypted.
// It then reads the store back through fact list to prove the codec round
// trips.
func TestFactWritesSealedWithKey(t *testing.T) {
	dir := t.TempDir()
	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	t.Setenv(keyring.EnvPassphrase, "")

	const detail = "Jane holds the prod keys"
	if _, _, err := runCmd(t, "fact", "record", "--data-dir", dir,
		"team:pay", "escalates_to", "person:jane@corp.com", "--detail", detail); err != nil {
		t.Fatalf("fact record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "facts.json"))
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	if !vault.IsEncrypted(raw) {
		t.Fatal("facts.json is plaintext while a key is configured")
	}
	if bytes.Contains(raw, []byte(detail)) {
		t.Fatal("fact detail readable on the stolen-laptop threat model the vault help names")
	}
	out, _, err := runCmd(t, "fact", "list", "--data-dir", dir, "--json")
	if err != nil {
		t.Fatalf("fact list: %v", err)
	}
	if !strings.Contains(string(out), "jane@corp.com") {
		t.Fatalf("fact list cannot read the sealed store back:\n%s", out)
	}
}

// TestDoctorReportsFileEncryptionState builds a plain index, sets a key
// afterward without rewriting anything, and verifies doctor reports the file
// as still plaintext instead of asserting it is encrypted because a key is
// configured.
func TestDoctorReportsFileEncryptionState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(keyring.EnvKey, "")
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	out, _, err := runCmd(t, "doctor", "--data-dir", dir)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "index file is encrypted at rest") {
		t.Errorf("doctor calls a plaintext file encrypted:\n%s", out)
	}
	if !strings.Contains(string(out), "still plaintext") {
		t.Errorf("doctor does not flag the key-set-but-file-plain gap:\n%s", out)
	}
	if !strings.Contains(string(out), "peepwho vault encrypt") {
		t.Errorf("doctor gives no fix for the plaintext file:\n%s", out)
	}
}

// TestKeyModeFileRefusesPassphrasePrompt verifies a key-sealed index read with
// no key configured points straight at PEEPWHO_INDEX_KEY instead of prompting
// for a passphrase that cannot work and then blaming the passphrase.
func TestKeyModeFileRefusesPassphrasePrompt(t *testing.T) {
	dir := t.TempDir()
	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	t.Setenv(keyring.EnvPassphrase, "")
	csv := writeOrgCSV(t, dir)
	if _, _, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv); err != nil {
		t.Fatalf("index: %v", err)
	}

	t.Setenv(keyring.EnvKey, "")
	err := runCmdStdin(t, "ask", "--data-dir", dir, "who owns billing")
	if err == nil {
		t.Fatal("ask without key: want error")
		return
	}
	if !strings.Contains(err.Error(), "raw key") || !strings.Contains(err.Error(), keyring.EnvKey) {
		t.Errorf("error = %v, want it to say the file needs the raw key in %s", err, keyring.EnvKey)
	}
	if strings.Contains(err.Error(), keyring.EnvPassphrase) {
		t.Errorf("error = %v, want no pointer at a passphrase no passphrase can open this file", err)
	}
}

// TestVaultCoversRefreshAndWatermarks verifies encrypt seals the refresh config and the
// watermarks, status reports both, decrypt opens them, and a run with no key then replays.
func TestVaultCoversRefreshAndWatermarks(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(t.TempDir(), "people.csv")
	people := "name,email,title,team,topics\nJane Roe,jane@x.com,Eng,Billing,retries\n"
	if err := os.WriteFile(csv, []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "index", "--source", "org-csv", "--file", csv, "--data-dir", dir); err != nil {
		t.Fatalf("index: %v", err)
	}
	refreshPath := filepath.Join(dir, "refresh.json")
	statePath := filepath.Join(dir, "index.state.json")
	wm := state.New()
	wm.Set(state.Watermark{Source: "jira", Scope: "project:PLANTED", Cursor: time.Unix(1000, 0).UTC()})
	if err := wm.Save(statePath, nil); err != nil {
		t.Fatal(err)
	}

	key, _ := keyring.GenerateKey()
	t.Setenv(keyring.EnvKey, key)
	if _, _, err := runCmd(t, "vault", "encrypt", "--data-dir", dir); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if raw, _ := os.ReadFile(refreshPath); !vault.IsEncrypted(raw) || bytes.Contains(raw, []byte("people.csv")) {
		t.Errorf("refresh config not sealed after encrypt: %s", raw)
	}
	if raw, _ := os.ReadFile(statePath); !vault.IsEncrypted(raw) || bytes.Contains(raw, []byte("PLANTED")) {
		t.Errorf("watermarks not sealed after encrypt: %s", raw)
	}
	out, _, err := runCmd(t, "vault", "status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, re := range []string{`(?m)^refresh:\s+encrypted`, `(?m)^watermarks:\s+encrypted`} {
		if !regexp.MustCompile(re).Match(out) {
			t.Errorf("status does not match %s:\n%s", re, out)
		}
	}
	if _, _, err := runCmd(t, "refresh", "--data-dir", dir); err != nil {
		t.Fatalf("refresh under the key: %v", err)
	}

	if _, _, err := runCmd(t, "vault", "decrypt", "--data-dir", dir); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	t.Setenv(keyring.EnvKey, "")
	if raw, _ := os.ReadFile(refreshPath); vault.IsEncrypted(raw) || !bytes.Contains(raw, []byte("people.csv")) {
		t.Errorf("refresh config not plain after decrypt: %s", raw)
	}
	if raw, _ := os.ReadFile(statePath); vault.IsEncrypted(raw) || !bytes.Contains(raw, []byte("PLANTED")) {
		t.Errorf("watermarks not plain after decrypt: %s", raw)
	}
	if _, _, err := runCmd(t, "refresh", "--data-dir", dir); err != nil {
		t.Fatalf("refresh with no key after decrypt: %v", err)
	}
}
