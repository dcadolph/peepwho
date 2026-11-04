// Package util holds small helpers shared across peepwho's packages.
package util

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// NormalizeEmail returns email trimmed of surrounding whitespace and
// lowercased. It is the canonical form of a person's merge key, so stray casing
// or spacing never splits one human across records from different sources.
func NormalizeEmail(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(e, '@')
	if at <= 0 {
		return e
	}
	local, domain := e[:at], e[at+1:]
	// GitHub's private commit address is the one place the plus runs the other
	// way. In 2481736+tlrenard@users.noreply.github.com the account id is in
	// front and the username behind, so the usual fold keeps the meaningless
	// half and throws the person away: it leaves 2481736@users.noreply.github.com,
	// which matches no handle any source of record ever writes down. Measured on
	// home-assistant/core, 424 of 1,272 authors commit under this form.
	if domain == githubNoReply {
		if plus := strings.IndexByte(local, '+'); plus >= 0 && plus+1 < len(local) {
			local = local[plus+1:]
		}
		return local + "@" + domain
	}
	// Drop a plus-tag: alice+ci@x.com and alice@x.com are the same mailbox.
	// Dots are left alone because many corporate domains make first.last
	// significant, so folding them would merge distinct people.
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	return local + "@" + domain
}

// githubNoReply is the domain GitHub issues private commit addresses on. It
// carries a username rather than a mailbox, which is why it is normalized
// differently from every other address.
const githubNoReply = "users.noreply.github.com"

// roleLocals are email local-parts that name a function or shared team mailbox
// rather than one person, so an address at one of them must not be used to merge
// two distinct people.
//
//nolint:gochecknoglobals // Read-only lookup table.
var roleLocals = map[string]bool{
	"admin": true, "administrator": true, "support": true, "help": true,
	"helpdesk": true, "info": true, "sales": true, "contact": true,
	"noreply": true, "no-reply": true, "donotreply": true, "do-not-reply": true,
	"team": true, "teams": true, "dev": true, "devops": true, "ops": true,
	"oncall": true, "on-call": true, "alerts": true, "alert": true,
	"notifications": true, "notification": true, "hello": true,
	"billing": true, "accounts": true, "accounting": true, "hr": true,
	"security": true, "abuse": true, "postmaster": true, "webmaster": true,
	"marketing": true, "press": true, "jobs": true, "careers": true,
	"recruiting": true, "root": true, "service": true, "services": true,
	"system": true, "sysadmin": true, "bot": true,
}

// IsRoleEmail reports whether email addresses a function or shared team mailbox
// rather than a single person, so a caller can avoid merging two people who
// happen to share it. It checks the local-part after normalization.
func IsRoleEmail(email string) bool {
	e := NormalizeEmail(email)
	at := strings.IndexByte(e, '@')
	if at <= 0 {
		return false
	}
	return roleLocals[e[:at]]
}

// Truncate returns s cut to at most max bytes without splitting a UTF-8 rune.
// Source text such as a pull request description or an issue body is indexed
// for the words it carries, so a pasted log dump is cut rather than allowed to
// drown the text that explains something.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		if max <= 0 {
			return ""
		}
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// WriteFileAtomic writes data to path through a same-directory temporary file
// and a rename, so a crash never leaves a partial or truncated file behind.
// perm applies to the final file even when path already exists looser.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		// The raw error names the randomly-suffixed temp file, which never
		// existed and means nothing to a user. Report the directory that could
		// not be written and the underlying cause instead.
		cause := err
		var pe *fs.PathError
		if errors.As(err, &pe) {
			cause = pe.Err
		}
		return fmt.Errorf("cannot write to directory %s: %w", filepath.Dir(path), cause)
	}
	name := tmp.Name()
	if err := fillTemp(tmp, data, perm); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("atomic write: rename: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a rename into it survives a crash. A directory
// that cannot be opened for sync, as on some platforms, is not an error.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("atomic write: sync dir: %w", err)
	}
	return nil
}

// fillTemp writes data and perm to the open temporary file and closes it.
func fillTemp(f *os.File, data []byte, perm fs.FileMode) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic write: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic write: sync: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("atomic write: chmod: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("atomic write: close: %w", err)
	}
	return nil
}

// GitHubNoreplyLogin returns the GitHub login encoded in a GitHub noreply commit
// email, and whether email was one. GitHub issues commit emails of the form
// "ID+login@users.noreply.github.com", and older ones as
// "login@users.noreply.github.com", so a commit made under a private email can
// still be joined to that person's GitHub activity without exposing a real
// address.
func GitHubNoreplyLogin(email string) (string, bool) {
	const suffix = "@users.noreply.github.com"
	e := strings.ToLower(strings.TrimSpace(email))
	if !strings.HasSuffix(e, suffix) {
		return "", false
	}
	local := strings.TrimSuffix(e, suffix)
	if i := strings.IndexByte(local, '+'); i >= 0 {
		local = local[i+1:]
	}
	if local == "" {
		return "", false
	}
	return local, true
}

