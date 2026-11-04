// Package graph reads an organization's people and reporting lines from
// Microsoft Graph, the live directory behind Entra ID. It is the org-chart
// source that stays current without re-exporting a CSV.
package graph

import "errors"

var (
	// ErrStatus marks a non-200 response from Graph.
	ErrStatus = errors.New("unexpected status")
	// ErrRateLimited marks a request throttled past the retry budget.
	ErrRateLimited = errors.New("rate limited")
	// ErrPagination marks a users listing whose cursor chain could not be
	// followed to its end: a nextLink that left the Graph host, where the
	// bearer token must not go, or more pages than any real directory
	// produces. The read fails outright rather than passing a partial
	// directory off as the organization.
	ErrPagination = errors.New("pagination broken")
)
