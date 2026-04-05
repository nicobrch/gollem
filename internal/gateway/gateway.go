package gateway

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gollem/internal/gatewaykeys"
	"gollem/internal/providers"
)

const defaultUserAgent = "gollem/0.1"

const (
	azureChatCompletionsPathPrefix = "/openai/deployments/"
	azureChatCompletionsPathSuffix = "/chat/completions"
)

type Config struct {
	AdminAPIKey          string
	DefaultModel         string
	AzureDeployment      string
	MaxBodyBytes         int64
	MaxInFlight          int
	LogPromptSummaries   bool
	LogResponseSummaries bool
}

type Gateway struct {
	client   *http.Client
	provider providers.ChatProvider
	keys     *gatewaykeys.Manager
	cfg      Config
	inflight chan struct{}
}

func New(client *http.Client, provider providers.ChatProvider, keyManager *gatewaykeys.Manager, cfg Config) *Gateway {
	var inflight chan struct{}
	if cfg.MaxInFlight > 0 {
		inflight = make(chan struct{}, cfg.MaxInFlight)
	}

	return &Gateway{
		client:   client,
		provider: provider,
		keys:     keyManager,
		cfg:      cfg,
		inflight: inflight,
	}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.healthzHandler)
	mux.HandleFunc("/admin/keys", g.adminKeysHandler)
	mux.HandleFunc("/admin/keys/", g.adminKeyByIDHandler)
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
	if g.cfg.AzureDeployment != "" && deploymentName != g.cfg.AzureDeployment {
		writeOpenAIError(w, http.StatusBadRequest, "unsupported deployment for this gateway", "invalid_deployment")
		return
	}

	g.handleChatCompletions(w, r, deploymentName)
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request, deploymentHint string) {
	start := time.Now()
	requestID := requestIDFor(r)
	w.Header().Set("X-Request-Id", requestID)

	if r.Method != http.MethodPost {
		log.Printf("gateway request_id=%s route=%s key_id=unknown status=%d latency_ms=%d error=%q", requestID, r.URL.Path, http.StatusMethodNotAllowed, elapsedMillis(start), "method not allowed")
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}

	releaseSlot, acquired := g.tryAcquireInFlightSlot()
	if !acquired {
		w.Header().Set("Retry-After", "1")
		log.Printf("gateway request_id=%s route=%s key_id=unknown status=%d latency_ms=%d error=%q", requestID, r.URL.Path, http.StatusTooManyRequests, elapsedMillis(start), "max inflight requests exceeded")
		writeOpenAIError(w, http.StatusTooManyRequests, "gateway overloaded, retry later", "rate_limit_exceeded")
		return
	}
	defer releaseSlot()

	principal, ok, err := g.authenticateRequest(r)
	if err != nil {
		log.Printf("gateway request_id=%s route=%s key_id=unknown status=%d latency_ms=%d error=%q", requestID, r.URL.Path, http.StatusInternalServerError, elapsedMillis(start), "auth backend unavailable")
		writeOpenAIError(w, http.StatusInternalServerError, "authentication failed", "gateway_internal_error")
		return
	}
	if !ok {
		log.Printf("gateway request_id=%s route=%s key_id=unknown status=%d latency_ms=%d error=%q", requestID, r.URL.Path, http.StatusUnauthorized, elapsedMillis(start), "invalid gateway API key")
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
		log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s status=%d latency_ms=%d error=%q", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), status, elapsedMillis(start), err.Error())
		writeOpenAIError(w, status, err.Error(), code)
		return
	}

	normalizedBody, err := normalizeChatCompletionRequest(body, g.cfg.DefaultModel, deploymentHint)
	if err != nil {
		log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s status=%d latency_ms=%d error=%q", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), http.StatusBadRequest, elapsedMillis(start), err.Error())
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	requestSummary, targetModel := summarizeRequestPayload(normalizedBody, g.cfg.LogPromptSummaries)

	acceptHeader := strings.TrimSpace(r.Header.Get("Accept"))
	proxyReq, err := g.provider.NewChatCompletionsRequest(r.Context(), normalizedBody, acceptHeader, defaultUserAgent)
	if err != nil {
		log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s provider=%s model=%s request=%q status=%d latency_ms=%d error=%q", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), g.provider.Name(), targetModel, requestSummary, http.StatusInternalServerError, elapsedMillis(start), "failed to create upstream request")
		writeOpenAIError(w, http.StatusInternalServerError, "failed to create upstream request", "gateway_internal_error")
		return
	}
	proxyReq.Header.Set("X-Request-Id", requestID)

	resp, err := g.client.Do(proxyReq)
	if err != nil {
		log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s provider=%s model=%s request=%q status=%d latency_ms=%d error=%q upstream_err=%q", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), g.provider.Name(), targetModel, requestSummary, http.StatusBadGateway, elapsedMillis(start), "upstream request failed", err.Error())
		writeOpenAIError(w, http.StatusBadGateway, "upstream request failed", "upstream_unavailable")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var responseSummary string
	reader := io.Reader(resp.Body)
	var responseCapture *limitedBuffer
	if g.cfg.LogResponseSummaries {
		responseCapture = newLimitedBuffer(2048)
		reader = io.TeeReader(resp.Body, responseCapture)
	}

	if err := streamCopy(w, reader); err != nil {
		log.Printf("response streaming failed: %v", err)
	}
	if g.cfg.LogResponseSummaries {
		responseSummary = summarizeResponsePayload(responseCapture.Bytes())
	}

	if g.cfg.LogResponseSummaries {
		log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s provider=%s model=%s request=%q response=%q status=%d latency_ms=%d", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), g.provider.Name(), targetModel, requestSummary, responseSummary, resp.StatusCode, elapsedMillis(start))
		return
	}
	log.Printf("gateway request_id=%s route=%s key_id=%s owner=%s provider=%s model=%s request=%q status=%d latency_ms=%d", requestID, r.URL.Path, principal.KeyID, ownerHint(principal.Metadata), g.provider.Name(), targetModel, requestSummary, resp.StatusCode, elapsedMillis(start))
}

func (g *Gateway) tryAcquireInFlightSlot() (func(), bool) {
	if g.inflight == nil {
		return func() {}, true
	}

	select {
	case g.inflight <- struct{}{}:
		return func() {
			<-g.inflight
		}, true
	default:
		return nil, false
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

func (g *Gateway) authenticateRequest(r *http.Request) (gatewaykeys.Principal, bool, error) {
	token := extractAPIToken(r)
	if token == "" {
		return gatewaykeys.Principal{}, false, nil
	}

	if g.keys != nil {
		principal, ok, err := g.keys.Authenticate(token)
		if err != nil {
			return gatewaykeys.Principal{}, false, err
		}
		if ok {
			return principal, true, nil
		}
	}

	return gatewaykeys.Principal{}, false, nil
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

func (g *Gateway) adminKeysHandler(w http.ResponseWriter, r *http.Request) {
	if !g.isAdminAuthorized(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid admin API key", "invalid_admin_api_key")
		return
	}
	if g.keys == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "key manager not configured", "key_manager_unavailable")
		return
	}

	switch r.Method {
	case http.MethodPost:
		g.createKeyHandler(w, r)
	case http.MethodGet:
		g.listKeysHandler(w)
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
	}
}

func (g *Gateway) adminKeyByIDHandler(w http.ResponseWriter, r *http.Request) {
	if !g.isAdminAuthorized(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid admin API key", "invalid_admin_api_key")
		return
	}
	if g.keys == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "key manager not configured", "key_manager_unavailable")
		return
	}

	keyID, action := parseAdminKeyPath(r.URL.Path)
	if keyID == "" {
		writeOpenAIError(w, http.StatusNotFound, "unsupported path", "not_found")
		return
	}

	if r.Method == http.MethodGet && action == "" {
		g.getKeyHandler(w, keyID)
		return
	}
	if r.Method == http.MethodPost && action == "revoke" {
		g.revokeKeyHandler(w, keyID)
		return
	}

	writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
}

type createKeyRequest struct {
	ExpiresAt *time.Time        `json:"expires_at"`
	Metadata  map[string]string `json:"metadata"`
}

type keyResponse struct {
	ID        string            `json:"id"`
	KeyPrefix string            `json:"key_prefix"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata"`
}

type keyCreateResponse struct {
	Key string `json:"key"`
	keyResponse
}

func (g *Gateway) createKeyHandler(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if r.Body != nil {
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid JSON payload", "invalid_request_error")
			return
		}
	}

	record, plain, err := g.keys.Create(gatewaykeys.CreateInput{ExpiresAt: req.ExpiresAt, Metadata: req.Metadata})
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	writeJSON(w, http.StatusCreated, keyCreateResponse{
		Key:         plain,
		keyResponse: toKeyResponse(record),
	})
}

