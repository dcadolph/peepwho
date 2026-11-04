package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// leakySecrets are the credentials planted in the leaky export. None of them
// may survive into any file peepwho writes or any answer it prints.
var leakySecrets = []string{
	"AKIAIOSFODNN7EXAMPLE",
	"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
	"xoxb-1111111111-2222222222-SeCrEtSeCrEt",
	"hunter2superpassword",
	"MIIEowIBAAKCAQEAleaky",
}

// writeLeakyExport builds a small Slack export directory whose messages carry
// the kinds of secrets people actually paste into chat, wrapped in enough
// prose that the surrounding conversation must survive scrubbing.
func writeLeakyExport(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("users.json", `[
	 {"id":"U1","name":"jo","profile":{"real_name":"Jo Vega","email":"jo@corp.com"}},
	 {"id":"U2","name":"sam","profile":{"real_name":"Sam Idris","email":"sam@corp.com"}}
	]`)
	write("channels.json", `[
	 {"id":"C1","name":"incidents","topic":{"value":"prod incidents"},"purpose":{"value":"firefighting"}}
	]`)
	write("incidents/2026-06-01.json", `[
	 {"type":"message","user":"U1","ts":"1780300100.000100",
	  "text":"the deploy is failing, key is `+leakySecrets[0]+` if anyone needs the bucket"},
	 {"type":"message","user":"U2","ts":"1780300200.000200",
	  "text":"use my github token `+leakySecrets[1]+` and password=`+leakySecrets[3]+` for the registry"},
	 {"type":"message","user":"U1","ts":"1780300300.000300",
	  "text":"also the slack bot token is `+leakySecrets[2]+` and here is the pem -----BEGIN RSA PRIVATE KEY-----\n`+leakySecrets[4]+`\n-----END RSA PRIVATE KEY-----"},
	 {"type":"message","user":"U2","ts":"1780300400.000400",
	  "text":"rotated everything, the kubernetes deploy pipeline is green again"}
	]`)
	return dir
}

// TestLeakyIngestStoresNoSecrets indexes an export full of pasted credentials
// and then reads every byte peepwho wrote and every answer it gives, looking
// for any of them. This is the privacy claim as a test: what lands in the
// index is who talked about the deploy, never the key that was pasted.
func TestLeakyIngestStoresNoSecrets(t *testing.T) {
	t.Parallel()
	export := writeLeakyExport(t)
	dataDir := t.TempDir()

	if _, stderr, err := runCmd(t, "index", "--data-dir", dataDir,
		"--source", "slack-export", "--file", export, "--episodes", "--since-days", "36500"); err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}

	// Every file peepwho wrote, byte by byte.
	err := filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range leakySecrets {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s contains the pasted secret %q", path, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// The conversation around the secrets must still be indexed: the person
	// who fixed the deploy is findable, or scrubbing ate the discussion.
	out, _, err := runCmd(t, "ask", "--data-dir", dataDir, "who knows about the kubernetes deploy pipeline")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	var ans struct {
		People []struct {
			Email string `json:"email"`
		} `json:"people"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(ans.People) == 0 {
		t.Error("scrubbing removed the whole conversation; nobody is findable for the deploy")
	}
	for _, secret := range leakySecrets {
		if strings.Contains(string(out), secret) {
			t.Errorf("ask output contains the pasted secret %q", secret)
		}
	}
}
