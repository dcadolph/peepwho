package cmd

import (
	"encoding/json"
	"fmt"
	"io"
)

// writeJSON encodes v to w as JSON, indenting when pretty is true. JSON goes to
// stdout; logs and prompts belong on stderr.
func writeJSON(w io.Writer, v any, pretty bool) error {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}
