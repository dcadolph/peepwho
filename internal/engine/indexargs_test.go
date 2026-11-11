package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/pflag"
)

// TestAbsPathFlags verifies each relative path flag comes back absolute against
// the working directory, in the value it is bound to and in the replay list,
// and that an empty value, an absolute path, and the stdin marker of --file stay
// as given.
func TestAbsPathFlags(t *testing.T) { //nolint:funlen // Test function.
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := AbsPathFlags(pflag.NewFlagSet("bare", pflag.ContinueOnError)); err != nil {
		t.Errorf("a flag set without path flags: %v", err)
	}
	sep := string(filepath.Separator)
	people, billing := filepath.Join(wd, "people.csv"), filepath.Join(wd, "billing")
	aliases, dash := filepath.Join(wd, "aliases.json"), filepath.Join(wd, "-")
	unclean := wd + sep + "." + sep + "people.csv"
	tests := []struct {
		WantFile    string
		WantAliases string
		WantChanges string
		WantRepos   []string
		WantReplay  []string
		Args        []string
	}{{ // Test 0: No path flag set leaves every value empty and replays nothing.
		Args: []string{"--merge"},
	}, { // Test 1: A relative file becomes absolute.
		Args:     []string{"--file", "people.csv"},
		WantFile: people, WantReplay: []string{"--file=" + people},
	}, { // Test 2: A relative file is cleaned on the way.
		Args:     []string{"--file=data" + sep + ".." + sep + "people.csv"},
		WantFile: people, WantReplay: []string{"--file=" + people},
	}, { // Test 3: An absolute file stays byte for byte, even an unclean one.
		Args:     []string{"--file", unclean},
		WantFile: unclean, WantReplay: []string{"--file=" + unclean},
	}, { // Test 4: The stdin marker of --file stays.
		Args:     []string{"--file", "-"},
		WantFile: "-", WantReplay: []string{"--file=-"},
	}, { // Test 5: Repeated repository paths keep their order, relative or not.
		Args:       []string{"--repo-path", "billing", "--repo-path", unclean, "--repo-path", "."},
		WantRepos:  []string{billing, unclean, wd},
		WantReplay: []string{"--repo-path", billing, "--repo-path", unclean, "--repo-path", wd},
	}, { // Test 6: Repository paths that are all absolute stay as given.
		Args:       []string{"--repo-path", unclean, "--repo-path", wd},
		WantRepos:  []string{unclean, wd},
		WantReplay: []string{"--repo-path", unclean, "--repo-path", wd},
	}, { // Test 7: The alias and changes files become absolute. Only the first replays.
		Args:        []string{"--aliases", "aliases.json", "--changes-file", "changes.json"},
		WantAliases: aliases, WantChanges: filepath.Join(wd, "changes.json"),
		WantReplay: []string{"--aliases=" + aliases},
	}, { // Test 8: A dash is a path for every flag but --file.
		Args:        []string{"--aliases", "-", "--repo-path", "-"},
		WantAliases: dash, WantRepos: []string{dash},
		WantReplay: []string{"--aliases=" + dash, "--repo-path", dash},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var run IndexRun
			fs := indexFlagSet(&run)
			if err := fs.Parse(test.Args); err != nil {
				t.Fatalf("parse %q: %v", test.Args, err)
			}
			if err := AbsPathFlags(fs); err != nil {
				t.Fatalf("AbsPathFlags: %v", err)
			}
			args := run.Request.IndexArgs
			if args.File != test.WantFile {
				t.Errorf("file = %q, want %q", args.File, test.WantFile)
			}
			if args.AliasesFile != test.WantAliases {
				t.Errorf("aliases = %q, want %q", args.AliasesFile, test.WantAliases)
			}
			if args.ChangesFile != test.WantChanges {
				t.Errorf("changes file = %q, want %q", args.ChangesFile, test.WantChanges)
			}
			if diff := cmp.Diff(test.WantRepos, args.RepoPaths, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("repository paths mismatch (-want +got):\n%s", diff)
			}
			replay := ReplayFlags(fs)
			if diff := cmp.Diff(test.WantReplay, replay, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("replay list mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// loseWorkingDirectory makes dir, the working directory, unreadable to the
// process and reports whether the working directory no longer resolves. Closing
// the directory to its owner does it for an ordinary user. Where the process
// ignores permissions, removing the directory does.
func loseWorkingDirectory(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err != nil {
		return true
	}
	if err := os.Remove(dir); err != nil {
		return false
	}
	_, err := os.Getwd()
	return err != nil
}

// TestAbsPathFlagsLostWorkingDirectory verifies a relative path is refused,
// naming its flag, when the working directory cannot be read, and that a value
// needing no working directory still passes. It changes directory, so neither it
// nor its subtests run in parallel.
func TestAbsPathFlagsLostWorkingDirectory(t *testing.T) {
	keep := t.TempDir()
	lost := filepath.Join(keep, "lost")
	if err := os.Mkdir(lost, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(lost)
	if !loseWorkingDirectory(t, lost) {
		t.Skip("the working directory still resolves here")
	}
	tests := []struct {
		WantMsg string
		Args    []string
	}{{ // Test 0: A relative file is refused.
		Args: []string{"--file", "people.csv"}, WantMsg: "resolve --file: ",
	}, { // Test 1: One relative repository path among absolute ones is refused.
		Args:    []string{"--repo-path", keep, "--repo-path", "billing"},
		WantMsg: "resolve --repo-path: ",
	}, { // Test 2: A relative alias file is refused.
		Args: []string{"--aliases", "aliases.json"}, WantMsg: "resolve --aliases: ",
	}, { // Test 3: A relative changes file is refused.
		Args: []string{"--changes-file", "changes.json"}, WantMsg: "resolve --changes-file: ",
	}, { // Test 4: Absolute paths and the stdin marker need no working directory.
		Args: []string{"--file", "-", "--aliases", keep, "--repo-path", keep},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			var run IndexRun
			fs := indexFlagSet(&run)
			if err := fs.Parse(test.Args); err != nil {
				t.Fatalf("parse %q: %v", test.Args, err)
			}
			err := AbsPathFlags(fs)
			if test.WantMsg == "" {
				if err != nil {
					t.Errorf("AbsPathFlags(%q) = %v, want no error", test.Args, err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), test.WantMsg) {
				t.Errorf("AbsPathFlags(%q) = %v, want prefix %q", test.Args, err, test.WantMsg)
			}
		})
	}
	_, err := IndexFromFlags("org-csv", []string{"--file", "people.csv"})
	if err == nil || !strings.HasPrefix(err.Error(), "flags: resolve --file: ") {
		t.Errorf("IndexFromFlags = %v, want an error starting %q", err, "flags: resolve --file: ")
	}
}
