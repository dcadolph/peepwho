// Package fact stores typed statements a crawl cannot find, such as which team
// owns a service or, just as usefully, which team does not. Facts live in their
// own file apart from the index, each labeled with where it came from, so a
// recorded fact never pretends to be a crawled one.
package fact

import "errors"

// ErrBadFact marks a fact missing a subject or object, or naming an unknown
// relation.
var ErrBadFact = errors.New("invalid fact")
