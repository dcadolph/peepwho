package jira

import "errors"

// ErrStatus indicates an unexpected HTTP status.
var ErrStatus = errors.New("jira: unexpected status")

// ErrRateLimited indicates the API rate limit was exhausted.
var ErrRateLimited = errors.New("jira: rate limited")

// ErrTruncated indicates a search hit its issue cap while more issues matched,
// so the returned set is partial. The partial issues are returned alongside it,
// and the error text says how many were read, and of how many when the
// deployment reports a total.
var ErrTruncated = errors.New("jira: search truncated")
