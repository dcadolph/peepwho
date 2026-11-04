package util

import (
	"fmt"
	"io"
	"sync"
)

// Progress reports how many items a long operation has finished so far. A nil
// Progress is a no-op, so a caller can invoke it unconditionally from inside a
// paging loop without checking.
type Progress func(done int)

// Report calls p with the running count when p is non-nil.
func (p Progress) Report(done int) {
	if p != nil {
		p(done)
	}
}

// ProgressWriter returns a Progress that writes a running count to w under a
// label, at most once per interval items so a large fetch does not flood the
// log. The point is that a user watching a long index sees movement rather
// than a line that only appears once everything has already arrived. A
// non-positive interval reports every call. It is safe for concurrent use.
func ProgressWriter(w io.Writer, label string, interval int) Progress {
	if w == nil {
		return nil
	}
	var (
		mu       sync.Mutex
		reported int
	)
	return func(done int) {
		mu.Lock()
		defer mu.Unlock()
		if interval > 0 && done < reported+interval {
			return
		}
		reported = done
		fmt.Fprintf(w, "%s: %d so far...\n", label, done)
	}
}
