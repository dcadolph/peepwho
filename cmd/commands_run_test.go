package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cmdStep is one command in a sequence run against a shared data directory.
type cmdStep struct {
	// Args are the command line, without --data-dir.
	Args []string
	// Stdin is fed to the command.
	Stdin string
	// Want is the error the command must return, nil for success.
	Want error
	// WantFail marks a step that must fail without a sentinel to match.
	WantFail bool
	// WantOut must appear in stdout.
	WantOut string
	// WantErrOut must appear in stderr.
	WantErrOut string
}

// runCmdIn executes the root command with args and a scripted stdin.
func runCmdIn(t *testing.T, stdin string, args ...string) (stdout, stderr []byte, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := newRootCmd()
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.Bytes(), errBuf.Bytes(), err
}

// runSteps runs every step in order against dir and checks each outcome.
func runSteps(t *testing.T, dir string, steps []cmdStep) {
	t.Helper()
	for stepNum, step := range steps {
		args := append(append([]string(nil), step.Args...), "--data-dir", dir)
		out, errOut, err := runCmdIn(t, step.Stdin, args...)
		switch {
		case step.Want != nil:
			if !errors.Is(err, step.Want) {
				t.Errorf("step %d %v: err = %v, want %v", stepNum, step.Args, err, step.Want)
			}
		case step.WantFail:
			if err == nil {
				t.Errorf("step %d %v: want an error, got nil", stepNum, step.Args)
			}
		case err != nil:
			t.Errorf("step %d %v: %v\n%s", stepNum, step.Args, err, errOut)
		}
		if !bytes.Contains(out, []byte(step.WantOut)) {
			t.Errorf("step %d %v: stdout lacks %q:\n%s", stepNum, step.Args, step.WantOut, out)
		}
		if !bytes.Contains(errOut, []byte(step.WantErrOut)) {
			t.Errorf("step %d %v: stderr lacks %q:\n%s", stepNum, step.Args, step.WantErrOut, errOut)
		}
	}
}

// TestFactFlow records, lists, imports, and forgets facts.
func TestFactFlow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "facts.json")
	body := `[{"subject":"kafka","relation":"owned_by","object":"bob@x.com"}]`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write facts: %v", err)
	}
	runSteps(t, dir, []cmdStep{{ // Test 0: Record one fact.
		Args: []string{"fact", "record", "billing", "owned_by", "jane@x.com", "--detail", "on call lead"},
	}, { // Test 1: The human list shows it.
		Args: []string{"fact", "list", "--human"}, WantOut: "billing",
	}, { // Test 2: A filtered JSON list shows it.
		Args:    []string{"fact", "list", "billing", "--relation", "owned_by", "--json"},
		WantOut: `"jane@x.com"`,
	}, { // Test 3: Import a file under a source label.
		Args: []string{"fact", "import", file, "--source", "hr"},
	}, { // Test 4: Import from stdin.
		Args:  []string{"fact", "import", "--source", "wiki"},
		Stdin: `[{"subject":"search","relation":"owned_by","object":"amy@x.com"}]`,
	}, { // Test 5: The source filter finds the imported fact.
		Args: []string{"fact", "list", "--source", "hr", "--json"}, WantOut: "kafka",
	}, { // Test 6: Malformed input is refused.
		Args: []string{"fact", "import", "--source", "bad"}, Stdin: "not json", WantFail: true,
	}, { // Test 7: A bare forget is refused.
		Args: []string{"fact", "forget"}, Want: ErrBadArgs,
	}, { // Test 8: Forget by subject and relation.
		Args: []string{"fact", "forget", "billing", "owned_by"}, WantErrOut: "forgot 1 fact(s)",
	}, { // Test 9: Forget a whole source.
		Args: []string{"fact", "forget", "--source", "hr"}, WantErrOut: "forgot 1 fact(s)",
	}, { // Test 10: Only the stdin import is left.
		Args: []string{"fact", "list", "--json"}, WantOut: "search",
	}})
}

// TestFeedbackFlow records, lists, summarizes, bundles, and clears votes on
// the saved demo.
func TestFeedbackFlow(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	bundle := filepath.Join(t.TempDir(), "bundle.json")
	runSteps(t, dir, []cmdStep{{ // Test 0: Exactly one of the vote flags is required.
		Args: []string{"feedback", "record", "billing retries"}, Want: ErrBadArgs,
	}, { // Test 1: A helpful vote.
		Args:       []string{"feedback", "record", "billing retries", "--helpful", "--person", demoPerson},
		WantErrOut: "feedback recorded",
	}, { // Test 2: An unhelpful vote with a note.
		Args: []string{"feedback", "record", "kafka upgrade", "--not-helpful", "--person", demoPerson,
			"--comment", "wrong team"},
	}, { // Test 3: The list holds both.
		Args: []string{"feedback", "list"}, WantOut: "kafka upgrade",
	}, { // Test 4: The human summary renders.
		Args: []string{"feedback", "summary", "--human"},
	}, { // Test 5: The JSON summary renders.
		Args: []string{"feedback", "summary", "--json"}, WantOut: "{",
	}, { // Test 6: The bundle is written where asked.
		Args: []string{"feedback", "bundle", "-o", bundle},
	}, { // Test 7: A clear with no filter is refused.
		Args: []string{"feedback", "clear"}, Want: ErrBadArgs,
	}, { // Test 8: Clear by question.
		Args: []string{"feedback", "clear", "--query", "kafka upgrade"}, WantErrOut: "cleared 1 votes",
	}, { // Test 9: Clear the rest.
		Args: []string{"feedback", "clear", "--all"}, WantErrOut: "cleared 1 votes",
	}})
	if _, err := os.Stat(bundle); err != nil {
		t.Errorf("feedback bundle wrote nothing: %v", err)
	}
}

