package kb

import "math"

// quantize L2-normalizes v and maps it to int8: q[i] = round(127·x[i]/max|x|), scale = max|x|/127,
// so x[i] ≈ scale·q[i] and cos(a, b) ≈ scaleA·scaleB·Σ qA[i]·qB[i].
func quantize(v []float32) ([]int8, float32) {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	q := make([]int8, len(v))
	if norm == 0 {
		return q, 0
	}
	norm = math.Sqrt(norm)
	var maxAbs float64
	for _, x := range v {
		maxAbs = max(maxAbs, math.Abs(float64(x)/norm))
	}
	for i, x := range v {
		q[i] = int8(math.Round(127 * float64(x) / norm / maxAbs))
	}
	return q, float32(maxAbs / 127)
}

// dot is the int32 dot product of two equal-length int8 vectors (unrolled by 8).
func dot(a, b []int8) int32 {
	b = b[:len(a)]
	var s0, s1, s2, s3 int32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		s0 += int32(a[i])*int32(b[i]) + int32(a[i+4])*int32(b[i+4])
		s1 += int32(a[i+1])*int32(b[i+1]) + int32(a[i+5])*int32(b[i+5])
		s2 += int32(a[i+2])*int32(b[i+2]) + int32(a[i+6])*int32(b[i+6])
		s3 += int32(a[i+3])*int32(b[i+3]) + int32(a[i+7])*int32(b[i+7])
	}
	for ; i < len(a); i++ {
		s0 += int32(a[i]) * int32(b[i])
	}
	return s0 + s1 + s2 + s3
}
