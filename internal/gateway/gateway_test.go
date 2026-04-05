package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gollem/internal/gatewaykeys"
)

type stubProvider struct {
	upstreamURL string
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

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, nil, Config{
		GatewayAPIKey: "gw-key",
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gw-key")
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

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, nil, Config{
		GatewayAPIKey:   "gw-key",
		DefaultModel:    "fallback-model",
		AzureDeployment: "gpt-4.1-mini",
		MaxBodyBytes:    4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/gpt-4.1-mini/chat/completions?api-version=2024-10-21", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gw-key")
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
	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, nil, Config{
		GatewayAPIKey:   "gw-key",
		DefaultModel:    "fallback-model",
		AzureDeployment: "allowed-deployment",
		MaxBodyBytes:    4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/other-deployment/chat/completions?api-version=2024-10-21", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gw-key")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	g.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandler_LegacyLLMPathIsNotServed(t *testing.T) {
	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, nil, Config{
		GatewayAPIKey: "gw-key",
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
	})

	req := httptest.NewRequest(http.MethodPost, "/llm", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer gw-key")
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
		GatewayAPIKey: "gw-key",
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
		MaxInFlight:   1,
	})

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"first"}]}`))
	firstReq.Header.Set("Authorization", "Bearer gw-key")
	firstReq.Header.Set("Content-Type", "application/json")

	firstCode := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		g.Handler().ServeHTTP(rr, firstReq)
		firstCode <- rr.Code
	}()

	<-entered

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"second"}]}`))
	secondReq.Header.Set("Authorization", "Bearer gw-key")
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