// NamesATeam reports whether an identifier names a group rather than a person,
// which a CODEOWNERS owner like @org/team does with its slash. A group cannot
// be tied to a mailbox, so identity joining, unlinked-owner accounting, and the
// people directory all treat it as a different kind of thing.
func NamesATeam(id string) bool {
	if _, handle, ok := strings.Cut(id, ":"); ok {
		id = handle
	}
	return strings.Contains(id, "/")
}

// Distinct returns the items whose key has not been seen before, keeping the
// order they arrived in and dropping any whose key is the zero value. Callers
// were hand-rolling this loop everywhere a source returns the same person or
// the same subject more than once, which is most of them: a comment thread
// names its author on every comment, and a path yields the same subject at
// every level.
//
// It is deliberately not used for the visited sets of a graph walk. Those look
// similar and mean something else, and reading one as the other is how a walk
// stops early.
func Distinct[T any, K comparable](items []T, key func(T) K) []T {
	var zero K
	seen := make(map[K]bool, len(items))
	out := make([]T, 0, len(items))
	for _, item := range items {
		k := key(item)
		if k == zero || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item)
	}
	return out
}

// SameFamily reports whether two subjects are one name seen twice, such as
// zwave and zwave_js, which the path tokenizer produces from a single
// directory. Those agree by construction and say nothing.
func SameFamily(a, b string) bool {
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return true
	}
	aw := strings.FieldsFunc(a, func(r rune) bool { return r == '_' || r == '-' })
	for _, w := range aw {
		if w == b {
			return true
		}
	}
	bw := strings.FieldsFunc(b, func(r rune) bool { return r == '_' || r == '-' })
	for _, w := range bw {
		if w == a {
			return true
		}
	}
	return false
}

// PersonKey settles which identifier a person is keyed by: their email when a
// source gave one, and the source's own id when it did not.
//
// An email is preferred because it is the one identifier that means the same
// thing in every source, so work recorded under a Jira account and a commit
// signed with the same address land on one person. The id is namespaced by
// source, since two systems reusing an id are not one person. An empty string
// means the source gave nothing to key on.
func PersonKey(source, email, id string) string {
	if email != "" {
		return NormalizeEmail(email)
	}
	if id != "" {
		return source + ":" + id
	}
	return ""
}

// GrammarWords are the words that hold a phrase together without naming any
// part of it. They are never a subject on their own, and a phrase containing one
// is a snatch of a sentence rather than the name of a thing.
//
// It stays deliberately small and holds only grammar. Verbs and nouns are not in
// here however dull, because "update" and "support" are real integrations and
// "change" and "parameter" are real words about real work.
var GrammarWords = map[string]bool{
	"a": true, "an": true, "and": true, "any": true, "are": true, "after": true,
	"also": true, "as": true, "at": true, "be": true, "been": true, "before": true,
	"both": true, "but": true, "by": true, "could": true, "during": true,
	"each": true, "else": true, "for": true, "from": true, "had": true, "has": true,
	"have": true, "here": true, "how": true, "if": true, "in": true, "into": true,
	"is": true, "it": true, "its": true, "nor": true, "not": true, "of": true,
	"on": true, "only": true, "or": true, "our": true, "per": true, "should": true,
	"so": true, "some": true, "than": true, "that": true, "the": true, "their": true,
	"them": true, "then": true, "there": true, "they": true, "this": true, "to": true,
	"via": true, "was": true, "were": true, "what": true, "when": true, "where": true,
	"which": true, "while": true, "who": true, "whom": true, "why": true,
	"will": true, "with": true, "would": true, "yet": true,
}

// ReadsAsName reports whether a hyphenated phrase names something, as opposed to
// being a run of adjacent words that happened to sit together in a sentence.
//
// Two tests, both learned from a real issue tracker. No part may be grammar,
// which is what separates state-store and jwt-bearer from should-have and
// during-rebalance. And no part may start with a digit, which is what a ticket
// reference looks like: kip-1076, hive-3-2-0-must.
func ReadsAsName(s string) bool {
	if s == "" {
		return false
	}
	for _, w := range strings.Split(s, "-") {
		if w == "" || GrammarWords[w] || (w[0] >= '0' && w[0] <= '9') {
			return false
		}
	}
	return true
}
