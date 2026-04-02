package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort                 = "8000"
	defaultAzureAPIVersion      = "2024-10-21"
	defaultRequestTimeout       = 60 * time.Second
	defaultMaxBodyBytes         = int64(1 << 20) // 1 MiB
	defaultAzureCompletionsPath = "/openai/deployments/%s/chat/completions"
)

type config struct {
	listenAddr     string
	gatewayAPIKey  string
	azureAPIKey    string
	upstreamURL    string
	defaultModel   string
	requestTimeout time.Duration
	maxBodyBytes   int64
}

type gateway struct {
	client *http.Client
	cfg    config
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	g := &gateway{
		client: newHTTPClient(cfg.requestTimeout),
		cfg:    cfg,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.healthzHandler)
	mux.HandleFunc("/llm", g.llmHandler)

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("go-llm listening on %s", cfg.listenAddr)
	log.Printf("azure upstream: %s", cfg.upstreamURL)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func loadConfig() (config, error) {
	port := envOrDefault("PORT", defaultPort)
	listenAddr := envOrDefault("LISTEN_ADDR", ":"+port)
	gatewayAPIKey := strings.TrimSpace(os.Getenv("GATEWAY_API_KEY"))
	if gatewayAPIKey == "" {
		return config{}, fmt.Errorf("GATEWAY_API_KEY is required")
	}

	azureAPIKey := strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_KEY"))
	if azureAPIKey == "" {
		azureAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if azureAPIKey == "" {
		return config{}, fmt.Errorf("AZURE_OPENAI_API_KEY (or OPENAI_API_KEY fallback) is required")
	}

	azureBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AZURE_OPENAI_BASE_URL")), "/")
	if azureBaseURL == "" {
		azureBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	}
	if azureBaseURL == "" {
		return config{}, fmt.Errorf("AZURE_OPENAI_BASE_URL is required")
	}

	deploymentName := strings.TrimSpace(os.Getenv("AZURE_OPENAI_DEPLOYMENT"))
	if deploymentName == "" {
		deploymentName = strings.TrimSpace(os.Getenv("DEFAULT_MODEL"))
	}
	if deploymentName == "" {
		return config{}, fmt.Errorf("AZURE_OPENAI_DEPLOYMENT is required (or DEFAULT_MODEL fallback)")
	}

	apiVersion := strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_VERSION"))
	if apiVersion == "" {
		apiVersion = defaultAzureAPIVersion
	}

	base, err := url.Parse(azureBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return config{}, fmt.Errorf("AZURE_OPENAI_BASE_URL must be a valid absolute URL")
	}

	path := strings.TrimRight(base.Path, "/") + fmt.Sprintf(defaultAzureCompletionsPath, url.PathEscape(deploymentName))
	base.Path = path
	query := base.Query()
	query.Set("api-version", apiVersion)
	base.RawQuery = query.Encode()

	upstreamURL := base.String()
	if customURL := strings.TrimSpace(os.Getenv("AZURE_OPENAI_CHAT_COMPLETIONS_URL")); customURL != "" {
		upstreamURL = customURL
	}

	requestTimeout := defaultRequestTimeout
	if v := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT_SECONDS")); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil || seconds <= 0 {
			return config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be a positive integer")
		}
		requestTimeout = time.Duration(seconds) * time.Second
	}

	maxBodyBytes := defaultMaxBodyBytes
	if v := strings.TrimSpace(os.Getenv("MAX_BODY_BYTES")); v != "" {
		size, err := strconv.ParseInt(v, 10, 64)
		if err != nil || size <= 0 {
			return config{}, fmt.Errorf("MAX_BODY_BYTES must be a positive integer")
		}
		maxBodyBytes = size
	}

	defaultModel := strings.TrimSpace(os.Getenv("DEFAULT_MODEL"))
	if defaultModel == "" {
		defaultModel = deploymentName
	}

	return config{
		listenAddr:     listenAddr,
		gatewayAPIKey:  gatewayAPIKey,
		azureAPIKey:    azureAPIKey,
		upstreamURL:    upstreamURL,
		defaultModel:   defaultModel,
		requestTimeout: requestTimeout,
		maxBodyBytes:   maxBodyBytes,
	}, nil
}

func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

func (g *gateway) healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (g *gateway) llmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}

	if !g.isAuthorized(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid gateway API key", "invalid_api_key")
		return
	}

	body, err := readRequestBody(r, g.cfg.maxBodyBytes)
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

	normalizedBody, err := normalizeChatCompletionRequest(body, g.cfg.defaultModel)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.cfg.upstreamURL, bytes.NewReader(normalizedBody))
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to create upstream request", "gateway_internal_error")
		return
	}

	proxyReq.Header.Set("api-key", g.cfg.azureAPIKey)
	proxyReq.Header.Set("Content-Type", "application/json")
	if accept := strings.TrimSpace(r.Header.Get("Accept")); accept != "" {
		proxyReq.Header.Set("Accept", accept)
	}
	proxyReq.Header.Set("User-Agent", "go-llm/0.1")

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

func (g *gateway) isAuthorized(r *http.Request) bool {
	token := extractAPIToken(r)
	return token != "" && token == g.cfg.gatewayAPIKey
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

func normalizeChatCompletionRequest(body []byte, defaultModel string) ([]byte, error) {
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

	if _, hasModel := payload["model"]; !hasModel || strings.TrimSpace(fmt.Sprint(payload["model"])) == "" {
		if defaultModel != "" {
			payload["model"] = defaultModel
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

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
