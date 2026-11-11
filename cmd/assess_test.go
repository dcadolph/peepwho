package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/policy"
)

// assessPolicyLine is the line an assessment prints when the policy keeps an
// export's private channels out.
const assessPolicyLine = "assess: org policy denies private channels"

// TestAssessProducesSealedDeliverable runs assess over the Slack export
// fixture and an org CSV, then checks every file of the deliverable exists,
// parses, and the seal verifies offline.
func TestAssessProducesSealedDeliverable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "assessment")
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Alice Nguyen,alice@corp.com,Staff Engineer,Payments,kafka;billing\n" +
		"Bob Okafor,bob@corp.com,SRE,Infra,terraform\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	_, stderr, err := runCmd(t, "assess", "--data-dir", dir,
		"--org-csv", csv, "--slack-export", exportZip, "--out", out)
	if err != nil {
		t.Fatalf("assess: %v\n%s", err, stderr)
	}
	if !strings.Contains(string(stderr), "subjects scored") {
		t.Errorf("no summary printed:\n%s", stderr)
	}

	for _, name := range []string{
		"summary.md", "report.html", "findings.json", "ownership.json", "departures.json",
		"crossings.json", "joined-work.json", "manifest.json", "manifest.sig", "README.txt",
	} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("deliverable lacks %s: %v", name, err)
		}
	}

	var findings []map[string]any
	data, err := os.ReadFile(filepath.Join(out, "findings.json"))
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	if err := json.Unmarshal(data, &findings); err != nil {
		t.Fatalf("findings.json does not parse: %v", err)
	}
	if len(findings) == 0 {
		t.Error("no topics scored from two sources of real shape")
	}

	summary, err := os.ReadFile(filepath.Join(out, "summary.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	for _, want := range []string{"# Knowledge continuity summary", "subjects scored"} {
		if !strings.Contains(string(summary), want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(string(summary), "%!") {
		t.Errorf("summary has a formatting error:\n%s", summary)
	}

	html, err := os.ReadFile(filepath.Join(out, "report.html"))
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(html), "<html") && !strings.Contains(string(html), "<!doctype") {
		t.Error("report.html does not look like a page")
	}

	// The seal must verify, offline, from the directory alone.
	verifyOut, _, err := runCmd(t, "verify", "--data-dir", dir, out)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(string(verifyOut), `"altered":[]`) {
		t.Errorf("verify output = %s", verifyOut)
	}
}

// TestAssessRequiresInput verifies assess refuses to run with nothing named.
func TestAssessRequiresInput(t *testing.T) {
	t.Parallel()
	if _, _, err := runCmd(t, "assess", "--data-dir", t.TempDir()); err == nil {
		t.Fatal("assess with no inputs: want error, got nil")
	}
}

// TestAssessSlackExportFollowsPrivatePolicy verifies an assessment reads an
// export's private channels only while the policy allows them, and names the
// policy when it keeps them out.
func TestAssessSlackExportFollowsPrivatePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Pol            policy.Policy
		WantChannels   []string
		WantPolicyLine bool
	}{{ // Test 0: The default policy reads the private channel.
		Pol: policy.Default(), WantChannels: []string{"engineering", "general", "private-ops"},
	}, { // Test 1: A policy that denies private channels leaves it out and says why.
		Pol:          policy.Default().WithoutPrivateChannels(),
		WantChannels: []string{"engineering", "general"}, WantPolicyLine: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var log bytes.Buffer
			src, err := readAssessSources(context.Background(), &options{pol: test.Pol},
				&assessArgs{SlackExport: exportZip}, &log)
			if err != nil {
				t.Fatalf("read sources: %v", err)
			}
			var got []string
			for _, rec := range src.Index.SourceRecords("slack") {
				if rec.Kind == connector.KindChannel {
					got = append(got, rec.Name)
				}
			}
			sort.Strings(got)
			if diff := cmp.Diff(test.WantChannels, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("channels read mismatch (-want +got):\n%s", diff)
			}
			if got := strings.Contains(log.String(), assessPolicyLine); got != test.WantPolicyLine {
				t.Errorf("policy line printed = %v, want %v; log:\n%s",
					got, test.WantPolicyLine, log.String())
			}
		})
	}
}

// TestAssessUnderLockedDenyPolicy verifies the org policy file reaches an
// assessment. Under a locked policy that denies private channels the run
// succeeds, reads the export's public channels, leaves its private channel
// unread, and names the policy as the reason. It points the policy variable at
// a file, so it cannot run in parallel.
func TestAssessUnderLockedDenyPolicy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(policyEnvVar, writePolicy(t, t.TempDir(), "policy.json",
		`{"mode":"strict","locked":true,"private_channels":"deny"}`))
	_, stderr, err := runCmd(t, "assess", "--data-dir", dir,
		"--slack-export", exportZip, "--out", filepath.Join(dir, "assessment"))
	if err != nil {
		t.Fatalf("assess: %v\n%s", err, stderr)
	}
	logs := string(stderr)
	if strings.Contains(logs, "indexed #private-ops") {
		t.Errorf("assess read a private channel the policy denies; log:\n%s", logs)
	}
	for _, want := range []string{assessPolicyLine, "indexed #general", "#private-ops"} {
		if !strings.Contains(logs, want) {
			t.Errorf("assess log lacks %q; log:\n%s", want, logs)
		}
	}
}