func (g *Gateway) listKeysHandler(w http.ResponseWriter) {
	records, err := g.keys.List()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed listing keys", "gateway_internal_error")
		return
	}

	items := make([]keyResponse, 0, len(records))
	for _, record := range records {
		items = append(items, toKeyResponse(record))
	}

	writeJSON(w, http.StatusOK, map[string]any{"keys": items})
}

func (g *Gateway) getKeyHandler(w http.ResponseWriter, keyID string) {
	record, err := g.keys.GetByID(keyID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "gateway_internal_error"
		message := "failed loading key"
		if errors.Is(err, gatewaykeys.ErrKeyNotFound) {
			status = http.StatusNotFound
			code = "not_found"
			message = "key not found"
		}
		writeOpenAIError(w, status, message, code)
		return
	}

	writeJSON(w, http.StatusOK, toKeyResponse(record))
}

func (g *Gateway) revokeKeyHandler(w http.ResponseWriter, keyID string) {
	record, err := g.keys.Revoke(keyID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "gateway_internal_error"
		message := "failed revoking key"
		if errors.Is(err, gatewaykeys.ErrKeyNotFound) {
			status = http.StatusNotFound
			code = "not_found"
			message = "key not found"
		}
		writeOpenAIError(w, status, message, code)
		return
	}

	writeJSON(w, http.StatusOK, toKeyResponse(record))
}

