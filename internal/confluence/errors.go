package confluence

import "errors"

// ErrStatus indicates an unexpected HTTP status.
var ErrStatus = errors.New("confluence: unexpected status")

// ErrRateLimited indicates the API rate limit was exhausted.
var ErrRateLimited = errors.New("confluence: rate limited")

// ErrTruncated indicates a read hit its page cap while more pages remained, so
// the returned set is partial. The partial pages are returned alongside it.
var ErrTruncated = errors.New("confluence: read truncated")
