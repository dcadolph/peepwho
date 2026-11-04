package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestDiagnose(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	healthy := doctorFacts{
		IndexPath: "/d/index.json", IndexLoaded: true, People: 100,
		BuiltAt: now.Add(-time.Hour), Now: now, Embeddings: true,
		IndexSources: []string{"slack"}, Configured: map[string]bool{"slack": true, "github": false},
	}
	tests := []struct {
		Facts     doctorFacts
		WantName  string
		WantLevel checkLevel
		WantFails int
	}{{ // Test 0: No index is a hard failure with a build fix.
		Facts: doctorFacts{
			IndexPath: "/d/index.json", IndexLoaded: false, IndexErr: fs.ErrNotExist, Now: now,
			Configured: map[string]bool{"slack": false},
		},
		WantName: "index", WantLevel: levelFail, WantFails: 1,
	}, { // Test 1: A healthy index passes with no failures.
		Facts: healthy, WantName: "freshness", WantLevel: levelOK, WantFails: 0,
	}, { // Test 2: A stale index warns for a refresh.
		Facts:    func() doctorFacts { f := healthy; f.BuiltAt = now.Add(-60 * 24 * time.Hour); return f }(),
		WantName: "freshness", WantLevel: levelWarn, WantFails: 0,
	}, { // Test 3: An empty index warns.
		Facts:    func() doctorFacts { f := healthy; f.People = 0; return f }(),
		WantName: "content", WantLevel: levelWarn, WantFails: 0,
	}, { // Test 4: A source in the index whose credentials went missing warns.
		Facts: func() doctorFacts {
			f := healthy
			f.IndexSources = []string{"github"}
			f.Configured = map[string]bool{"github": false}
			return f
		}(),
		WantName: "source:github", WantLevel: levelWarn, WantFails: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			findings := diagnose(test.Facts)
			fails := 0
			var found *finding
			for i := range findings {
				if findings[i].Level == levelFail {
					fails++
				}
				if findings[i].Name == test.WantName {
					found = &findings[i]
				}
			}
			if fails != test.WantFails {
				t.Errorf("fails = %d, want %d", fails, test.WantFails)
			}
			if found == nil {
				t.Fatalf("no finding named %q in %+v", test.WantName, findings)
				return
			}
			if found.Level != test.WantLevel {
				t.Errorf("%q level = %d, want %d", test.WantName, found.Level, test.WantLevel)
			}
		})
	}
}

// TestDiagnoseTellsMissingFromBroken checks the two ways an index fails to load
// read differently. A first run has no index yet, which is expected and needs a
// build; an index that exists but cannot be parsed is a real fault, and telling
// somebody to build one would be the wrong advice.
func TestDiagnoseTellsMissingFromBroken(t *testing.T) {
	t.Parallel()
	base := doctorFacts{
		IndexPath: "/d/index.json", IndexLoaded: false, Now: time.Now(),
		Configured: map[string]bool{"slack": false},
	}
	find := func(f doctorFacts) string {
		for _, got := range diagnose(f) {
			if got.Name == "index" {
				return got.Detail
			}
		}
		t.Fatal("no index finding")
		return ""
	}

	missing := base
	missing.IndexErr = fmt.Errorf("index: open: %w", fs.ErrNotExist)
	if got := find(missing); !strings.Contains(got, "no index at /d/index.json yet") {
		t.Errorf("missing index detail = %q, want it to say there is not one yet", got)
	}

	broken := base
	broken.IndexErr = fmt.Errorf("index: open: %w", errors.New("unexpected end of JSON input"))
	got := find(broken)
	if !strings.Contains(got, "unexpected end of JSON input") {
		t.Errorf("broken index detail = %q, want the actual cause", got)
	}
	// The wrapping the error collected on its way here is noise to the reader.
	if strings.Contains(got, "index: open:") {
		t.Errorf("broken index detail = %q, want the cause without its wrapping", got)
	}
}

// TestEncryptionFinding checks doctor reports the file's own state, never key
// configuration. A key set after the last write leaves the file plaintext, and
// the old check called that "encrypted at rest": the exact false assurance a
// stolen-laptop threat model cannot afford.
func TestEncryptionFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantDetail    string
		WantFix       string
		WantLevel     checkLevel
		KeyConfigured bool
		OnDisk        bool
	}{{ // Test 0: No key, plain file: nothing to fix, stated plainly.
		WantDetail: "index file is not encrypted at rest", WantLevel: levelOK,
	}, { // Test 1: Sealed file reports encrypted whatever the key state.
		KeyConfigured: true, OnDisk: true,
		WantDetail: "index file is encrypted at rest", WantLevel: levelOK,
	}, { // Test 2: Key configured over a plaintext file warns with the fix,
		// because the operator believes the disk is protected and it is not.
		KeyConfigured: true, OnDisk: false,
		WantDetail: "an encryption key is configured but the index file on disk is still plaintext",
		WantLevel:  levelWarn, WantFix: "peepwho vault encrypt",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := encryptionFinding(doctorFacts{
				KeyConfigured: test.KeyConfigured, EncryptedOnDisk: test.OnDisk,
			})
			if got.Detail != test.WantDetail {
				t.Errorf("detail = %q, want %q", got.Detail, test.WantDetail)
			}
			if got.Level != test.WantLevel {
				t.Errorf("level = %v, want %v", got.Level, test.WantLevel)
			}
			if got.Fix != test.WantFix {
				t.Errorf("fix = %q, want %q", got.Fix, test.WantFix)
			}
		})
	}
}

// TestOwnershipFinding checks the gap between declared owners and recorded work
// is reported as something to fix rather than left to look like turnover. Those
// owners look inactive everywhere they appear, so every area they own reads as
// drifted, and the usual cause is identities that were never joined.
func TestOwnershipFinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name      string
		Declared  int
		Unlinked  int
		WantFound bool
		WantLevel checkLevel
	}{{ // Test 0: No declared ownership at all, so there is nothing to say.
		Name: "nothing declared", Declared: 0, Unlinked: 0, WantFound: false,
	}, { // Test 1: Every owner accounted for.
		Name: "all linked", Declared: 40, Unlinked: 0, WantFound: false,
	}, { // Test 2: A few unlinked is ordinary turnover, worth stating, not fixing.
		Name: "a few unlinked", Declared: 40, Unlinked: 4,
		WantFound: true, WantLevel: levelOK,
	}, { // Test 3: Most of them unlinked is a join that did not happen.
		Name: "most unlinked", Declared: 746, Unlinked: 457,
		WantFound: true, WantLevel: levelWarn,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			got, ok := ownershipFinding(doctorFacts{
				DeclaredOwners: test.Declared, UnlinkedOwners: test.Unlinked,
			})
			if ok != test.WantFound {
				t.Fatalf("reported = %v, want %v", ok, test.WantFound)
			}
			if !ok {
				return
			}
			if got.Level != test.WantLevel {
				t.Errorf("level = %v, want %v (%s)", got.Level, test.WantLevel, got.Detail)
			}
			if test.WantLevel == levelWarn && got.Fix == "" {
				t.Error("a warning with no fix leaves the reader nothing to do")
			}
		})
	}
}
