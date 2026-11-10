package cmd

import (
	"errors"

	"github.com/dcadolph/whotoask/internal/engine"
)

// ErrUnknownSource indicates an unsupported --source value. It is the engine's
// error, so a source the engine rejects reads the same at every caller.
var ErrUnknownSource = engine.ErrUnknownSource

// ErrBadArgs indicates missing or invalid command arguments. It is the engine's
// error, so an argument the engine rejects reads and exits the same way as
// one the command line rejects.
var ErrBadArgs = engine.ErrBadArgs

// ErrServe indicates the web server could not start, most often because
// something already holds the address.
var ErrServe = errors.New("cannot serve")

// ErrNoIndex indicates the index could not be loaded.
var ErrNoIndex = errors.New("no index")

// ErrNoIdentity indicates whotoask could not tell who is asking, which personal
// recall needs before it can scope an answer.
var ErrNoIdentity = errors.New("unknown identity")

// ErrSeal indicates a sealed finding did not check out.
var ErrSeal = errors.New("the seal did not verify")

// ErrAuth indicates a source rejected the credentials or could not find what
// was named. It is the engine's error.
var ErrAuth = engine.ErrAuth

// ErrShrunkSource indicates a source returned far less than it did last time.
// It is the engine's error.
var ErrShrunkSource = engine.ErrShrunkSource

// ErrNoRecords indicates a run read nothing from any source. It is the
// engine's error.
var ErrNoRecords = engine.ErrNoRecords

// ErrEval indicates a measurement could not be read, written, or compared.
var ErrEval = errors.New("cannot evaluate")

// ErrFeedback indicates the feedback bundle could not be written.
var ErrFeedback = errors.New("cannot write feedback")

// ErrPolicy indicates the organization policy forbids the requested action.
var ErrPolicy = errors.New("forbidden by policy")
