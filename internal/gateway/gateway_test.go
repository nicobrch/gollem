package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gollem/internal/gatewaykeys"
	"gollem/internal/semanticcache"
)

type stubProvider struct {
	upstreamURL string
}

type stubSemanticCache struct {
	lookupFn         func(ctx context.Context, keyID string, requestBody []byte) ([]byte, *semanticcache.PreparedLookup, error)
	storeFn          func(ctx context.Context, prepared *semanticcache.PreparedLookup, responseBody []byte, statusCode int, contentType string) error
	maxResponseBytes int64
	lookupCalls      int
	storeCalls       int
	lookupKeyIDs     []string
}

func (s *stubSemanticCache) Lookup(ctx context.Context, keyID string, requestBody []byte) ([]byte, *semanticcache.PreparedLookup, error) {
	s.lookupCalls++
	s.lookupKeyIDs = append(s.lookupKeyIDs, keyID)
	if s.lookupFn == nil {
		return nil, nil, nil
	}
	return s.lookupFn(ctx, keyID, requestBody)
}

func (s *stubSemanticCache) StorePrepared(ctx context.Context, prepared *semanticcache.PreparedLookup, responseBody []byte, statusCode int, contentType string) error {
	s.storeCalls++
	if s.storeFn == nil {
		return nil
	}
	return s.storeFn(ctx, prepared, responseBody, statusCode, contentType)
}

func (s *stubSemanticCache) MaxResponseBytes() int64 {
	if s.maxResponseBytes > 0 {
		return s.maxResponseBytes
	}
	return 1 << 20
}

func (p stubProvider) Name() string {
	return "stub"
}

func (p stubProvider) NewChatCompletionsRequest(ctx context.Context, payload []byte, _ string, _ string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func TestDeploymentNameFromPath(t *testing.T) {
	testCases := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{name: "valid", path: "/openai/deployments/my-deployment/chat/completions", want: "my-deployment", ok: true},
		{name: "encoded", path: "/openai/deployments/my%2Fdep/chat/completions", want: "my/dep", ok: true},
		{name: "invalid suffix", path: "/openai/deployments/my-deployment/chat/other", want: "", ok: false},
		{name: "empty deployment", path: "/openai/deployments//chat/completions", want: "", ok: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := deploymentNameFromPath(tc.path)
			if ok != tc.ok {
				t.Fatalf("expected ok=%v, got %v", tc.ok, ok)
			}
			if got != tc.want {
				t.Fatalf("expected deployment %q, got %q", tc.want, got)
			}
		})
	}
}

func TestHandler_OpenAICompatiblePathUsesDefaultModel(t *testing.T) {
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode upstream payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, nil, Config{
		DefaultModel: "fallback-model",
		MaxBodyBytes: 4096,
	})
	g.keys = manager

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if captured["model"] != "fallback-model" {
		t.Fatalf("expected model %q, got %#v", "fallback-model", captured["model"])
	}
}

func TestHandler_AzureCompatiblePathUsesDeploymentAsModel(t *testing.T) {
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode upstream payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, nil, Config{
		DefaultModel:    "fallback-model",
		AzureDeployment: "gpt-4.1-mini",
		MaxBodyBytes:    4096,
	})
	g.keys = manager

	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/gpt-4.1-mini/chat/completions?api-version=2024-10-21", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if captured["model"] != "gpt-4.1-mini" {
		t.Fatalf("expected model %q, got %#v", "gpt-4.1-mini", captured["model"])
	}
}

func TestHandler_AzureCompatiblePathRejectsUnknownDeployment(t *testing.T) {
	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, manager, Config{
		DefaultModel:    "fallback-model",
		AzureDeployment: "allowed-deployment",
		MaxBodyBytes:    4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/other-deployment/chat/completions?api-version=2024-10-21", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandler_LegacyLLMPathIsNotServed(t *testing.T) {
	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, nil, Config{
		DefaultModel: "fallback-model",
		MaxBodyBytes: 4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/llm", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestHandler_ManagedKeyAuthenticates(t *testing.T) {
	var captured map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("failed to decode upstream payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{Metadata: map[string]string{"email": "owner@example.com"}})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, manager, Config{
		DefaultModel: "fallback-model",
		MaxBodyBytes: 4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if captured["model"] != "fallback-model" {
		t.Fatalf("expected model %q, got %#v", "fallback-model", captured["model"])
	}
}

func TestHandler_RevokedManagedKeyRejected(t *testing.T) {
	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	record, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	if _, err := manager.Revoke(record.ID); err != nil {
		t.Fatalf("failed to revoke key: %v", err)
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, manager, Config{
		DefaultModel: "fallback-model",
		MaxBodyBytes: 4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestAdminEndpoints_KeyLifecycle(t *testing.T) {
	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)

	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, manager, Config{
		AdminAPIKey:  "admin-key",
		MaxBodyBytes: 4096,
	})

	createReq := httptest.NewRequest(http.MethodPost, "/admin/keys", bytes.NewBufferString(`{"metadata":{"email":"owner@example.com"}}`))
	createReq.Header.Set("Authorization", "Bearer admin-key")
	createReq.Header.Set("Content-Type", "application/json")

	createRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, createRR.Code)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createRR.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected created key id")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	listReq.Header.Set("Authorization", "Bearer admin-key")
	listRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, listRR.Code)
	}

	revokeReq := httptest.NewRequest(http.MethodPost, "/admin/keys/"+created.ID+"/revoke", nil)
	revokeReq.Header.Set("Authorization", "Bearer admin-key")
	revokeRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(revokeRR, revokeReq)
	if revokeRR.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, revokeRR.Code)
	}
}

