package semanticcache

import "testing"

func TestCosineSimilarity(t *testing.T) {
	sim := cosineSimilarity([]float64{1, 0}, []float64{1, 0})
	if sim < 0.9999 {
		t.Fatalf("expected near 1.0 similarity, got %f", sim)
	}

	orthogonal := cosineSimilarity([]float64{1, 0}, []float64{0, 1})
	if orthogonal > 0.0001 {
		t.Fatalf("expected near 0.0 similarity, got %f", orthogonal)
	}
}
