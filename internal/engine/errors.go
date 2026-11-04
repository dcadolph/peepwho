package engine

import "errors"

// ErrBadArgs indicates a missing or invalid argument, such as an unknown mode
// or provider. The command line reports it as its own invalid-arguments error.
var ErrBadArgs = errors.New("invalid arguments")
