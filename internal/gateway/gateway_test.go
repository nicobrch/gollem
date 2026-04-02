package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, Config{
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

	g := New(http.DefaultClient, stubProvider{upstreamURL: upstream.URL}, Config{
		GatewayAPIKey: "gw-key",
		DefaultModel:  "fallback-model",
		MaxBodyBytes:  4096,
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

func TestHandler_LegacyLLMPathIsNotServed(t *testing.T) {
	g := New(http.DefaultClient, stubProvider{upstreamURL: "http://example.com"}, Config{
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
