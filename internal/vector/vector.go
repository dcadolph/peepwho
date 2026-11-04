// Package vector scores embedding vectors. Every searchable store in the
// program compares vectors the same way, so a similarity means the same thing
// wherever it is reported.
package vector

import "math"

// Cosine returns the cosine similarity of a and b, or zero for empty or
// mismatched vectors.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Quantize compresses a vector to signed 8-bit values scaled by its largest
// magnitude, storing it in a quarter of the space of float32. Cosine similarity
// is invariant to the single uniform scale factor, so the quantized values can
// be dequantized as themselves and compared directly without recording the
// scale.
func Quantize(v []float32) []int8 {
	var maxAbs float64
	for _, x := range v {
		if a := math.Abs(float64(x)); a > maxAbs {
			maxAbs = a
		}
	}
	q := make([]int8, len(v))
	if maxAbs == 0 {
		return q
	}
	scale := maxAbs / 127
	for i, x := range v {
		q[i] = int8(math.Round(float64(x) / scale))
	}
	return q
}

// Dequantize turns quantized values back into a float32 vector. It restores the
// direction, not the original magnitudes, which is all cosine similarity needs.
func Dequantize(q []int8) []float32 {
	v := make([]float32, len(q))
	for i, x := range q {
		v[i] = float32(x)
	}
	return v
}
