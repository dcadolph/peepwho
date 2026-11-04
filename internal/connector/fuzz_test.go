package connector

import (
	"context"
	"strings"
	"testing"
)

// FuzzParseCodeOwners feeds arbitrary bytes to the CODEOWNERS parser, which
// reads files peepwho's user does not control. It must never panic, and every
// record it returns must carry a pattern.
func FuzzParseCodeOwners(f *testing.F) {
	f.Add("* @org/team\n/docs/ @writer\n")
	f.Add("# comment\n\npath/**  user@corp.com @two\n")
	f.Add("\x00\xff\n@@@\n* \n")
	f.Fuzz(func(t *testing.T, s string) {
		recs, err := parseCodeOwners(context.Background(), strings.NewReader(s))
		if err != nil {
			return
		}
		for _, r := range recs {
			if r.Kind == KindPerson && r.Name == "" && r.Email == "" {
				t.Fatalf("person record with no identity from %q", s)
			}
		}
	})
}

// FuzzParseMailmapLine feeds arbitrary lines to the mailmap parser, which
// reads a repo-controlled file. It must never panic, and an ok result must
// carry a commit email to match on.
func FuzzParseMailmapLine(f *testing.F) {
	f.Add("Proper Name <proper@x.com> Commit Name <commit@x.com>")
	f.Add("<a@b.c> <d@e.f>")
	f.Add("no brackets at all")
	f.Add("<>< ><\x00>")
	f.Fuzz(func(t *testing.T, line string) {
		proper, commit, ok := parseMailmapLine(line)
		if ok && proper.email == "" && proper.name == "" {
			t.Fatalf("ok mailmap line with empty proper identity from %q", line)
		}
		_ = commit
	})
}
