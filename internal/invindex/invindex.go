// Package invindex encodes an inverted index, a term to per-key weight map, as a
// compact binary blob shared by the people index and the episode store. Interning
// the keys once and storing weights as four bytes, rather than repeating key
// strings and printing floats in a JSON map of maps, is what keeps a store small
// on disk and quick to read back.
package invindex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

// EncodePostings packs a term to per-key weight index into a binary blob: an
// interned key table in sorted order, then for each term its (key-index, float32
// weight) pairs, both in sorted order so the blob is reproducible.
func EncodePostings[K ~string](postings map[string]map[K]float64) []byte {
	keySet := make(map[K]struct{})
	for _, keys := range postings {
		for k := range keys {
			keySet[k] = struct{}{}
		}
	}
	keys := make([]K, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	index := make(map[K]uint32, len(keys))
	for i, k := range keys {
		index[k] = uint32(i)
	}

	var b bytes.Buffer
	var scratch [4]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(scratch[:], v)
		b.Write(scratch[:])
	}
	putStr := func(s string) {
		putU32(uint32(len(s)))
		b.WriteString(s)
	}
	putU32(uint32(len(keys)))
	for _, k := range keys {
		putStr(string(k))
	}
	terms := make([]string, 0, len(postings))
	for term := range postings {
		terms = append(terms, term)
	}
	slices.Sort(terms)
	putU32(uint32(len(terms)))
	for _, term := range terms {
		putStr(term)
		weights := postings[term]
		putU32(uint32(len(weights)))
		idxs := make([]uint32, 0, len(weights))
		for k := range weights {
			idxs = append(idxs, index[k])
		}
		slices.Sort(idxs)
		for _, idx := range idxs {
			putU32(idx)
			putU32(math.Float32bits(float32(weights[keys[idx]])))
		}
	}
	return b.Bytes()
}

// reader reads an EncodePostings blob, tracking the first short read so a
// truncated or corrupt blob becomes an error rather than a panic.
type reader struct {
	b   []byte
	pos int
	err error
}

// u32 reads a little-endian uint32.
func (r *reader) u32() uint32 {
	if r.err != nil {
		return 0
	}
	if r.pos+4 > len(r.b) {
		r.err = fmt.Errorf("invindex: unexpected end of data")
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

// str reads a length-prefixed string.
func (r *reader) str() string {
	n := int(r.u32())
	if r.err != nil {
		return ""
	}
	if n < 0 || r.pos+n > len(r.b) {
		r.err = fmt.Errorf("invindex: unexpected end of data")
		return ""
	}
	s := string(r.b[r.pos : r.pos+n])
	r.pos += n
	return s
}

// DecodePostings rebuilds the inverted index from an EncodePostings blob.
func DecodePostings[K ~string](blob []byte) (map[string]map[K]float64, error) {
	out := make(map[string]map[K]float64)
	if len(blob) == 0 {
		return out, nil
	}
	r := &reader{b: blob}
	numKeys := int(r.u32())
	// A key needs at least its length prefix, so it cannot exceed the blob size.
	// Cap the preallocation at that bound so a corrupt count cannot trigger a
	// giant allocation; append still grows to the real count.
	keys := make([]K, 0, min(max(numKeys, 0), len(blob)))
	for i := 0; i < numKeys && r.err == nil; i++ {
		keys = append(keys, K(r.str()))
	}
	numTerms := int(r.u32())
	for t := 0; t < numTerms && r.err == nil; t++ {
		term := r.str()
		count := int(r.u32())
		weights := make(map[K]float64, min(max(count, 0), len(blob)))
		for e := 0; e < count && r.err == nil; e++ {
			idx := r.u32()
			w := math.Float32frombits(r.u32())
			if r.err != nil {
				break
			}
			// A key index past the table means the blob is corrupt. Dropping the
			// entry would hand back a smaller index that decodes cleanly, and a
			// query would then run against it with nobody told what was lost.
			if int(idx) >= len(keys) {
				return nil, fmt.Errorf("invindex: term %q: key index %d out of range (%d keys)",
					term, idx, len(keys))
			}
			weights[keys[idx]] = float64(w)
		}
		out[term] = weights
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}
