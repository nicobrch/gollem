package semanticcache

import "math"

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return -1
	}

	var dot float64
	var normA float64
	var normB float64

	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return -1
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
