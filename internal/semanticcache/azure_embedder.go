package semanticcache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gollem/internal/appconfig"
)

const defaultEmbeddingsPath = "/openai/deployments/%s/embeddings"

type azureEmbedder struct {
	httpClient *http.Client
	apiKey     string
	url        string
}

func newAzureEmbedder(httpClient *http.Client, azureCfg appconfig.AzureConfig, deployment string) (*azureEmbedder, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("semantic cache embedder requires an HTTP client")
	}

	apiKey := strings.TrimSpace(azureCfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("semantic cache embedder requires AZURE_OPENAI_API_KEY")
	}

	baseURL := strings.TrimSpace(azureCfg.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("semantic cache embedder requires AZURE_OPENAI_BASE_URL")
	}

	apiVersion := strings.TrimSpace(azureCfg.APIVersion)
	if apiVersion == "" {
		return nil, fmt.Errorf("semantic cache embedder requires AZURE_OPENAI_API_VERSION")
	}

	embeddingURL, err := buildEmbeddingsURL(baseURL, deployment, apiVersion)
	if err != nil {
		return nil, err
	}

	return &azureEmbedder{
		httpClient: httpClient,
		apiKey:     apiKey,
		url:        embeddingURL,
	}, nil
}

func buildEmbeddingsURL(baseURL, deployment, apiVersion string) (string, error) {
	deployment = strings.TrimSpace(deployment)
	if deployment == "" {
		return "", fmt.Errorf("semantic cache embedder requires AZURE_OPENAI_EMBEDDINGS_DEPLOYMENT")
	}

	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("AZURE_OPENAI_BASE_URL must be a valid absolute URL")
	}

	base.Path = strings.TrimRight(base.Path, "/") + fmt.Sprintf(defaultEmbeddingsPath, url.PathEscape(deployment))
	query := base.Query()
	query.Set("api-version", apiVersion)
	base.RawQuery = query.Encode()

	return base.String(), nil
}

type azureEmbeddingRequest struct {
	Input string `json:"input"`
}

type azureEmbeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

type azureEmbeddingError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *azureEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, err := json.Marshal(azureEmbeddingRequest{Input: text})
	if err != nil {
		return nil, fmt.Errorf("failed encoding embeddings request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed creating embeddings request")
	}
	req.Header.Set("api-key", e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request failed")
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("failed reading embeddings response")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var azureErr azureEmbeddingError
		if jsonErr := json.Unmarshal(respBody, &azureErr); jsonErr == nil {
			message := strings.TrimSpace(azureErr.Error.Message)
			if message != "" {
				return nil, fmt.Errorf("embeddings request failed: %s", message)
			}
		}
		return nil, fmt.Errorf("embeddings request failed with status %d", resp.StatusCode)
	}

	var parsed azureEmbeddingResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed decoding embeddings response")
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings response missing vectors")
	}

	return parsed.Data[0].Embedding, nil
}
