package text

import (
	"regexp"
	"strings"
)

// redactedMark replaces each secret found in ingested text. It is a plain
// word on purpose: it tokenizes cleanly, it is greppable in a stored index,
// and it never carries any part of what it replaced.
const redactedMark = "[redacted]"

// secretPattern is one credential shape and how a match of it is rewritten.
type secretPattern struct {
	// re matches the credential as it actually appears in pasted text.
	re *regexp.Regexp
	// keep rebuilds the replacement from the submatches, preserving the parts
	// that are context rather than secret; nil replaces the whole match.
	keep func(sub []string) string
}

// secretPatterns match credentials as they actually appear in pasted chat and
// ticket text. Each pattern must be specific enough that prose never matches:
// a missed secret is redacted at the next pattern down, but a false positive
// eats someone's sentence.
var secretPatterns = []secretPattern{
	// Private key blocks, PEM armor and everything inside.
	{re: regexp.MustCompile(
		`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	// AWS access key ids, and their secret keys when labeled.
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	// GitHub tokens: classic ghp_/gho_/ghu_/ghs_/ghr_ and fine-grained.
	{re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,255}\b`)},
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,255}\b`)},
	// Slack tokens: bot, user, app-level config, legacy, and the xoxc/xoxd
	// session pair, whose cookie half is pasted URL-encoded, hence the wider
	// character class and no trailing boundary to cut a padding "=" loose.
	{re: regexp.MustCompile(`\bxox[abcdeprs]-[A-Za-z0-9%+/=-]{10,255}`)},
	{re: regexp.MustCompile(`\bxapp-[A-Za-z0-9-]{10,255}`)},
	// Stripe secret and restricted keys.
	{re: regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,255}\b`)},
	// OpenAI and Anthropic API keys, prefixed or bare. These are the providers
	// peepwho itself supports, so they are certain to be pasted near it. The
	// dashed tails demand length a hyphenated prose slug never reaches, since
	// "sk-proj-management-plan" is a phrase someone could actually write.
	// The dashed tail is tried first: alternation is leftmost-first, so the
	// other way around a long dashed key would end at its first hyphen and the
	// rest of it would survive.
	{re: regexp.MustCompile(`\bsk-(?:` +
		`(?:proj|svcacct|admin)-(?:[A-Za-z0-9_-]{60,255}|[A-Za-z0-9_]{20,255})|` +
		`ant-[a-z0-9]{2,10}-[A-Za-z0-9_-]{20,255}|[A-Za-z0-9]{32,255})\b`)},
	// Google API keys.
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	// JWTs: three dot-separated base64url parts, first decoding to '{"'.
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)},
	// URL userinfo passwords: https://alice:hunter2@db.example.com. No label and
	// no known prefix, so nothing else here would catch it, and it is one of
	// the commonest paste shapes in incident channels. The scheme and user
	// survive; the password goes. The value class stops at /, ? and # so a
	// plain URL with a port and a later @ in its query cannot match.
	{re: regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s/:@]+:)([^\s@/?#]{3,256})@`),
		keep: func(sub []string) string { return sub[1] + redactedMark + "@" }},
	// Bearer and token authorization header values; the scheme word survives.
	{re: regexp.MustCompile(`(?i)\b(bearer|authorization:)\s+[A-Za-z0-9._~+/=-]{16,512}`),
		keep: func(sub []string) string { return sub[1] + " " + redactedMark }},
	// Labeled credentials: password=..., api_key: "...", export TOKEN=... The
	// label and separator survive; the value goes. The separator must be an
	// explicit = or :, because "my token expired" is a sentence, not a leak.
	{re: regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|apikey|` +
		`access[_-]?key|client[_-]?secret|private[_-]?key|auth)\b(\s*[:=]+\s*)["']?[^\s"']{6,}["']?`),
		keep: func(sub []string) string {
			return sub[1] + strings.TrimRight(sub[2], `"'`) + redactedMark
		}},
}

// Scrub replaces credential-shaped substrings with a redaction mark and
// reports how many were found. It runs over every piece of prose peepwho
// ingests, because people paste keys into Slack and tickets, and a
// who-knows-what index must never become a where-the-secrets-are index.
func Scrub(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	found := 0
	for _, pat := range secretPatterns {
		if !pat.re.MatchString(s) {
			continue
		}
		if pat.keep == nil {
			s = pat.re.ReplaceAllStringFunc(s, func(string) string {
				found++
				return redactedMark
			})
			continue
		}
		re := pat.re
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			found++
			return pat.keep(re.FindStringSubmatch(m))
		})
	}
	return s, found
}
