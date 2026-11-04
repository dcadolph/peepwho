package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/prompt"
	"github.com/dcadolph/peepwho/internal/slack"
)

// TestConnectHelpMatchesKeychainBehavior pins the help text to what the
// command does. It offers the OS keychain, default yes, and --forget exists to
// undo it, so any copy claiming credentials are never written to disk is a
// lie the very next prompt exposes.
func TestConnectHelpMatchesKeychainBehavior(t *testing.T) {
	t.Parallel()
	long := newConnectCmd(&options{}).Long
	if strings.Contains(long, "never written to disk") {
		t.Error("connect help claims credentials are never written to disk while it offers the keychain")
	}
	if !strings.Contains(long, "keychain") {
		t.Error("connect help does not say where saved credentials go")
	}
}

// TestConnectSpecs verifies each source description is internally consistent: a
// credentialed source has a validator, a fix message, and complete fields; a
// no-credential source has neither validator nor fields.
func TestConnectSpecs(t *testing.T) {
	t.Parallel()
	valid := map[string]bool{
		"org-csv": true, "codeowners": true, "git": true, "slack": true,
		"github": true, "jira": true, "confluence": true, "pagerduty": true,
		"graph": true, "matters": true,
	}
	seen := make(map[string]bool)
	for _, s := range connectSpecs() {
		if s.id == "" || s.title == "" || s.summary == "" {
			t.Errorf("spec %q is missing an id, title, or summary", s.id)
		}
		if seen[s.id] {
			t.Errorf("duplicate source id %q", s.id)
		}
		seen[s.id] = true
		if !valid[s.id] {
			t.Errorf("source id %q is not an index source", s.id)
		}
		if len(s.creds) > 0 {
			if s.validate == nil {
				t.Errorf("%q has credentials but no validate func", s.id)
			}
			if s.authFix == "" {
				t.Errorf("%q has credentials but no authFix", s.id)
			}
			for _, c := range s.creds {
				if c.env == "" || c.label == "" {
					t.Errorf("%q has an incomplete credential field: %+v", s.id, c)
				}
			}
		} else if s.validate != nil {
			t.Errorf("%q has no credentials but a validate func", s.id)
		}
	}
	for _, id := range []string{"slack", "github", "jira", "confluence", "pagerduty"} {
		if !seen[id] {
			t.Errorf("missing credentialed source %q", id)
		}
	}
}

// TestConnectStatus verifies the non-interactive report lists every source and
// reflects whether its credentials are set, on stdout and without color.
func TestConnectStatus(t *testing.T) {
	for _, e := range []string{
		slackTokenEnv, githubTokenEnv, pagerdutyTokenEnv, jiraURLEnv, jiraEmailEnv,
		jiraTokenEnv, confluenceURLEnv, confluenceEmailEnv, confluenceTokenEnv,
	} {
		t.Setenv(e, "")
	}
	dir := t.TempDir()
	out, _, err := runCmd(t, "connect", "--status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	report := string(out)
	for _, id := range []string{
		"org-csv", "codeowners", "git", "slack", "github", "jira", "confluence", "pagerduty",
	} {
		if !strings.Contains(report, id) {
			t.Errorf("status missing source %q:\n%s", id, report)
		}
	}
	if !strings.Contains(report, "not configured") {
		t.Errorf("expected a not-configured source:\n%s", report)
	}
	if strings.Contains(report, "\033[") {
		t.Errorf("color leaked into piped status:\n%q", report)
	}

	t.Setenv(slackTokenEnv, "xoxb-test")
	out2, _, err := runCmd(t, "connect", "--status", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status after configuring slack: %v", err)
	}
	if !strings.Contains(string(out2), "configured") {
		t.Errorf("slack not reported configured:\n%s", out2)
	}

	// The global output flags apply here like everywhere else: piped output is
	// machine-readable, and --json guarantees it.
	jsonOut, _, err := runCmd(t, "connect", "--status", "--json", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var view connectStatusView
	if err := json.Unmarshal(jsonOut, &view); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, jsonOut)
	}
	if len(view.Sources) == 0 || view.Index == "" {
		t.Errorf("status --json view = %+v, want sources and the index path", view)
	}
	slackConfigured := false
	for _, s := range view.Sources {
		if s.ID == "slack" && s.Configured {
			slackConfigured = true
		}
	}
	if !slackConfigured {
		t.Errorf("status --json does not report slack configured: %+v", view.Sources)
	}

	human, _, err := runCmd(t, "connect", "--status", "--human", "--data-dir", dir)
	if err != nil {
		t.Fatalf("status --human: %v", err)
	}
	if json.Valid(human) {
		t.Errorf("status --human still emitted JSON:\n%s", human)
	}
}

