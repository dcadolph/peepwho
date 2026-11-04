package engine

import (
	"fmt"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
)

// GuardShrink refuses to replace a source's contribution with a far smaller
// one. A rate limit or a scope a token quietly lost makes a connector keep
// what it managed to read and return no error, so without this a run that saw
// a fraction of a source would shrink the index while reporting success.
func GuardShrink(existing *index.Index, recs []connector.Record, allow bool) error {
	if existing == nil || allow {
		return nil
	}
	incoming := make(map[string]int)
	for _, rec := range recs {
		incoming[rec.Source]++
	}
	for name, got := range incoming {
		if name == "" {
			continue
		}
		had := existing.SourceSize(name)
		if had > 0 && got*2 < had {
			return fmt.Errorf(
				"%w: %s returned %d records where it last returned %d, so the existing index "+
					"was left alone. Check the messages above for a rate limit or a token that "+
					"lost a scope. Pass --allow-shrink if the source really did get smaller",
				ErrShrunkSource, name, got, had)
		}
	}
	return nil
}
