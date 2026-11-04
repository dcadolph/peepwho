package github

import "errors"

// ErrNotFound indicates the requested resource does not exist.
var ErrNotFound = errors.New("github: not found")

// ErrRateLimited indicates the API rate limit was exhausted.
var ErrRateLimited = errors.New("github: rate limited")

// ErrStatus indicates an unexpected HTTP status.
var ErrStatus = errors.New("github: unexpected status")

// ErrTruncated indicates a listing hit the page cap while more pages remained,
// so the returned set is partial. The partial results are returned alongside it.
var ErrTruncated = errors.New("github: listing truncated")