// TestConnectNonInteractive verifies connect refuses to prompt without a
// terminal and points at the scriptable index command.
func TestConnectNonInteractive(t *testing.T) {
	t.Parallel()
	err := runCmdStdin(t, "connect")
	if !errors.Is(err, ErrBadArgs) {
		t.Fatalf("err = %v, want ErrBadArgs", err)
	}
}

// TestConnectUnknownSource verifies an unrecognized source is rejected before
// the terminal check, so it fails clearly even in a script.
func TestConnectUnknownSource(t *testing.T) {
	t.Parallel()
	err := runCmdStdin(t, "connect", "not-a-source")
	if !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v, want ErrUnknownSource", err)
	}
}

// TestExplainAuthError verifies validation errors map to a specific fix: an auth
// status or Slack's logical error points at the credential guidance, and a
// transport error points at the URL and network.
func TestExplainAuthError(t *testing.T) {
	t.Parallel()
	spec := sourceSpec{authFix: "recreate the token"}
	tests := []struct {
		Name         string
		Err          error
		WantContains string
	}{ // Test 0-5: the status and error mappings.
		{Name: "unauthorized", Err: &httputil.StatusError{Code: 401}, WantContains: "recreate the token"},
		{Name: "forbidden", Err: &httputil.StatusError{Code: 403}, WantContains: "recreate the token"},
		{Name: "notfound", Err: &httputil.StatusError{Code: 404}, WantContains: "404"},
		{Name: "teapot", Err: &httputil.StatusError{Code: 418}, WantContains: "418"},
		{Name: "slack", Err: slack.ErrAPI, WantContains: "recreate the token"},
		{Name: "transport", Err: errors.New("dial tcp: no route to host"), WantContains: "Could not reach"},
	}
	for testNum, test := range tests {
		got := explainAuthError(spec, test.Err)
		if !strings.Contains(got, test.WantContains) {
			t.Errorf("test %d (%s): got %q, want contains %q", testNum, test.Name, got, test.WantContains)
		}
	}
}

// TestCollect verifies collection reads a plain value and a secret without
// echoing the secret, and reuses a value already set in the environment.
func TestCollect(t *testing.T) {
	spec := sourceSpec{creds: []credField{
		{env: "PEEPWHO_TEST_URL", label: "URL"},
		{env: "PEEPWHO_TEST_TOKEN", label: "Token", secret: true},
	}}

	t.Setenv("PEEPWHO_TEST_URL", "")
	t.Setenv("PEEPWHO_TEST_TOKEN", "")
	var out bytes.Buffer
	ui := prompt.New(strings.NewReader("https://x\ns3cr3t\n"), &out, &out)
	creds, err := collect(ui, spec)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if creds["PEEPWHO_TEST_URL"] != "https://x" || creds["PEEPWHO_TEST_TOKEN"] != "s3cr3t" {
		t.Errorf("creds = %v", creds)
	}
	if strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("secret echoed into output: %q", out.String())
	}

	t.Setenv("PEEPWHO_TEST_URL", "https://preset")
	var out2 bytes.Buffer
	ui2 := prompt.New(strings.NewReader("y\nsecret2\n"), &out2, &out2)
	creds2, err := collect(ui2, spec)
	if err != nil {
		t.Fatalf("collect reuse: %v", err)
	}
	if creds2["PEEPWHO_TEST_URL"] != "https://preset" || creds2["PEEPWHO_TEST_TOKEN"] != "secret2" {
		t.Errorf("creds2 = %v", creds2)
	}
}

// runCmdStdin executes the root command with a scripted, non-terminal stdin,
// and returns the execution error.
func runCmdStdin(t *testing.T, args ...string) error {
	t.Helper()
	var out, errBuf bytes.Buffer
	root := newRootCmd()
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	return root.Execute()
}

// TestQuietAbortDistinguishesNetworkEOF verifies a clean exit only for a user
// backing out, not for a network error that unwraps to io.EOF. A server
// closing a connection mid-fetch during the first index once slipped through
// as success because both were io.EOF.
func TestQuietAbortDistinguishesNetworkEOF(t *testing.T) {
	t.Parallel()
	// A user ending input, or quitting the menu, is a clean exit.
	if err := quietAbort(prompt.ErrInputClosed); err != nil {
		t.Errorf("end of input should abort cleanly, got %v", err)
	}
	if err := quietAbort(prompt.ErrAborted); err != nil {
		t.Errorf("menu quit should abort cleanly, got %v", err)
	}
	// A network read that failed with io.EOF is a real failure and must survive.
	netErr := &url.Error{Op: "Post", URL: "https://x.example/api", Err: io.EOF}
	if err := quietAbort(fmt.Errorf("jira search: %w", netErr)); err == nil {
		t.Error("a network EOF was swallowed as a clean abort")
	}
	// Nil stays nil.
	if err := quietAbort(nil); err != nil {
		t.Errorf("nil became %v", err)
	}
}
