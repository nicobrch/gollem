package providers

import (
	"context"
	"fmt"
	"net/http"

	"go-llm/internal/appconfig"
	"go-llm/internal/providers/azure"
)

type ChatProvider interface {
	Name() string
	NewChatCompletionsRequest(ctx context.Context, payload []byte, acceptHeader string, userAgent string) (*http.Request, error)
}

func New(cfg appconfig.Config) (ChatProvider, error) {
	switch cfg.ProviderName {
	case "azure":
		return azure.New(cfg.Azure)
	default:
		return nil, fmt.Errorf("unsupported provider: %q", cfg.ProviderName)
	}
}
