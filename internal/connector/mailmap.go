package connector

import (
	"os"
	"path/filepath"
	"strings"
)

// mailmap canonicalizes commit authors the way git does, keyed by the commit
// email. It lets two emails or spellings for one person collapse to a single
// identity before the git connector tallies expertise.
type mailmap map[string]*mailmapEntry

// mailmapEntry holds the replacements for one commit email: a default that
// applies to any commit name, and overrides keyed by a specific commit name.
type mailmapEntry struct {
	// def replaces any commit with this email; nil if only name overrides exist.
	def *mailmapProper
	// byName replaces a commit only when its lowercased name matches the key.
	byName map[string]*mailmapProper
}

// mailmapProper is a canonical name and email a commit author maps to. An empty
// name means keep the commit's original name.
type mailmapProper struct {
	// name is the canonical display name, or empty to keep the original.
	name string
	// email is the canonical email; always set.
	email string
}

// loadMailmap reads the .mailmap at a repository root, returning nil when the
// file is absent, unreadable, or empty. Parsing never fails the run.
func loadMailmap(root string) mailmap {
	data, err := os.ReadFile(filepath.Join(root, ".mailmap"))
	if err != nil {
		return nil
	}
	m := make(mailmap)
	for line := range strings.SplitSeq(string(data), "\n") {
		proper, commit, ok := parseMailmapLine(line)
		if ok {
			m.add(commit, proper)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// add records that commits matching commit map to proper.
func (m mailmap) add(commit, proper mailmapProper) {
	key := strings.ToLower(strings.TrimSpace(commit.email))
	e := m[key]
	if e == nil {
		e = &mailmapEntry{byName: make(map[string]*mailmapProper)}
		m[key] = e
	}
	p := proper
	if name := strings.ToLower(strings.TrimSpace(commit.name)); name != "" {
		e.byName[name] = &p
	} else {
		e.def = &p
	}
}

// resolve returns the canonical name and email for a commit author, or the
// inputs unchanged when nothing matches.
func (m mailmap) resolve(name, email string) (string, string) {
	e := m[strings.ToLower(strings.TrimSpace(email))]
	if e == nil {
		return name, email
	}
	if p := e.byName[strings.ToLower(strings.TrimSpace(name))]; p != nil {
		return orElse(p.name, name), p.email
	}
	if e.def != nil {
		return orElse(e.def.name, name), e.def.email
	}
	return name, email
}

// parseMailmapLine parses one .mailmap line into the proper identity and the
// commit identity it replaces. It reports false for comments, blank lines, and
// lines without a usable email.
func parseMailmapLine(line string) (proper, commit mailmapProper, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return proper, commit, false
	}
	name1, email1, rest, ok := cutAngle(line)
	if !ok {
		return proper, commit, false
	}
	proper = mailmapProper{name: name1, email: email1}
	name2, email2, _, ok := cutAngle(rest)
	if !ok {
		return proper, mailmapProper{email: email1}, true
	}
	return proper, mailmapProper{name: name2, email: email2}, true
}

// cutAngle splits the text before the next <email> from the email and the
// remainder after the closing angle bracket. It reports false when no complete
// <email> is present.
func cutAngle(s string) (name, email, rest string, ok bool) {
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		return "", "", "", false
	}
	gt := strings.IndexByte(s[lt:], '>')
	if gt < 0 {
		return "", "", "", false
	}
	gt += lt
	name = strings.TrimSpace(s[:lt])
	email = strings.TrimSpace(s[lt+1 : gt])
	rest = s[gt+1:]
	return name, email, rest, email != ""
}

// orElse returns v when non-empty, otherwise fallback.
func orElse(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
