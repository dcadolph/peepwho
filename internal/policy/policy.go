// Package policy decides what peepwho may send to model providers. Strict keeps
// answers fully local, Redacted permits redacted payloads to known provider
// hosts only, and Open permits any destination. Indexing calls made with the
// user's own credentials against sources the user names are outside its scope.
package policy

import (
	"fmt"
	"net/http"
	"strings"
)

// egressTransport enforces the egress policy on the actual outbound host of
// every request, so a payload cannot physically leave for a host the policy
// forbids even if a call path forgot the up-front check. It is defense in depth
// behind the call-site AllowEgress checks, not a replacement for them.
type egressTransport struct {
	// base performs the request once the host is allowed.
	base http.RoundTripper
	// pol decides whether a host may be reached.
	pol Policy
}

// Transport wraps base so every request is allowed by pol before it is sent. A
// nil base uses http.DefaultTransport.
func Transport(base http.RoundTripper, pol Policy) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &egressTransport{base: base, pol: pol}
}

// RoundTrip checks the request's host against the policy, refusing to send when
// it is not allowed.
func (t *egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.pol.AllowEgress(req.URL.Hostname()); err != nil {
		return nil, fmt.Errorf("policy: egress to %s: %w", req.URL.Hostname(), err)
	}
	return t.base.RoundTrip(req)
}

// Mode is a data egress posture.
type Mode int

const (
	// Strict forbids all egress: nothing leaves the machine.
	Strict Mode = iota
	// Redacted permits egress only after the caller has redacted the payload.
	Redacted
	// Open permits egress without restriction.
	Open
)

// String returns the lowercase name of the mode.
func (m Mode) String() string {
	switch m {
	case Strict:
		return "strict"
	case Redacted:
		return "redacted"
	case Open:
		return "open"
	default:
		return "unknown"
	}
}

// ParseMode parses a mode name, defaulting to Strict on empty input.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "strict":
		return Strict, nil
	case "redacted":
		return Redacted, nil
	case "open":
		return Open, nil
	default:
		return Strict, fmt.Errorf("%w: %q: use strict, redacted, or open", ErrUnknownMode, s)
	}
}

// Policy decides whether data may leave the machine. A locked policy is pinned
// by an organization and cannot be loosened by user flags.
type Policy struct {
	// mode is the current egress posture.
	mode Mode
	// locked marks the policy as pinned and unoverridable when true.
	locked bool
	// privateOff, when true, forbids private-channel ingest regardless of flags.
	privateOff bool
	// archiveOff pins retention of conversation content off.
	archiveOff bool
	// feedbackOff pins the feedback bundle off, so not even a hand-carried
	// report can be composed on this machine.
	feedbackOff bool
}

// New returns a Policy with the given mode and lock state.
func New(mode Mode, locked bool) Policy {
	return Policy{mode: mode, locked: locked}
}

// Default returns the deny-all Strict policy.
func Default() Policy {
	return Policy{mode: Strict}
}

// Mode returns the policy's current mode.
func (p Policy) Mode() Mode { return p.mode }

// Locked reports whether the policy is pinned and cannot be loosened.
func (p Policy) Locked() bool { return p.locked }

// AllowPrivateChannels reports whether ingesting private channels is permitted.
// An organization can pin this off; user flags then cannot enable it.
func (p Policy) AllowPrivateChannels() bool { return !p.privateOff }

// AllowArchive reports whether retaining conversation content is permitted. An
// organization that keeps a short retention period on purpose can pin this
// off, so no local archive outlives the record its own policy deletes.
func (p Policy) AllowArchive() bool { return !p.archiveOff }

// AllowFeedbackBundle reports whether composing the redacted feedback bundle
// is permitted. The bundle never sends itself anywhere, but an organization
// that wants no report of any shape leaving its walls can pin even the manual
// path off, and a lock enforces what a promise only asserts.
func (p Policy) AllowFeedbackBundle() bool { return !p.feedbackOff }

// WithoutFeedbackBundle returns a copy that forbids composing the bundle.
func (p Policy) WithoutFeedbackBundle() Policy {
	c := p
	c.feedbackOff = true
	return c
}

// WithoutArchive returns a copy that forbids retaining conversation content.
func (p Policy) WithoutArchive() Policy {
	c := p
	c.archiveOff = true
	return c
}

// WithoutPrivateChannels returns a copy that forbids private-channel ingest.
// This is how an organization pins private ingest off.
func (p Policy) WithoutPrivateChannels() Policy {
	c := p
	c.privateOff = true
	return c
}

// AllowEgress reports whether sending data to dest is permitted. Strict always
// denies. Redacted permits only known model provider hosts, which must receive
// redacted payloads. Open permits any destination.
func (p Policy) AllowEgress(dest string) error {
	switch p.mode {
	case Open:
		return nil
	case Redacted:
		if knownProviderDest(dest) {
			return nil
		}
		return fmt.Errorf("%w: mode=%s dest=%s is not a known model provider", ErrEgressDenied, p.mode, dest)
	default:
		return fmt.Errorf("%w: mode=%s dest=%s", ErrEgressDenied, p.mode, dest)
	}
}

// knownProviderDest reports whether dest is a known model provider API host:
// Anthropic, OpenAI, or Google's Gemini endpoint.
func knownProviderDest(dest string) bool {
	switch dest {
	case "api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com":
		return true
	default:
		return false
	}
}

// WithMode returns a copy at the requested mode, carrying over every other
// field including the private-channel pin. A locked policy cannot change to a
// different mode and returns ErrLocked.
func (p Policy) WithMode(mode Mode) (Policy, error) {
	if p.locked && mode != p.mode {
		return p, fmt.Errorf("%w: pinned at %s", ErrLocked, p.mode)
	}
	c := p
	c.mode = mode
	return c, nil
}
