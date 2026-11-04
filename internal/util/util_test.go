package util

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestWriteFileAtomic covers fresh writes, overwrites that tighten
// permissions, and a missing parent directory.
func TestWriteFileAtomic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Data        string
		Perm        fs.FileMode
		PreExisting string // existing content at path; empty means absent
		MissingDir  bool   // target a path whose parent does not exist
		WantErr     bool
	}{{ // Test 0: Fresh file gets the content and permissions.
		Data: `{"a":1}`, Perm: 0o600,
	}, { // Test 1: Overwrite replaces content and tightens permissions.
		Data: "new", Perm: 0o600, PreExisting: "old",
	}, { // Test 2: A missing parent directory is an error, nothing written.
		Data: "x", Perm: 0o600, MissingDir: true, WantErr: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "out.json")
			if test.MissingDir {
				path = filepath.Join(dir, "absent", "out.json")
			}
			if test.PreExisting != "" {
				if err := os.WriteFile(path, []byte(test.PreExisting), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			err := WriteFileAtomic(path, []byte(test.Data), test.Perm)
			if (err != nil) != test.WantErr {
				t.Fatalf("err = %v, want error %t", err, test.WantErr)
			}
			if test.WantErr {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Errorf("stat after failed write = %v, want not exist", statErr)
				}
				return
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.Data, string(got)); diff != "" {
				t.Errorf("content mismatch (-want +got):\n%s", diff)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != test.Perm {
				t.Errorf("perm = %o, want %o", info.Mode().Perm(), test.Perm)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("directory holds %d entries, want only the target: %v", len(entries), entries)
			}
		})
	}
}

// TestTruncate verifies the cut respects the byte cap and never splits a rune,
// since indexed source text arrives in every language.
func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Max  int
		Want string
	}{{ // Test 0: Shorter than the cap.
		In: "billing", Max: 20, Want: "billing",
	}, { // Test 1: Exactly the cap.
		In: "billing", Max: 7, Want: "billing",
	}, { // Test 2: Cut on an ASCII boundary.
		In: "billing retries", Max: 7, Want: "billing",
	}, { // Test 3: A cut that would split a multi-byte rune drops it whole.
		In: "café", Max: 4, Want: "caf",
	}, { // Test 4: Emoji are not split.
		In: "ok 🎉", Max: 5, Want: "ok ",
	}, { // Test 5: A zero cap yields nothing.
		In: "billing", Max: 0, Want: "",
	}, { // Test 6: A negative cap yields nothing.
		In: "billing", Max: -1, Want: "",
	}, { // Test 7: Empty input.
		In: "", Max: 10, Want: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := Truncate(test.In, test.Max)
			if got != test.Want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", test.In, test.Max, got, test.Want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d) = %q, not valid UTF-8", test.In, test.Max, got)
			}
		})
	}
}

// TestWriteFileAtomicUnwritableDir verifies the error names the directory and
// the cause, not the phantom temp file that never existed.
func TestWriteFileAtomicUnwritableDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := WriteFileAtomic(filepath.Join(dir, "index.json"), []byte("x"), 0o600)
	if err == nil {
		t.Skip("directory is still writable (likely running as root); nothing to check")
	}
	if strings.Contains(err.Error(), ".tmp-") {
		t.Errorf("error names a phantom temp file: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error does not name the unwritable directory: %v", err)
	}
}

// TestGitHubNoreplyLogin verifies the login is recovered from both the modern
// numeric-prefixed noreply email and the older bare form, and that ordinary
// emails are left alone.
func TestGitHubNoreplyLogin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In     string
		Want   string
		WantOK bool
	}{
		{"34209028+veeceey@users.noreply.github.com", "veeceey", true},          // Test 0: modern form.
		{"veeceey@users.noreply.github.com", "veeceey", true},                   // Test 1: older bare form.
		{"1+dependabot[bot]@users.noreply.github.com", "dependabot[bot]", true}, // Test 2: prefixed bot.
		{"Jane@Users.NoReply.GitHub.Com", "jane", true},                         // Test 3: case-insensitive.
		{"jane@corp.com", "", false},                                            // Test 4: ordinary email.
		{"", "", false},                                                         // Test 5: empty.
		{"+@users.noreply.github.com", "", false},                               // Test 6: no login.
	}
	for testNum, test := range tests {
		got, ok := GitHubNoreplyLogin(test.In)
		if got != test.Want || ok != test.WantOK {
			t.Errorf("test %d: GitHubNoreplyLogin(%q) = (%q, %v), want (%q, %v)",
				testNum, test.In, got, ok, test.Want, test.WantOK)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	tests := []struct{ In, Want string }{
		{"Alice@X.COM", "alice@x.com"},
		{"  bob@x.com  ", "bob@x.com"},
		{"alice+ci@x.com", "alice@x.com"},
		{"alice+a+b@x.com", "alice@x.com"},
		{"first.last@corp.com", "first.last@corp.com"},
		{"notanemail", "notanemail"},
		{"", ""},
	}
	for _, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeEmail(test.In); got != test.Want {
				t.Errorf("NormalizeEmail(%q) = %q, want %q", test.In, got, test.Want)
			}
		})
	}
}

