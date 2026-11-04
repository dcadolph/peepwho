package invindex

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// TestPostingsCodec verifies the encoding round-trips within float32 precision,
// handles an empty index, and rejects a truncated blob rather than panicking.
func TestPostingsCodec(t *testing.T) {
	t.Parallel()
	in := map[string]map[string]float64{
		"billing": {"jane@x.com": 1.5, "bob@x.com": 0.25},
		"retries": {"jane@x.com": 2.0},
		"empty":   {},
	}
	blob := EncodePostings(in)
	out, err := DecodePostings[string](blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("terms = %d, want %d", len(out), len(in))
	}
	for term, keys := range in {
		for k, w := range keys {
			if got := out[term][k]; math.Abs(got-w) > 1e-6 {
				t.Errorf("%s/%s = %v, want %v", term, k, got, w)
			}
		}
	}
	if empty, err := DecodePostings[string](nil); err != nil || len(empty) != 0 {
		t.Errorf("empty decode = %v, %v; want empty and no error", empty, err)
	}
	if _, err := DecodePostings[string](blob[:len(blob)/2]); err == nil {
		t.Error("a truncated blob did not error")
	}
}

// TestDecodeRejectsOutOfRangeKeyIndex verifies a blob whose posting entry
// points past the key table is refused rather than silently dropped. Dropping
// the entry hands back a smaller index that decodes cleanly, and a query then
// runs against it with nobody told what was lost.
func TestDecodeRejectsOutOfRangeKeyIndex(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	putU32 := func(v uint32) {
		var scratch [4]byte
		binary.LittleEndian.PutUint32(scratch[:], v)
		b.Write(scratch[:])
	}
	putStr := func(s string) {
		putU32(uint32(len(s)))
		b.WriteString(s)
	}
	putU32(1)
	putStr("jane@x.com")
	putU32(1)
	putStr("billing")
	putU32(1)
	putU32(5) // Key index past the one-entry table.
	putU32(math.Float32bits(1.5))

	_, err := DecodePostings[string](b.Bytes())
	if err == nil {
		t.Fatal("an out-of-range key index decoded without error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error %q does not name the out-of-range index", err)
	}
}
