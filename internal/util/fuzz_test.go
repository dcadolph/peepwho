package util

import (
	"strings"
	"testing"
)

// FuzzGitHubNoreplyLogin feeds arbitrary strings to the noreply parser, which
// reads attacker-controllable commit emails. It must never panic, and a
// reported login must be non-empty and part of the input.
func FuzzGitHubNoreplyLogin(f *testing.F) {
	f.Add("123+login@users.noreply.github.com")
	f.Add("login@users.noreply.github.com")
	f.Add("+@users.noreply.github.com")
	f.Add("\x00+\xff@USERS.NOREPLY.GITHUB.COM")
	f.Fuzz(func(t *testing.T, email string) {
		login, ok := GitHubNoreplyLogin(email)
		if !ok {
			return
		}
		if login == "" {
			t.Fatalf("ok with empty login from %q", email)
		}
		if !strings.Contains(strings.ToLower(email), login) {
			t.Fatalf("login %q not part of %q", login, email)
		}
	})
}
