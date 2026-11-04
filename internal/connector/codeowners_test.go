package connector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestParseCodeOwners covers owner parsing, topic derivation, and identity.
func TestParseCodeOwners(t *testing.T) {
	t.Parallel()
	in := "# comment\n" +
		"/internal/billing/   @jane  @org/payments\n" +
		"*.go                 jane@x.com\n" +
		"*.tf                 @kim\n" +
		"/internal/infra/     @kim\n"

	recs, err := parseCodeOwners(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := make(map[string]Record)
	for _, r := range recs {
		byName[r.Name] = r
	}

	if jane := byName["@jane"]; jane.PersonID != "codeowners:jane" ||
		!slices.Contains(jane.Topics, "billing") {
		t.Errorf("@jane = %+v, want id codeowners:jane and topic billing", jane)
	}
	if team := byName["@org/payments"]; team.PersonID != "codeowners:org/payments" {
		t.Errorf("@org/payments id = %q", team.PersonID)
	}
	if email := byName["jane@x.com"]; email.Email != "jane@x.com" ||
		!slices.Contains(email.Topics, "golang") {
		t.Errorf("email owner = %+v, want jane@x.com with golang from *.go", email)
	}
	if kim := byName["@kim"]; !slices.Contains(kim.Topics, "infra") ||
		!slices.Contains(kim.Topics, "terraform") {
		t.Errorf("@kim topics = %v, want infra and terraform", kim.Topics)
	}
}

// TestParseCodeOwnersInlineComments verifies a trailing comment mints no
// owners: every "@" token after the "#" used to become a person owning the
// pattern, so "# ask @bob" handed @bob terraform. An escaped "\#" is not a
// comment, which is how a path containing one is written.
func TestParseCodeOwnersInlineComments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantOwners []string
	}{{ // Test 0: A spaced trailing comment full of handles and addresses.
		In:         "*.tf @alice # ask @bob or ops@corp.com",
		WantOwners: []string{"@alice"},
	}, { // Test 1: A comment with no space on either side of the hash.
		In:         "*.js @js-owner#deprecated @old-owner",
		WantOwners: []string{"@js-owner"},
	}, { // Test 2: A whole-line comment yields nothing.
		In:         "# only @ghost lives here",
		WantOwners: nil,
	}, { // Test 3: An escaped hash is part of the pattern, not a comment.
		In:         "/docs/\\#faq/ @kim",
		WantOwners: []string{"@kim"},
	}, { // Test 4: An escaped hash and a real comment on one line.
		In:         "/\\#tags/ @jane # reviewed by @nobody",
		WantOwners: []string{"@jane"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			recs, err := parseCodeOwners(context.Background(), strings.NewReader(test.In))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := make([]string, 0, len(recs))
			for _, r := range recs {
				got = append(got, r.Name)
			}
			if diff := cmp.Diff(test.WantOwners, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("owners mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCodeOwnersMissing verifies a directory with no CODEOWNERS is an error.
func TestCodeOwnersMissing(t *testing.T) {
	t.Parallel()
	if _, err := NewCodeOwners(t.TempDir()).Fetch(context.Background()); !errors.Is(err, ErrNoCodeOwners) {
		t.Fatalf("err = %v, want ErrNoCodeOwners", err)
	}
}

// TestParseCodeOwnersSkipsSections verifies section headers and non-owner tokens
// never become phantom owners.
func TestParseCodeOwnersSkipsSections(t *testing.T) {
	t.Parallel()
	in := "[Optional Reviewers]\n" +
		"^[Security]\n" +
		"*.go @jane\n" +
		"[Docs] @docs-team\n"

	recs, err := parseCodeOwners(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, r := range recs {
		if strings.Contains(r.Name, "]") || strings.Contains(r.PersonID, "]") {
			t.Errorf("phantom owner leaked from a section header: %+v", r)
		}
	}
	if len(recs) != 1 || recs[0].Name != "@jane" {
		t.Errorf("records = %+v, want only @jane", recs)
	}
}

// TestOwningOneFileIsNotHoldingAnArea draws the same line CODEOWNERS parsing
// that the git connector draws for commits: a file's own name counts as a word
// people can search, never as an area somebody holds. On prometheus/prometheus
// the entry /Makefile minted "make" and "makefile" as ownable areas, and the
// ownership report then said the build had drifted away from its owners, who
// owned one file at the repository root. A glob stays a statement, because
// owning *.tf across a tree really does say who holds terraform.
func TestOwningOneFileIsNotHoldingAnArea(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Pattern    string
		WantStated []string
		WantWords  []string
	}{{ // Test 0: A literal file at the root states nothing.
		Pattern: "/Makefile", WantStated: nil, WantWords: []string{"make", "makefile"},
	}, { // Test 1: A directory keeps stating its subject.
		Pattern: "/model/histogram", WantStated: []string{"histogram", "model"}, WantWords: nil,
	}, { // Test 2: A glob class still states its mapped subject.
		Pattern: "*.tf", WantStated: []string{"terraform"}, WantWords: nil,
	}, { // Test 3: A literal file inside a directory leaves its own name as
		// words. The directory here is "docs", which the stop list already
		// treats as scaffolding rather than a subject, so nothing is stated.
		Pattern: "/docs/architecture.md", WantStated: nil,
		WantWords: []string{"architecture", "markdown"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			stated, words := topicsFromPatterns([]string{test.Pattern})
			sort.Strings(stated)
			sort.Strings(words)
			if diff := cmp.Diff(test.WantStated, stated, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("stated mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantWords, words, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("words mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestHiddenSegmentsNameNoSubject pins the dot-directory rule: .github fused
// with directories genuinely named github on grafana, collecting CI config,
// auth docs, and provisioning code under one areas with twenty-two owners. A
// hidden segment is tooling, so it yields nothing, while its children and a
// real directory of the same name still do.
func TestHiddenSegmentsNameNoSubject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Path    string
		WantHas []string
		WantNot []string
	}{{ // Test 0: The hidden container yields nothing; its child names the area.
		Path: ".github/workflows/release.yml", WantHas: []string{"workflows"}, WantNot: []string{"github"},
	}, { // Test 1: A real directory named github still names its subject.
		Path: "pkg/connection/github/client.go", WantHas: []string{"github", "connection"}, WantNot: nil,
	}, { // Test 2: A hidden file at the root names nothing.
		Path: ".golangci.yml", WantHas: nil, WantNot: []string{"golangci"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := segmentNames(test.Path)
			for _, want := range test.WantHas {
				if !slices.Contains(got, want) {
					t.Errorf("segmentNames(%q) = %v, missing %q", test.Path, got, want)
				}
			}
			for _, not := range test.WantNot {
				if slices.Contains(got, not) {
					t.Errorf("segmentNames(%q) = %v, must not contain %q", test.Path, got, not)
				}
			}
		})
	}
}