func (g *Gateway) isAdminAuthorized(r *http.Request) bool {
	if g.cfg.AdminAPIKey == "" {
		return false
	}
	token := extractAPIToken(r)
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(g.cfg.AdminAPIKey)) == 1
}

func parseAdminKeyPath(path string) (string, string) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 3 || parts[0] != "admin" || parts[1] != "keys" {
		return "", ""
	}
	if parts[2] == "" {
		return "", ""
	}
	if len(parts) == 3 {
		return parts[2], ""
	}
	if len(parts) == 4 {
		return parts[2], parts[3]
	}
	return "", ""
}

func toKeyResponse(record gatewaykeys.Record) keyResponse {
	return keyResponse{
		ID:        record.ID,
		KeyPrefix: record.KeyPrefix,
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
		Status:    record.Status,
		Metadata:  record.Metadata,
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func requestIDFor(r *http.Request) string {
	if requestID := strings.TrimSpace(r.Header.Get("X-Request-Id")); requestID != "" {
		if len(requestID) > 128 {
			return requestID[:128]
		}
		return requestID
	}

	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err == nil {
		return hexEncode(b)
	}

	return "req-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func ownerHint(metadata map[string]string) string {
	if len(metadata) == 0 {
		return "unknown"
	}
	for _, key := range []string{"email", "user_id", "name"} {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			return value
		}
	}
	return "custom"
}

func summarizeRequestPayload(body []byte, includePrompt bool) (string, string) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		if includePrompt {
			return truncateForLog(string(body), 180), ""
		}
		return "invalid_json", ""
	}

	model := strings.TrimSpace(fmt.Sprint(payload["model"]))
	if model == "<nil>" {
		model = ""
	}

	messagesAny, _ := payload["messages"].([]any)
	messageCount := len(messagesAny)
	if !includePrompt {
		return fmt.Sprintf("messages=%d", messageCount), model
	}

	promptPreview := ""
	for i := len(messagesAny) - 1; i >= 0; i-- {
		messageMap, ok := messagesAny[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(fmt.Sprint(messageMap["role"]))
		if role != "user" {
			continue
		}
		promptPreview = extractMessageContent(messageMap["content"])
		break
	}

	summary := fmt.Sprintf("messages=%d prompt=%q", messageCount, truncateForLog(promptPreview, 180))
	return summary, model
}

func summarizeResponsePayload(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return truncateForLog(string(trimmed), 180)
	}

	choicesAny, _ := payload["choices"].([]any)
	if len(choicesAny) > 0 {
		if firstChoice, ok := choicesAny[0].(map[string]any); ok {
			if message, ok := firstChoice["message"].(map[string]any); ok {
				content := extractMessageContent(message["content"])
				if content != "" {
					return truncateForLog(content, 180)
				}
			}
		}
	}

	if errValue, ok := payload["error"]; ok {
		return truncateForLog(fmt.Sprint(errValue), 180)
	}

	return truncateForLog(string(trimmed), 180)
}

func extractMessageContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if itemMap, ok := item.(map[string]any); ok {
				text := strings.TrimSpace(fmt.Sprint(itemMap["text"]))
				if text != "" && text != "<nil>" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return strings.TrimSpace(fmt.Sprint(content))
	}
}

func truncateForLog(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= max {
		return trimmed
	}
	if max <= 3 {
		return trimmed[:max]
	}
	return trimmed[:max-3] + "..."
}

func elapsedMillis(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

type limitedBuffer struct {
	max int
	buf bytes.Buffer
}

func newLimitedBuffer(max int) *limitedBuffer {
	return &limitedBuffer{max: max}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	if b.max <= 0 {
		return originalLen, nil
	}

	remaining := b.max - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}

	return originalLen, nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hextable[v>>4]
		out[i*2+1] = hextable[v&0x0f]
	}
	return string(out)
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
