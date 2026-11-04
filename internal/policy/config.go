package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Config is the on-disk policy an organization can ship to pin behavior. When
// Locked is true, user flags cannot loosen it.
type Config struct {
	// Mode is the egress mode name: strict, redacted, or open.
	Mode string `json:"mode"`
	// Locked pins the policy so user flags cannot change it.
	Locked bool `json:"locked"`
	// PrivateChannels is "allow" or "deny" for private-channel ingest.
	PrivateChannels string `json:"private_channels"`
	// Archive is "allow" or "deny" for retaining conversation content.
	Archive string `json:"archive"`
	// FeedbackBundle is "allow" or "deny" for composing the redacted feedback
	// bundle a user can hand-carry to the makers.
	FeedbackBundle string `json:"feedback_bundle"`
}

// Load reads a policy Config from path. found is false when the file is absent;
// an unreadable or malformed file is an error.
func Load(path string) (cfg Config, found bool, err error) {
	if path == "" {
		return Config{}, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("policy: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	return cfg, true, nil
}

// Policy builds an enforced Policy from the config.
func (c Config) Policy() (Policy, error) {
	mode, err := ParseMode(c.Mode)
	if err != nil {
		return Policy{}, err
	}
	p := New(mode, c.Locked)
	privateOK, err := parseAllowDeny("private_channels", c.PrivateChannels)
	if err != nil {
		return Policy{}, err
	}
	if !privateOK {
		p = p.WithoutPrivateChannels()
	}
	archiveOK, err := parseAllowDeny("archive", c.Archive)
	if err != nil {
		return Policy{}, err
	}
	if !archiveOK {
		p = p.WithoutArchive()
	}
	feedbackOK, err := parseAllowDeny("feedback_bundle", c.FeedbackBundle)
	if err != nil {
		return Policy{}, err
	}
	if !feedbackOK {
		p = p.WithoutFeedbackBundle()
	}
	return p, nil
}

// parseAllowDeny reads an allow-or-deny toggle from a config string. An empty
// value keeps the default of allow; an unrecognized value is an error, so a typo
// in a privacy control fails loudly rather than silently permitting.
func parseAllowDeny(name, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return true, nil
	case "allow", "on", "true", "yes":
		return true, nil
	case "deny", "off", "false", "no", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("policy: %s must be allow or deny, got %q", name, value)
	}
}
