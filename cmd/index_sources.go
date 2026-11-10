package cmd

import "github.com/dcadolph/whotoask/internal/engine"

// explainSourceError turns a low-level fetch failure into an actionable message
// naming the source and the credential to check. It is the engine's
// ExplainSourceError, named here for the commands that share it.
func explainSourceError(source, tokenEnv string, err error) error {
	return engine.ExplainSourceError(source, tokenEnv, err)
}
