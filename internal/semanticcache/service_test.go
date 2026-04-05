package semanticcache

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"gollem/internal/appconfig"
)

type fakeEmbedder struct {
	vectors map[string][]float64
}

func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return []float64{0, 0}, nil
}

func TestService_LookupAndStore(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	s := &Service{
		cfg: appconfig.SemanticCacheConfig{
			Enabled:             true,
			TTL:                 5 * time.Minute,
			SimilarityThreshold: 0.9,
			MaxCandidates:       50,
			MaxEntriesPerScope:  200,
			MaxResponseBytes:    1 << 20,
		},
		embedder: fakeEmbedder{vectors: map[string][]float64{"hello": {1, 0}}},
		redis: redis.NewClient(&redis.Options{
			Addr: mr.Addr(),
		}),
		nowFn: func() time.Time {
			return time.Unix(1000, 0)
		},
		randReader: rand.Reader,
	}
	defer s.Close()

	req := []byte(`{"model":"gpt4o","messages":[{"role":"user","content":"hello"}]}`)

	response, prepared, err := s.Lookup(context.Background(), "key_1", req)
	if err != nil {
		t.Fatalf("lookup should not fail: %v", err)
	}
	if len(response) != 0 {
		t.Fatalf("expected cache miss")
	}
	if prepared == nil {
		t.Fatalf("expected prepared lookup on miss")
	}

	body := []byte(`{"id":"cached"}`)
	if err := s.StorePrepared(context.Background(), prepared, body, 200, "application/json"); err != nil {
		t.Fatalf("store should not fail: %v", err)
	}

	response, _, err = s.Lookup(context.Background(), "key_1", req)
	if err != nil {
		t.Fatalf("lookup should not fail: %v", err)
	}
	if string(response) != string(body) {
		t.Fatalf("expected cached response, got %s", string(response))
	}
}

func TestService_PerKeyIsolation(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	s := &Service{
		cfg: appconfig.SemanticCacheConfig{
			Enabled:             true,
			TTL:                 5 * time.Minute,
			SimilarityThreshold: 0.9,
			MaxCandidates:       50,
			MaxEntriesPerScope:  200,
			MaxResponseBytes:    1 << 20,
		},
		embedder: fakeEmbedder{vectors: map[string][]float64{"hello": {1, 0}}},
		redis: redis.NewClient(&redis.Options{
			Addr: mr.Addr(),
		}),
		nowFn:      time.Now,
		randReader: rand.Reader,
	}
	defer s.Close()

	req := []byte(`{"model":"gpt4o","messages":[{"role":"user","content":"hello"}]}`)

	_, prepared, err := s.Lookup(context.Background(), "key_a", req)
	if err != nil {
		t.Fatalf("lookup should not fail: %v", err)
	}
	if prepared == nil {
		t.Fatalf("expected prepared lookup")
	}

	body := json.RawMessage(`{"id":"from-a"}`)
	if err := s.StorePrepared(context.Background(), prepared, body, 200, "application/json"); err != nil {
		t.Fatalf("store should not fail: %v", err)
	}

	respA, _, err := s.Lookup(context.Background(), "key_a", req)
	if err != nil {
		t.Fatalf("lookup key_a should not fail: %v", err)
	}
	if string(respA) != string(body) {
		t.Fatalf("expected cache hit for key_a")
	}

	respB, _, err := s.Lookup(context.Background(), "key_b", req)
	if err != nil {
		t.Fatalf("lookup key_b should not fail: %v", err)
	}
	if len(respB) != 0 {
		t.Fatalf("expected no cache hit for key_b")
	}
}

type countingEmbedder struct {
	calls int
}

func (e *countingEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	e.calls++
	return []float64{1, 0}, nil
}

func TestService_StreamRequestBypassesLookup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	embedder := &countingEmbedder{}
	s := &Service{
		cfg: appconfig.SemanticCacheConfig{
			Enabled:             true,
			TTL:                 5 * time.Minute,
			SimilarityThreshold: 0.9,
			MaxCandidates:       50,
			MaxEntriesPerScope:  200,
			MaxResponseBytes:    1 << 20,
		},
		embedder: embedder,
		redis: redis.NewClient(&redis.Options{
			Addr: mr.Addr(),
		}),
		nowFn:      time.Now,
		randReader: rand.Reader,
	}
	defer s.Close()

	req := []byte(`{"model":"gpt4o","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	resp, prepared, err := s.Lookup(context.Background(), "key_1", req)
	if err != nil {
		t.Fatalf("lookup should not fail: %v", err)
	}
	if len(resp) != 0 {
		t.Fatalf("expected no cached response")
	}
	if prepared != nil {
		t.Fatalf("expected no prepared lookup for stream request")
	}
	if embedder.calls != 0 {
		t.Fatalf("expected embedder to be bypassed for stream request")
	}
}