func TestHandler_MaxInFlightRejectsExcessRequests(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer upstream.Close()

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, nil, Config{
		DefaultModel: "fallback-model",
		MaxBodyBytes: 4096,
		MaxInFlight:  1,
	})

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	g.keys = manager

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"first"}]}`))
	firstReq.Header.Set("Authorization", "Bearer "+plainKey)
	firstReq.Header.Set("Content-Type", "application/json")

	firstCode := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		g.Handler().ServeHTTP(rr, firstReq)
		firstCode <- rr.Code
	}()

	<-entered

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"second"}]}`))
	secondReq.Header.Set("Authorization", "Bearer "+plainKey)
	secondReq.Header.Set("Content-Type", "application/json")

	secondRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(secondRR, secondReq)

	if secondRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, secondRR.Code)
	}

	close(release)

	if got := <-firstCode; got != http.StatusOK {
		t.Fatalf("expected first request status %d, got %d", http.StatusOK, got)
	}
}

func TestHandler_SemanticCacheHitBypassesUpstream(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"upstream"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	cache := &stubSemanticCache{
		lookupFn: func(_ context.Context, _ string, _ []byte) ([]byte, *semanticcache.PreparedLookup, error) {
			return []byte(`{"id":"cached"}`), &semanticcache.PreparedLookup{}, nil
		},
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, manager, Config{
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
		SemanticCache: cache,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if rr.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache HIT, got %q", rr.Header().Get("X-Cache"))
	}
	if strings.TrimSpace(rr.Body.String()) != `{"id":"cached"}` {
		t.Fatalf("unexpected cached response body: %s", rr.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("expected upstream to be bypassed, got %d calls", upstreamCalls)
	}
}

func TestHandler_SemanticCacheMissStoresResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"upstream"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	_, plainKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}

	stored := false
	cache := &stubSemanticCache{
		lookupFn: func(_ context.Context, _ string, _ []byte) ([]byte, *semanticcache.PreparedLookup, error) {
			return nil, &semanticcache.PreparedLookup{ScopeKey: "scope", QueryEmbedding: []float64{1, 0}}, nil
		},
		storeFn: func(_ context.Context, prepared *semanticcache.PreparedLookup, responseBody []byte, statusCode int, contentType string) error {
			stored = prepared != nil && statusCode == http.StatusOK && strings.Contains(contentType, "application/json") && strings.TrimSpace(string(responseBody)) == `{"id":"upstream"}`
			return nil
		},
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, manager, Config{
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
		SemanticCache: cache,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+plainKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if rr.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected X-Cache MISS, got %q", rr.Header().Get("X-Cache"))
	}
	if !stored {
		t.Fatalf("expected semantic cache store call with upstream response")
	}
}

func TestHandler_SemanticCacheReceivesPerKeyIdentity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"upstream"}`))
	}))
	defer upstream.Close()

	store := gatewaykeys.NewFileStore(filepath.Join(t.TempDir(), "gateway_keys.json"))
	manager := gatewaykeys.NewManager(store)
	firstRecord, firstKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create first key: %v", err)
	}
	secondRecord, secondKey, err := manager.Create(gatewaykeys.CreateInput{})
	if err != nil {
		t.Fatalf("failed to create second key: %v", err)
	}

	cache := &stubSemanticCache{
		lookupFn: func(_ context.Context, _ string, _ []byte) ([]byte, *semanticcache.PreparedLookup, error) {
			return nil, nil, nil
		},
	}

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, manager, Config{
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
		SemanticCache: cache,
	})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	firstReq.Header.Set("Authorization", "Bearer "+firstKey)
	firstReq.Header.Set("Content-Type", "application/json")
	firstRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(firstRR, firstReq)

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	secondReq.Header.Set("Authorization", "Bearer "+secondKey)
	secondReq.Header.Set("Content-Type", "application/json")
	secondRR := httptest.NewRecorder()
	g.Handler().ServeHTTP(secondRR, secondReq)

	if firstRR.Code != http.StatusOK || secondRR.Code != http.StatusOK {
		t.Fatalf("expected both requests to succeed, got %d and %d", firstRR.Code, secondRR.Code)
	}
	if len(cache.lookupKeyIDs) != 2 {
		t.Fatalf("expected two cache lookups, got %d", len(cache.lookupKeyIDs))
	}
	if cache.lookupKeyIDs[0] != firstRecord.ID {
		t.Fatalf("expected first lookup key id %q, got %q", firstRecord.ID, cache.lookupKeyIDs[0])
	}
	if cache.lookupKeyIDs[1] != secondRecord.ID {
		t.Fatalf("expected second lookup key id %q, got %q", secondRecord.ID, cache.lookupKeyIDs[1])
	}
}
