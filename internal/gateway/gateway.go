package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"go-llm/internal/providers"
)

const defaultUserAgent = "go-llm/0.1"

const (
	azureChatCompletionsPathPrefix = "/openai/deployments/"
	azureChatCompletionsPathSuffix = "/chat/completions"
)

type Config struct {
	GatewayAPIKey string
	DefaultModel  string
	MaxBodyBytes  int64
}

type Gateway struct {
	client   *http.Client
	provider providers.ChatProvider
	cfg      Config
}

func New(client *http.Client, provider providers.ChatProvider, cfg Config) *Gateway {
	return &Gateway{
		client:   client,
		provider: provider,
		cfg:      cfg,
	}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.healthzHandler)
	mux.HandleFunc("/chat/completions", g.chatCompletionsHandler(""))
	mux.HandleFunc("/v1/chat/completions", g.chatCompletionsHandler(""))
	mux.HandleFunc(azureChatCompletionsPathPrefix, g.azureChatCompletionsHandler)
	return mux
}

func (g *Gateway) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (g *Gateway) chatCompletionsHandler(deploymentHint string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.handleChatCompletions(w, r, deploymentHint)
	}
}

func (g *Gateway) azureChatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	deploymentName, ok := deploymentNameFromPath(r.URL.Path)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "unsupported path", "not_found")
		return
	}

	g.handleChatCompletions(w, r, deploymentName)
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request, deploymentHint string) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}

	if !g.isAuthorized(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid gateway API key", "invalid_api_key")
		return
	}

	body, err := readRequestBody(r, g.cfg.MaxBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		code := "invalid_request_error"
		if errors.Is(err, errBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
			code = "payload_too_large"
		}
		writeOpenAIError(w, status, err.Error(), code)
		return
	}

	normalizedBody, err := normalizeChatCompletionRequest(body, g.cfg.DefaultModel, deploymentHint)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	acceptHeader := strings.TrimSpace(r.Header.Get("Accept"))
	proxyReq, err := g.provider.NewChatCompletionsRequest(r.Context(), normalizedBody, acceptHeader, defaultUserAgent)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to create upstream request", "gateway_internal_error")
		return
	}

	resp, err := g.client.Do(proxyReq)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "upstream request failed", "upstream_unavailable")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if err := streamCopy(w, resp.Body); err != nil {
		log.Printf("response streaming failed: %v", err)
	}
}

func deploymentNameFromPath(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, azureChatCompletionsPathPrefix) {
		return "", false
	}
	if !strings.HasSuffix(requestPath, azureChatCompletionsPathSuffix) {
		return "", false
	}

	deploymentSegment := strings.TrimPrefix(requestPath, azureChatCompletionsPathPrefix)
	deploymentSegment = strings.TrimSuffix(deploymentSegment, azureChatCompletionsPathSuffix)
	deploymentSegment = strings.Trim(deploymentSegment, "/")
	if deploymentSegment == "" || strings.Contains(deploymentSegment, "/") {
		return "", false
	}

	deploymentName, err := url.PathUnescape(deploymentSegment)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(deploymentName) == "" {
		return "", false
	}

	return deploymentName, true
}

func (g *Gateway) isAuthorized(r *http.Request) bool {
	token := extractAPIToken(r)
	return token != "" && token == g.cfg.GatewayAPIKey
}

func extractAPIToken(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

var errBodyTooLarge = errors.New("request body exceeds MAX_BODY_BYTES")

func readRequestBody(r *http.Request, maxBodyBytes int64) ([]byte, error) {
	limited := io.LimitReader(r.Body, maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body")
	}
	if int64(len(body)) > maxBodyBytes {
		return nil, errBodyTooLarge
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("request body cannot be empty")
	}
	return body, nil
}

func normalizeChatCompletionRequest(body []byte, defaultModel string, deploymentHint string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON payload")
	}

	messages, ok := payload["messages"]
	if !ok {
		return nil, fmt.Errorf("missing required field: messages")
	}
	messagesSlice, ok := messages.([]any)
	if !ok || len(messagesSlice) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}

	modelValue := strings.TrimSpace(fmt.Sprint(payload["model"]))
	if modelValue == "<nil>" {
		modelValue = ""
	}

	if modelValue == "" {
		resolvedModel := strings.TrimSpace(deploymentHint)
		if resolvedModel == "" {
			resolvedModel = strings.TrimSpace(defaultModel)
		}
		if resolvedModel != "" {
			payload["model"] = resolvedModel
		}
	}

	normalizedBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request payload")
	}

	return normalizedBody, nil
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	response := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    code,
		},
	}

	_ = json.NewEncoder(w).Encode(response)
}

func copyResponseHeaders(dst http.Header, src http.Header) {
	for k, values := range src {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func isHopByHopHeader(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func streamCopy(w http.ResponseWriter, reader io.Reader) error {
	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
