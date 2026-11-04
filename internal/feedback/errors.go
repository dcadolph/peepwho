package feedback

import "errors"

// ErrBadEntry reports a vote missing its query, target, or a known vote value.
var ErrBadEntry = errors.New("feedback: entry needs a query, one target, and a helpful or not-helpful vote")