func TestIsRoleEmail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want bool
	}{
		{"support@x.com", true},
		{"NoReply@x.com", true},
		{"oncall@x.com", true},
		{"support+x@x.com", true},
		{"alice@x.com", false},
		{"first.last@corp.com", false},
		{"notanemail", false},
	}
	for _, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if got := IsRoleEmail(test.In); got != test.Want {
				t.Errorf("IsRoleEmail(%q) = %v, want %v", test.In, got, test.Want)
			}
		})
	}
}

// TestPersonKey checks which identifier a person is keyed by, since every
// source funnels through this and a wrong answer either splits one person in
// two or merges two people into one.
func TestPersonKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Source     string
		Email      string
		ID         string
		WantResult string
	}{{ // Test 0: An email wins, because it means the same in every source.
		Source: "jira", Email: "Kim.Doe@Example.com", ID: "acct-1",
		WantResult: "kim.doe@example.com",
	}, { // Test 1: No email falls back to the source's own id.
		Source: "jira", Email: "", ID: "acct-1", WantResult: "jira:acct-1",
	}, { // Test 2: The id is namespaced, so two systems reusing one id stay apart.
		Source: "pagerduty", Email: "", ID: "acct-1", WantResult: "pagerduty:acct-1",
	}, { // Test 3: Nothing to key on.
		Source: "confluence", Email: "", ID: "", WantResult: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := PersonKey(test.Source, test.Email, test.ID)
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDistinctKeepsFirstAndDropsEmptyKeys checks the two properties every
// caller depends on: the first appearance of a key wins, so an ordered merge
// can put preferred items in front and let the rest fall in behind, and an
// item with no key at all is dropped rather than treated as one more identity.
func TestDistinctKeepsFirstAndDropsEmptyKeys(t *testing.T) {
	t.Parallel()
	type row struct {
		ID   string
		Rank int
	}
	in := []row{{"a", 1}, {"b", 2}, {"a", 3}, {"", 4}, {"c", 5}, {"", 6}, {"b", 7}}
	want := []row{{"a", 1}, {"b", 2}, {"c", 5}}
	got := Distinct(in, func(r row) string { return r.ID })
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	if got := Distinct([]row(nil), func(r row) string { return r.ID }); len(got) != 0 {
		t.Errorf("Distinct(nil) = %v, want nothing", got)
	}
}

// TestReadsAsName checks the test that separates the name of a thing from a run
// of words that happened to sit together in a sentence.
//
// Both cases below are drawn from a real issue tracker read alongside its wiki.
// Mining text for phrases is what lets a question asked in somebody's own words
// find the right person, and it also produces "should have" and "during
// rebalance", which are not subjects any organization has.
func TestReadsAsName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In      string
		WantYes bool
		Why     string
	}{
		{"state-store", true, "two nouns naming a thing"},
		{"jwt-bearer", true, "a real term of art"},
		{"advertised-listeners", true, "adjective and noun, still a name"},
		{"should-have", false, "grammar, not a subject"},
		{"during-rebalance", false, "a preposition dragging a noun along"},
		{"per-record", false, "same, and it read as a subject for months"},
		{"kip-1076", false, "a ticket reference, not an area of work"},
		{"hive-3-2-0-must", false, "a version string with a verb stuck on it"},
		{"", false, "nothing at all"},
		{"-leading", false, "an empty part is not a word"},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.In), func(t *testing.T) {
			t.Parallel()
			if got := ReadsAsName(test.In); got != test.WantYes {
				t.Errorf("ReadsAsName(%q) = %v, want %v: %s",
					test.In, got, test.WantYes, test.Why)
			}
		})
	}
}

// TestNormalizeEmailKeepsAGitHubUsername covers the address that carries a
// person's identity behind the plus instead of in front of it. Folding it the
// usual way keeps the account number and discards the human, which is exactly
// the half no source of record ever writes down: a CODEOWNERS file says
// @tlrenard and the history only ever says 2481736+tlrenard@users.noreply.github.com.
func TestNormalizeEmailKeepsAGitHubUsername(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantResult string
	}{{ // Test 0: The account id goes, the username stays.
		In: "2481736+tlrenard@users.noreply.github.com", WantResult: "tlrenard@users.noreply.github.com",
	}, { // Test 1: Older addresses carry the username alone.
		In: "tlrenard@users.noreply.github.com", WantResult: "tlrenard@users.noreply.github.com",
	}, { // Test 2: Case and spacing still normalize.
		In: "  2481736+TlRenard@Users.NoReply.GitHub.com ", WantResult: "tlrenard@users.noreply.github.com",
	}, { // Test 3: An ordinary plus-tag still folds the other way, since
		// alice+ci@x.com and alice@x.com really are one mailbox.
		In: "alice+ci@x.com", WantResult: "alice@x.com",
	}, { // Test 4: A plus-tag on a domain that merely looks similar is not
		// GitHub's, so it folds normally.
		In: "2481736+tlrenard@noreply.github.com", WantResult: "2481736@noreply.github.com",
	}, { // Test 5: A trailing plus leaves nothing to take, so nothing changes.
		In: "12345+@users.noreply.github.com", WantResult: "12345+@users.noreply.github.com",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := NormalizeEmail(test.In); got != test.WantResult {
				t.Errorf("NormalizeEmail(%q) = %q, want %q", test.In, got, test.WantResult)
			}
		})
	}
}
