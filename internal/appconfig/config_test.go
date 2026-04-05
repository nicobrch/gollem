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
