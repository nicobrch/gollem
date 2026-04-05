package appconfig

import (
	"strings"
	"testing"
)

func setRequiredAzureEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AZURE_OPENAI_API_KEY", "test-key")
	t.Setenv("AZURE_OPENAI_BASE_URL", "https://example.openai.azure.com")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT", "gpt4o")
}

func TestLoad_RequiresAdminKeyByDefault(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_API_KEY", "legacy-client-key")
	t.Setenv("GATEWAY_ADMIN_API_KEY", "")
	t.Setenv("ALLOW_LEGACY_ADMIN_KEY_FALLBACK", "false")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error when GATEWAY_ADMIN_API_KEY is missing")
	}
	if !strings.Contains(err.Error(), "GATEWAY_ADMIN_API_KEY is required") {
		t.Fatalf("expected missing admin key error, got %v", err)
	}
}

func TestLoad_AllowsLegacyAdminFallbackWhenEnabled(t *testing.T) {
	setRequiredAzureEnv(t)
	t.Setenv("GATEWAY_API_KEY", "legacy-client-key")
	t.Setenv("GATEWAY_ADMIN_API_KEY", "")
	t.Setenv("ALLOW_LEGACY_ADMIN_KEY_FALLBACK", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if cfg.GatewayAdminKey != "legacy-client-key" {
		t.Fatalf("expected admin key fallback to legacy client key")
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
