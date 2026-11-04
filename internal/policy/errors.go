package policy

import "errors"

// ErrUnknownMode indicates an unrecognized policy mode name.
var ErrUnknownMode = errors.New("policy: unknown mode")

// ErrEgressDenied indicates the policy forbade an external call.
var ErrEgressDenied = errors.New("policy: egress denied")

// ErrLocked indicates an attempt to change a pinned policy.
var ErrLocked = errors.New("policy: locked")
