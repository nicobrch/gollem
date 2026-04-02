package azure

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"go-llm/internal/appconfig"
)

type Provider struct {
	apiKey      string
	upstreamURL string
}

func New(cfg appconfig.AzureConfig) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("azure provider requires API key")
	}
	if strings.TrimSpace(cfg.UpstreamURL) == "" {
		return nil, fmt.Errorf("azure provider requires upstream URL")
	}

	return &Provider{
		apiKey:      cfg.APIKey,
		upstreamURL: cfg.UpstreamURL,
	}, nil
}

func (p *Provider) Name() string {
	return "azure"
}

func (p *Provider) NewChatCompletionsRequest(ctx context.Context, payload []byte, acceptHeader string, userAgent string) (*http.Request, error) {
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create azure upstream request")
	}

	proxyReq.Header.Set("api-key", p.apiKey)
	proxyReq.Header.Set("Content-Type", "application/json")
	if acceptHeader != "" {
		proxyReq.Header.Set("Accept", acceptHeader)
	}
	if userAgent != "" {
		proxyReq.Header.Set("User-Agent", userAgent)
	}

	return proxyReq, nil
}
