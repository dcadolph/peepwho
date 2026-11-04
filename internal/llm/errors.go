package llm

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrEmptyResponse indicates the model returned no content.
var ErrEmptyResponse = errors.New("llm: empty response")

// ErrModel indicates a model backend was unreachable or reported an error.
var ErrModel = errors.New("llm: model error")

// apiErrorMessage extracts a provider's error message from a response body,
// falling back to a trimmed snippet of the body when it is not the expected
// JSON, so a non-JSON gateway error still surfaces the underlying status.
func apiErrorMessage(raw []byte) string {
	var body struct {
		// Error carries the provider's structured error.
		Error struct {
			// Message describes the error.
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil {
		if msg := strings.TrimSpace(body.Error.Message); msg != "" {
			return msg
		}
	}
	return snippet(raw)
}

// snippet returns a whitespace-trimmed, length-bounded view of raw.
func snippet(raw []byte) string {
	const maxLen = 200
	s := strings.TrimSpace(string(raw))
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
