package engine

import "errors"

// ErrBadArgs indicates a missing or invalid argument, such as an unknown mode
// or provider. The command line reports it as its own invalid-arguments error.
var ErrBadArgs = errors.New("invalid arguments")

// ErrUnknownSource indicates an unsupported --source value.
var ErrUnknownSource = errors.New("unknown source")

// ErrAuth indicates a source rejected the credentials or could not find what
// was named. It is distinct so the message can name the token to fix rather
// than surfacing a bare status code.
var ErrAuth = errors.New("source rejected the request")

// ErrShrunkSource indicates a source returned far less than it did last time,
// which is what a rate limit or a lost scope looks like from here.
var ErrShrunkSource = errors.New("source returned much less than before")

// ErrNoRecords indicates a run read nothing from any source. It stops a
// replacing index from writing an empty result over a good one, which is what
// an expired token would otherwise do silently.
var ErrNoRecords = errors.New("nothing was read from any source")
