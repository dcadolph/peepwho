package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// mcpReply is one tools/call response line.
type mcpReply struct {
	// ID is the request id the reply answers.
	ID int `json:"id"`
	// Result is the tool's payload.
	Result struct {
		// Content holds the text the tool returned.
		Content []struct {
			// Text is the tool's text output or error message.
			Text string `json:"text"`
		} `json:"content"`
		// IsError is set when the tool returned an error.
		IsError bool `json:"isError"`
	} `json:"result"`
}

// TestMCPTools drives every MCP tool over stdio against the saved demo and
// checks each answer and each refusal.
func TestMCPTools(t *testing.T) {
	t.Parallel()
	dir := savedDemo(t)
	tests := []struct {
		Tool      string
		Args      string
		WantError bool
		WantText  string
	}{{ // Test 0: Keyword ask finds people.
		Tool: "peepwho_ask", Args: `{"question":"billing retries"}`, WantText: "people",
	}, { // Test 1: No llm mode over MCP.
		Tool: "peepwho_ask", Args: `{"question":"billing retries","mode":"llm"}`, WantError: true,
		WantText: "no llm mode",
	}, { // Test 2: An ask with no question is refused.
		Tool: "peepwho_ask", Args: `{}`, WantError: true, WantText: "needs a question",
	}, { // Test 3: A known person returns a profile.
		Tool: "peepwho_person", Args: fmt.Sprintf(`{"id":%q}`, demoPerson), WantText: demoPerson,
	}, { // Test 4: An unknown person is an error.
		Tool: "peepwho_person", Args: `{"id":"nobody@nowhere.test"}`, WantError: true,
		WantText: "unknown person",
	}, { // Test 5: A person call with no id is refused.
		Tool: "peepwho_person", Args: `{}`, WantError: true, WantText: "needs an id",
	}, { // Test 6: The people directory lists the demo person.
		Tool: "peepwho_directory", Args: `{"kind":"people"}`, WantText: demoPerson,
	}, { // Test 7: Channels list.
		Tool: "peepwho_directory", Args: `{"kind":"channels"}`, WantText: "[",
	}, { // Test 8: Teams list.
		Tool: "peepwho_directory", Args: `{"kind":"teams"}`, WantText: "[",
	}, { // Test 9: Topics list.
		Tool: "peepwho_directory", Args: `{"kind":"topics"}`, WantText: "[",
	}, { // Test 10: An unknown kind is an error.
		Tool: "peepwho_directory", Args: `{"kind":"pets"}`, WantError: true, WantText: "unknown kind",
	}, { // Test 11: Recall answers over the demo person's conversations.
		Tool: "peepwho_recall", Args: fmt.Sprintf(`{"question":"payment webhook retries","person":%q}`,
			demoPerson), WantText: "{",
	}, { // Test 12: Recall needs a person.
		Tool: "peepwho_recall", Args: `{"question":"payment webhook retries"}`, WantError: true,
		WantText: "needs a person",
	}, { // Test 13: Recall needs a question.
		Tool: "peepwho_recall", Args: `{}`, WantError: true, WantText: "needs a question",
	}}
	var in strings.Builder
	for testNum, test := range tests {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call",`+
			`"params":{"name":%q,"arguments":%s}}`+"\n", testNum, test.Tool, test.Args)
	}
	var out, errBuf bytes.Buffer
	root := newRootCmd()
	root.SetIn(strings.NewReader(in.String()))
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"mcp", "--data-dir", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("mcp: %v\n%s", err, errBuf.String())
	}
	got := make(map[int]mcpReply)
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var r mcpReply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode reply: %v\n%s", err, sc.Bytes())
		}
		got[r.ID] = r
	}
	for testNum, test := range tests {
		r, ok := got[testNum]
		if !ok || len(r.Result.Content) == 0 {
			t.Errorf("test %d: no reply for %s", testNum, test.Tool)
			continue
		}
		if r.Result.IsError != test.WantError {
			t.Errorf("test %d: %s isError = %t, want %t: %s", testNum, test.Tool, r.Result.IsError,
				test.WantError, r.Result.Content[0].Text)
		}
		if !strings.Contains(r.Result.Content[0].Text, test.WantText) {
			t.Errorf("test %d: %s text lacks %q: %.200s", testNum, test.Tool, test.WantText,
				r.Result.Content[0].Text)
		}
	}
}
