package jira

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestADFText verifies Jira's rich-text tree flattens to the words whotoask
// indexes, and that shapes it cannot read yield nothing rather than an error.
func TestADFText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantHas  []string
		WantText string
	}{{ // Test 0: Nothing at all.
		In: "", WantText: "",
	}, { // Test 1: Null description, which Jira sends for an empty field.
		In: `null`, WantText: "",
	}, { // Test 2: A plain string, which older sites and API v2 send.
		In:       `"retries exhausted on the billing queue"`,
		WantText: "retries exhausted on the billing queue",
	}, { // Test 3: A v3 document tree.
		In: `{"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"Raised the retry ceiling."}]},
			{"type":"paragraph","content":[{"type":"text","text":"Root cause was backoff."}]}]}`,
		WantHas: []string{"Raised the retry ceiling.", "Root cause was backoff."},
	}, { // Test 4: Nested marks, code blocks, and a mention carrying attr text.
		In: `{"type":"doc","content":[
			{"type":"paragraph","content":[
				{"type":"text","text":"fixed by"},
				{"type":"mention","attrs":{"text":"@Jane Roe"}}]},
			{"type":"codeBlock","content":[{"type":"text","text":"max_retries=10"}]}]}`,
		WantHas: []string{"fixed by", "@Jane Roe", "max_retries=10"},
	}, { // Test 5: An empty document.
		In: `{"type":"doc","content":[]}`, WantText: "",
	}, { // Test 6: A shape that is neither string nor node tree.
		In: `[1,2,3]`, WantText: "",
	}, { // Test 7: Malformed JSON must not panic or error.
		In: `{"type":`, WantText: "",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := adfText(json.RawMessage(test.In))
			if test.WantText != "" && got != test.WantText {
				t.Errorf("adfText = %q, want %q", got, test.WantText)
			}
			if test.WantText == "" && len(test.WantHas) == 0 && got != "" {
				t.Errorf("adfText = %q, want empty", got)
			}
			for _, want := range test.WantHas {
				if !strings.Contains(got, want) {
					t.Errorf("adfText = %q, missing %q", got, want)
				}
			}
		})
	}
}

// TestADFTextCaps verifies a runaway description is cut, so a pasted log dump
// cannot drown an index.
func TestADFTextCaps(t *testing.T) {
	t.Parallel()
	huge, err := json.Marshal(strings.Repeat("stacktrace ", 5000))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := adfText(huge); len(got) > maxADFText {
		t.Errorf("adfText returned %d bytes, want at most %d", len(got), maxADFText)
	}
}

// TestDescriptionOnIssue verifies the accessor reads what Search decodes.
func TestDescriptionOnIssue(t *testing.T) {
	t.Parallel()
	var is Issue
	body := `{"key":"OPS-1","fields":{"summary":"Retry storm","description":` +
		`{"type":"doc","content":[{"type":"paragraph","content":[` +
		`{"type":"text","text":"raised the ceiling"}]}]}}}`
	if err := json.Unmarshal([]byte(body), &is); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := is.Description(); got != "raised the ceiling" {
		t.Errorf("Description = %q", got)
	}
}
