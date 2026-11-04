package index

import "errors"

// ErrDamaged indicates the index file was read but does not parse. The everyday
// cause is a run that was interrupted while writing, or a file copied in part.
var ErrDamaged = errors.New("index file is damaged")

// ErrSchema indicates the index file was written by a build with a different
// snapshot layout. Decoding it best-effort would drop unknown fields and zero
// missing ones, and a query would then run against a partial index without
// anyone knowing, so the mismatch is refused instead.
var ErrSchema = errors.New("index schema mismatch")

// ErrStaleSidecar indicates the sources sidecar does not hold what the index
// says it must. A crash between the two writes, or a second index run racing
// this one, leaves files from different runs side by side; rebuilding from the
// stale records would silently revert a source to an older read.
var ErrStaleSidecar = errors.New("sources sidecar does not match the index")
