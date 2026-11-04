package jira

import (
	"encoding/json"
	"strings"

	"github.com/dcadolph/peepwho/internal/util"
)

// maxADFText caps the plain text taken from one rich field. An issue
// description carries the explanation worth searching; a pasted log dump does
// not, and indexing it whole would drown the words that matter.
const maxADFText = 8000

// adfNode is one node of an Atlassian Document Format tree. Only the parts
// that carry words are decoded: the text a node holds, the children it wraps,
// and the attributes that hold text for nodes such as mentions and links.
type adfNode struct {
	// Type names the node kind, such as paragraph, text, or codeBlock.
	Type string `json:"type"`
	// Text is the literal text of a text node.
	Text string `json:"text"`
	// Content are the node's children.
	Content []adfNode `json:"content"`
	// Attrs holds node attributes; mentions keep their display text here.
	Attrs struct {
		// Text is the rendered text of a mention or similar node.
		Text string `json:"text"`
		// URL is the target of a link node.
		URL string `json:"url"`
	} `json:"attrs"`
}

// adfText flattens a Jira rich-text field into plain words. Jira Cloud's v3 API
// returns descriptions and comments as a nested node tree rather than a
// string, so the words in an issue are unsearchable until the tree is walked.
// Sites and API versions that send a plain string are passed through, and
// anything unrecognized yields nothing rather than an error, since a field
// peepwho cannot read must not fail an index run.
func adfText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return strings.TrimSpace(util.Truncate(plain, maxADFText))
	}
	var root adfNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	var b strings.Builder
	appendADF(&b, root)
	return strings.TrimSpace(util.Truncate(b.String(), maxADFText))
}

// appendADF walks a node tree, writing the words it carries separated by
// spaces. Block nodes are separated the same way, since the result is
// tokenized rather than read as prose.
func appendADF(b *strings.Builder, n adfNode) {
	if b.Len() >= maxADFText {
		return
	}
	for _, s := range []string{n.Text, n.Attrs.Text} {
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
	}
	for _, child := range n.Content {
		appendADF(b, child)
	}
}