// TestAssessSealCoversTheFilesItShipped is the test the deliverable rests on.
//
// A seal that digests a re-encoding of the report rather than the file written
// beside it verifies as a valid bundle while covering nothing the reader
// received: every file could be edited and the verifier still said VERIFIED.
// This checks the digests against the bytes on disk the way a verifier does,
// then edits a file and checks the match breaks.
func TestAssessSealCoversTheFilesItShipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "assessment")
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Alice Nguyen,alice@corp.com,Staff Engineer,Payments,kafka;billing\n" +
		"Bob Okafor,bob@corp.com,SRE,Infra,terraform\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, stderr, err := runCmd(t, "assess", "--data-dir", dir,
		"--org-csv", csv, "--out", out); err != nil {
		t.Fatalf("assess: %v\n%s", err, stderr)
	}

	sealed := sealedDigests(t, filepath.Join(out, "manifest.json"))
	if len(sealed) < 2 {
		t.Fatalf("the seal covers %d artifacts, want every file of the deliverable", len(sealed))
	}
	for name, digest := range sealed {
		if name == "" {
			t.Error("a file is sealed with no path, so nothing can be checked against it")
			continue
		}
		if got := fileDigest(t, filepath.Join(out, name)); got != digest {
			t.Errorf("%s hashes to %s but was sealed as %s: the seal does not cover "+
				"the file that shipped", name, got, digest)
		}
	}
	for _, want := range []string{"summary.md", "report.html", "findings.json"} {
		if _, ok := sealed[want]; !ok {
			t.Errorf("%s is not covered by the seal", want)
		}
	}

	// An edited deliverable must stop matching. This is the whole claim.
	edited := filepath.Join(out, "summary.md")
	body, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := os.WriteFile(edited, append(body, []byte("\nand one more thing\n")...), 0o644); err != nil {
		t.Fatalf("edit summary: %v", err)
	}
	if fileDigest(t, edited) == sealed["summary.md"] {
		t.Error("an edited summary still matches its sealed digest")
	}
}

// TestAssessSummaryNumbersReconcile checks that every count the executive
// summary leads with can be recovered by counting rows in the files beside it.
//
// Three different quantities were once all called "rests on one person" and the
// summary printed whichever came to hand: the severity level, the bus factor,
// and the number of people in the record. On one repository those are 2, 34,
// and 6, and the summary reported 2 while departures.json listed 6. A reader
// who checks and finds a number the report does not have has stopped reading
// the report.
func TestAssessSummaryNumbersReconcile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "assessment")
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Alice Nguyen,alice@corp.com,Staff Engineer,Payments,kafka;billing\n" +
		"Bob Okafor,bob@corp.com,SRE,Infra,terraform\n"
	if err := os.WriteFile(csv, []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if _, stderr, err := runCmd(t, "assess", "--data-dir", dir,
		"--org-csv", csv, "--slack-export", exportZip, "--out", out); err != nil {
		t.Fatalf("assess: %v\n%s", err, stderr)
	}

	var findings []struct {
		Level     string `json:"level"`
		BusFactor int    `json:"busFactor"`
		Experts   []struct {
			ID string `json:"id"`
		} `json:"experts"`
	}
	readAssessJSON(t, filepath.Join(out, "findings.json"), &findings)

	// Counted from the shipped file, then rendered through the same function
	// the summary uses, so the test pins the numbers rather than the prose.
	var c assessCounts
	for _, f := range findings {
		if len(f.Experts) == 1 {
			c.Sole++
		}
		switch f.BusFactor {
		case 1:
			c.BusOne++
		case 2:
			c.BusTwo++
		}
		switch f.Level {
		case "critical":
			c.Critical++
		case "elevated":
			c.Elevated++
		}
	}
	if c.Sole == 0 && c.BusOne == 0 {
		t.Fatal("the fixture found no concentration at all, so this proves nothing")
	}

	summary, err := os.ReadFile(filepath.Join(out, "summary.md"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	body := string(summary)
	want := strings.TrimSuffix(concentrationLine(c), "\n")
	if !strings.Contains(body, want) {
		t.Errorf("the summary's headline does not match findings.json.\nwant: %s\n\ngot:\n%s",
			want, body)
	}

	// The people named as sole holders must be the ones the departure file
	// names, or the section and the data behind it are telling two stories.
	var departures []struct {
		Sole []string `json:"sole"`
	}
	readAssessJSON(t, filepath.Join(out, "departures.json"), &departures)
	listed := 0
	for _, d := range departures {
		listed += len(d.Sole)
	}
	if listed > c.Sole {
		t.Errorf("departures.json lists %d sole subjects but only %d subjects have "+
			"one person in the record", listed, c.Sole)
	}
}

// readAssessJSON decodes one deliverable file.
func readAssessJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
}

// sealedDigests reads a manifest's files as a map of path to sha256.
func sealedDigests(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest does not parse: %v", err)
	}
	out := make(map[string]string)
	for _, f := range m.Files {
		out[f.Path] = f.SHA256
	}
	return out
}

// fileDigest hashes a file the way the seal verifier does.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