// TestReadCommandsOnDemo runs the read-only commands over the saved demo in
// both output forms.
func TestReadCommandsOnDemo(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	html := filepath.Join(t.TempDir(), "risk.html")
	runSteps(t, dir, []cmdStep{{ // Test 0: Identity overview.
		Args: []string{"identity", "--human"},
	}, { // Test 1: One person's identities.
		Args: []string{"identity", demoPerson, "--json"}, WantOut: `"merges"`,
	}, { // Test 2: The unlinked owners worklist.
		Args: []string{"identity", "--unlinked", "--human"},
	}, { // Test 3: Who works nearest a person.
		Args: []string{"near", demoPerson, "--limit", "3", "--json"}, WantOut: "[",
	}, { // Test 4: The human near view.
		Args: []string{"near", demoPerson, "--human"},
	}, { // Test 5: An unknown person is refused.
		Args: []string{"near", "nobody@nowhere.test"}, WantFail: true,
	}, { // Test 6: Search in both forms.
		Args: []string{"search", "billing", "--human"},
	}, { // Test 7: Search as JSON.
		Args: []string{"search", "billing", "--json"}, WantOut: "billing",
	}, { // Test 8: The risk brief as HTML.
		Args: []string{"risk", "--html", html},
	}, { // Test 9: Archive status in both forms.
		Args: []string{"archive", "status", "--human"},
	}, { // Test 10: Archive status as JSON.
		Args: []string{"archive", "status", "--json"}, WantOut: "{",
	}, { // Test 11: Recall over the demo person's conversations.
		Args: []string{"recall", "payment webhook retries", "--me", demoPerson, "--human"},
	}, { // Test 12: Recall as JSON.
		Args: []string{"recall", "duplicating charges", "--me", demoPerson, "--json"}, WantOut: "{",
	}, { // Test 13: The attest key prints its id.
		Args: []string{"attest", "key", "--human"},
	}, { // Test 14: The attest key as JSON.
		Args: []string{"attest", "key", "--json"}, WantOut: "{",
	}})
	if b, err := os.ReadFile(html); err != nil || !bytes.Contains(b, []byte("<html")) {
		t.Errorf("risk --html wrote no HTML: %v", err)
	}
}

// TestArchivePrune drops retained content and old conversations from the
// saved demo, and refuses a prune that names nothing.
func TestArchivePrune(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	runSteps(t, dir, []cmdStep{{ // Test 0: A prune with no criterion is refused.
		Args: []string{"archive", "prune"}, WantFail: true,
	}, { // Test 1: Drop content, keep links.
		Args: []string{"archive", "prune", "--content-only"}, WantErrOut: "keeping their links",
	}, { // Test 2: Forget everything older than a day.
		Args: []string{"archive", "prune", "--older-than-days", "1"}, WantErrOut: "older than",
	}})
}

// TestVerifyHuman seals a finding from the saved demo and verifies it in the
// human form, then names an altered file.
func TestVerifyHuman(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	finding := filepath.Join(t.TempDir(), "finding")
	if _, stderr, err := runCmd(t, "attest", "--data-dir", dir, "--out", finding); err != nil {
		t.Fatalf("attest: %v\n%s", err, stderr)
	}
	out, _, err := runCmd(t, "verify", finding, "--data-dir", dir, "--human")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("verified: ")) {
		t.Errorf("verify of an untouched finding does not say verified:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(finding, "risk.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("alter: %v", err)
	}
	out, _, err = runCmd(t, "verify", finding, "--data-dir", dir, "--human")
	if err == nil {
		t.Fatal("verify of an altered finding: want error, got nil")
	}
	if !bytes.Contains(out, []byte("risk.json")) {
		t.Errorf("verify does not name the altered file:\n%s", out)
	}
}

// TestForgetRefusals verifies forget refuses an unknown person and namesakes,
// and leaves everything alone when the prompt is declined.
func TestForgetRefusals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	csv := filepath.Join(dir, "people.csv")
	content := "name,email,title,team,topics\n" +
		"Jane Roe,jane@x.com,Engineer,Billing,retries\n" +
		"Jane Roe,jroe@y.com,Engineer,Search,ranking\n" +
		"Bob Lee,bob@x.com,SRE,Infra,kafka\n"
	if err := os.WriteFile(csv, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	_, stderr, err := runCmd(t, "index", "--data-dir", dir, "--source", "org-csv", "--file", csv)
	if err != nil {
		t.Fatalf("index: %v\n%s", err, stderr)
	}
	runSteps(t, dir, []cmdStep{{ // Test 0: Nobody matches.
		Args: []string{"forget", "nobody@nowhere.test", "--yes"}, Want: ErrBadArgs,
	}, { // Test 1: Two people share the name, so the purge names neither.
		Args: []string{"forget", "Jane Roe", "--yes"}, Want: ErrBadArgs,
	}, { // Test 2: Declining the prompt keeps everything.
		Args: []string{"forget", "bob@x.com"}, Stdin: "n\n", WantErrOut: "left everything as it was",
	}, { // Test 3: Bob is still indexed.
		Args: []string{"near", "bob@x.com", "--json"}, WantOut: "[",
	}, { // Test 4: Confirming the prompt purges him.
		Args: []string{"forget", "bob@x.com"}, Stdin: "y\n", WantErrOut: "forgot Bob Lee",
	}, { // Test 5: He is gone.
		Args: []string{"near", "bob@x.com"}, WantFail: true,
	}})
	_, _, err = runCmd(t, "forget", "jane@x.com", "--data-dir", t.TempDir())
	if !errors.Is(err, ErrNoIndex) {
		t.Errorf("forget with no index: err = %v, want ErrNoIndex", err)
	}
}
