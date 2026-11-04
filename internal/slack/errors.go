package slack

import "errors"

// ErrRateLimited indicates Slack kept returning 429 past the retry budget.
var ErrRateLimited = errors.New("slack: rate limited")

// ErrAPI indicates Slack returned a logical error (ok=false).
var ErrAPI = errors.New("slack: api error")

// ErrTruncated indicates a listing hit its page bound while the server still
// offered a cursor, so the returned set is partial. The partial results are
// returned alongside it.
var ErrTruncated = errors.New("slack: listing truncated")
