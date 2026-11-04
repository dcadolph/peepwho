package ownersbench

import "errors"

// ErrProbe reports a saved benchmark read that cannot be scored as asked.
var ErrProbe = errors.New("ownersbench: probe")
