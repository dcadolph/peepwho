package web

import "errors"

// ErrBadRequest marks an error caused by the request itself, such as an
// unknown mode, so the API answers 400 instead of a server error.
var ErrBadRequest = errors.New("web: bad request")
