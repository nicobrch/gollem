package appconfig

import (
	"strings"
	"testing"
)

func setRequiredAzureEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GATEWAY_KEYS_BACKEND", "file")
	t.Setenv("GATEWAY_KEYS_POSTGRES_DSN", "")
	t.Setenv("AZURE_OPENAI_API_KEY", "test-key")
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://example.openai.azure.com")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "gpt4o")
	t.Setenv("AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT", "")
	t.Setenv("SEMANTIC_CACHE_ENABLED", "")
	t.Setenv("SEMANTIC_CACHE_REDIS_ADDR", "")
	t.Setenv("SEMANTIC_CACHE_REDIS_PASSWORD", "")
	t.Setenv("SEMANTIC_CACHE_REDIS_DB", "")
	t.Setenv("SEMANTIC_CACHE_TTL_SECONDS", "")
	t.Setenv("SEMANTIC_CACHE_SIMILARITY_THRESHOLD", "")
	t.Setenv("SEMANTIC_CACHE_MAX_CANDIDATES", "")
	t.Setenv("SEMANTIC_CACHE_MAX_ENTRIES_PER_SCOPE", "")
	t.Setenv("SEMANTIC_CACHE_MAX_RESPONSE_BYTES", "")
}

func TestLoad_RequiresAdminKeyByDefault(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error when GATEWAY_ADMIN_API_KEY is missing")
	}
	if !strings.Contains(err.Error(), "GATEWAY_ADMIN_API_KEY is required") {
		t.Fatalf("expected missing admin key error, got %v", err)
	}
}

func TestLoad_DefaultFileBackend(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if cfg.GatewayKeysBackend != "file" {
		t.Fatalf("expected file backend, got %q", cfg.GatewayKeysBackend)
	}
	if cfg.GatewayKeysFile == "" {
		t.Fatalf("expected default keys file to be set")
	}
}

func TestLoad_PostgresBackendRequiresDSN(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("GATEWAY_KEYS_BACKEND", "postgres")
	t.Setenv("GATEWAY_KEYS_POSTGRES_DSN", "")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error when postgres DSN is missing")
	}
	if !strings.Contains(err.Error(), "GATEWAY_KEYS_POSTGRES_DSN") {
		t.Fatalf("expected postgres DSN error, got %v", err)
	}
}

func TestLoad_PostgresBackendConfigured(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("GATEWAY_KEYS_BACKEND", "postgres")
	t.Setenv("GATEWAY_KEYS_POSTGRES_DSN", "postgres://user:pass@localhost:5432/gollem?sslmode=disable")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if cfg.GatewayKeysBackend != "postgres" {
		t.Fatalf("expected postgres backend, got %q", cfg.GatewayKeysBackend)
	}
	if cfg.GatewayKeysPostgres.DSN == "" {
		t.Fatalf("expected postgres DSN to be set")
	}
}

func TestLoad_RejectsInvalidLogBool(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("LOG_PROMPT_SUMMARIES", "not-a-bool")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid LOG_PROMPT_SUMMARIES value")
	}
	if !strings.Contains(err.Error(), "LOG_PROMPT_SUMMARIES") {
		t.Fatalf("expected error to mention LOG_PROMPT_SUMMARIES, got %v", err)
	}
}

func TestLoad_SemanticCacheDisabledByDefault(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if cfg.SemanticCache.Enabled {
		t.Fatalf("expected semantic cache to be disabled by default")
	}
}

func TestLoad_SemanticCacheRequiresEmbeddingsDeployment(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("SEMANTIC_CACHE_ENABLED", "true")
	t.Setenv("AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT", "")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected semantic cache config error")
	}
	if !strings.Contains(err.Error(), "AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT") {
		t.Fatalf("expected embeddings deployment error, got %v", err)
	}
}

func TestLoad_SemanticCacheConfigured(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("SEMANTIC_CACHE_ENABLED", "true")
	t.Setenv("AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT", "text-embedding-3-small")
	t.Setenv("SEMANTIC_CACHE_REDIS_ADDR", "redis:6379")
	t.Setenv("SEMANTIC_CACHE_REDIS_DB", "2")
	t.Setenv("SEMANTIC_CACHE_TTL_SECONDS", "120")
	t.Setenv("SEMANTIC_CACHE_SIMILARITY_THRESHOLD", "0.95")
	t.Setenv("SEMANTIC_CACHE_MAX_CANDIDATES", "25")
	t.Setenv("SEMANTIC_CACHE_MAX_ENTRIES_PER_SCOPE", "64")
	t.Setenv("SEMANTIC_CACHE_MAX_RESPONSE_BYTES", "2048")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if !cfg.SemanticCache.Enabled {
		t.Fatalf("expected semantic cache enabled")
	}
	if cfg.SemanticCache.RedisAddr != "redis:6379" {
		t.Fatalf("expected redis addr, got %q", cfg.SemanticCache.RedisAddr)
	}
	if cfg.SemanticCache.RedisDB != 2 {
		t.Fatalf("expected redis DB 2, got %d", cfg.SemanticCache.RedisDB)
	}
	if cfg.SemanticCache.TTL.Seconds() != 120 {
		t.Fatalf("expected ttl 120s, got %v", cfg.SemanticCache.TTL)
	}
	if cfg.SemanticCache.SimilarityThreshold != 0.95 {
		t.Fatalf("expected threshold 0.95, got %f", cfg.SemanticCache.SimilarityThreshold)
	}
	if cfg.SemanticCache.MaxCandidates != 25 {
		t.Fatalf("expected max candidates 25, got %d", cfg.SemanticCache.MaxCandidates)
	}
	if cfg.SemanticCache.MaxEntriesPerScope != 64 {
		t.Fatalf("expected max entries 64, got %d", cfg.SemanticCache.MaxEntriesPerScope)
	}
	if cfg.SemanticCache.MaxResponseBytes != 2048 {
		t.Fatalf("expected max response bytes 2048, got %d", cfg.SemanticCache.MaxResponseBytes)
	}
}

func TestLoad_SemanticCacheRejectsInvalidThreshold(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_ADMIN_API_KEY", "admin-key")
	t.Setenv("SEMANTIC_CACHE_ENABLED", "true")
	t.Setenv("AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT", "text-embedding-3-small")
	t.Setenv("SEMANTIC_CACHE_SIMILARITY_THRESHOLD", "1.2")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected invalid threshold error")
	}
	if !strings.Contains(err.Error(), "SEMANTIC_CACHE_SIMILARITY_THRESHOLD") {
		t.Fatalf("expected threshold error, got %v", err)
	}
}
