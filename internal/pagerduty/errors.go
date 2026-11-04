package pagerduty

import "errors"

// ErrStatus indicates an unexpected HTTP status.
var ErrStatus = errors.New("pagerduty: unexpected status")

// ErrRateLimited indicates the API rate limit was exhausted.
var ErrRateLimited = errors.New("pagerduty: rate limited")
