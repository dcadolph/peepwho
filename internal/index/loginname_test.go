package index

import (
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
)

// TestGitHubLoginAsDisplayNameIsOnePerson covers a split the handle pass
// cannot reach. Somebody commits from a GitHub noreply address, which the
// build folds into their login straight away, and separately from an ordinary
// mailbox signing with that same login as their display name. Two full people
// remain, keyed under the same string, and before this rule nothing compared
// them: the work split in two and each half looked smaller than the person.
func TestGitHubLoginAsDisplayNameIsOnePerson(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		person("Real Name", "4242+octodev@users.noreply.github.com", "billing", "invoices"),
		person("octodev", "personal@example.com", "billing", "refunds"),
	})
	ix.AutoJoin()
	ix.Canonicalize()

	if got := len(ix.Graph.People); got != 1 {
		for id, p := range ix.Graph.People {
			t.Logf("person %s (%s)", id, p.Name)
		}
		t.Fatalf("people = %d, want 1: the login and the name are the same handle", got)
	}
}

// TestGitHubLoginJoinRefusesAmbiguity verifies the guard that keeps the rule
// honest. Two different people signing with the same single-word name is
// exactly the case where merging is unrecoverable, so the rule must decline
// rather than pick one, and a spaced full name must never be read as a login.
func TestGitHubLoginJoinRefusesAmbiguity(t *testing.T) {
	t.Parallel()
	t.Run("two candidates for one login", func(t *testing.T) {
		t.Parallel()
		ix := New()
		// Both names flatten to the login, since flattening drops the hyphen,
		// but they are spelled differently, so no same-name rule merges them
		// first and the ambiguity actually reaches this one.
		ix.Build([]connector.Record{
			person("Real Name", "4242+octodev@users.noreply.github.com", "billing"),
			person("octodev", "one@example.com", "billing"),
			person("octo-dev", "two@example.net", "shipping"),
		})
		ix.AutoJoin()
		ix.Canonicalize()
		for _, j := range ix.Joins() {
			if j.Reason == loginNameReason {
				t.Errorf("a login claimed by two different people was merged anyway: %+v", j)
			}
		}
	})
	t.Run("a spaced name is not a login", func(t *testing.T) {
		t.Parallel()
		ix := New()
		ix.Build([]connector.Record{
			person("Someone Else", "4242+someoneelse@users.noreply.github.com", "billing"),
			// Flattening removes the space, so without the guard this full
			// name would claim a login it merely resembles.
			person("Someone Else", "unrelated@example.com", "shipping"),
		})
		before := len(ix.Graph.People)
		ix.AutoJoin()
		ix.Canonicalize()
		if before < 2 {
			t.Fatalf("fixture built %d people, want 2", before)
		}
		// The pair may still merge on the same-name rule, which is a different
		// rule with its own evidence; what must not happen is the login rule
		// claiming it, so assert on the reason rather than the count.
		for _, j := range ix.Joins() {
			if j.Reason == loginNameReason {
				t.Errorf("a spaced full name was read as a GitHub login: %+v", j)
			}
		}
	})
}
