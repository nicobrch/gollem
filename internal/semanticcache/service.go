package semanticcache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"gollem/internal/appconfig"
)

const (
	indexKeyPrefix = "semcache:index:"
	entryKeyPrefix = "semcache:entry:"
)

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

type PreparedLookup struct {
	ScopeKey       string
	QueryEmbedding []float64
}

type Service struct {
	cfg        appconfig.SemanticCacheConfig
	embedder   Embedder
	redis      *redis.Client
	nowFn      func() time.Time
	randReader io.Reader
}

type redisEntry struct {
	Embedding []float64       `json:"embedding"`
	Response  json.RawMessage `json:"response"`
}

func New(cfg appconfig.SemanticCacheConfig, azureCfg appconfig.AzureConfig, httpClient *http.Client) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		_ = redisClient.Close()
		return nil, fmt.Errorf("semantic cache redis unavailable: %w", err)
	}

	embedder, err := newAzureEmbedder(httpClient, azureCfg, cfg.AzureEmbeddingsDeployment)
	if err != nil {
		_ = redisClient.Close()
		return nil, err
	}

	return &Service{
		cfg:        cfg,
		embedder:   embedder,
		redis:      redisClient,
		nowFn:      time.Now,
		randReader: rand.Reader,
	}, nil
}

func (s *Service) Close() error {
	if s == nil || s.redis == nil {
		return nil
	}
	return s.redis.Close()
}

func (s *Service) MaxResponseBytes() int64 {
	if s == nil {
		return 0
	}
	return s.cfg.MaxResponseBytes
}

func (s *Service) Lookup(ctx context.Context, keyID string, requestBody []byte) ([]byte, *PreparedLookup, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, nil, nil
	}

	parsed, err := ParseRequest(requestBody)
	if err != nil || parsed.Stream {
		return nil, nil, nil
	}

	queryEmbedding, err := s.embedder.Embed(ctx, parsed.Query)
	if err != nil {
		return nil, nil, err
	}

	scopeKey := buildScopeKey(keyID, parsed.Model, parsed.ContextHash)
	cachedResponse, hit, err := s.findBestMatch(ctx, scopeKey, queryEmbedding)
	if err != nil {
		return nil, nil, err
	}

	prepared := &PreparedLookup{
		ScopeKey:       scopeKey,
		QueryEmbedding: queryEmbedding,
	}

	if hit {
		return cachedResponse, prepared, nil
	}

	return nil, prepared, nil
}

func (s *Service) StorePrepared(ctx context.Context, prepared *PreparedLookup, responseBody []byte, statusCode int, contentType string) error {
	if s == nil || prepared == nil {
		return nil
	}
	if statusCode != http.StatusOK {
		return nil
	}
	if int64(len(responseBody)) > s.cfg.MaxResponseBytes {
		return nil
	}
	if !isJSONContentType(contentType) {
		return nil
	}
	if !json.Valid(responseBody) {
		return nil
	}

	entryID, err := s.newEntryID()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(redisEntry{
		Embedding: prepared.QueryEmbedding,
		Response:  json.RawMessage(responseBody),
	})
	if err != nil {
		return fmt.Errorf("failed encoding semantic cache entry")
	}

	entryKey := entryKeyPrefix + entryID
	indexKey := indexKeyPrefix + prepared.ScopeKey
	score := float64(s.nowFn().UnixMilli())

	pipe := s.redis.TxPipeline()
	pipe.Set(ctx, entryKey, payload, s.cfg.TTL)
	pipe.ZAdd(ctx, indexKey, redis.Z{Score: score, Member: entryID})
	pipe.Expire(ctx, indexKey, s.cfg.TTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed writing semantic cache entry: %w", err)
	}

	if err := s.trimScope(ctx, indexKey); err != nil {
		return fmt.Errorf("failed trimming semantic cache scope: %w", err)
	}

	return nil
}

func (s *Service) findBestMatch(ctx context.Context, scopeKey string, queryEmbedding []float64) ([]byte, bool, error) {
	indexKey := indexKeyPrefix + scopeKey
	entryIDs, err := s.redis.ZRevRange(ctx, indexKey, 0, int64(s.cfg.MaxCandidates-1)).Result()
	if err != nil {
		return nil, false, err
	}
	if len(entryIDs) == 0 {
		return nil, false, nil
	}

	entryKeys := make([]string, 0, len(entryIDs))
	for _, entryID := range entryIDs {
		entryKeys = append(entryKeys, entryKeyPrefix+entryID)
	}

	values, err := s.redis.MGet(ctx, entryKeys...).Result()
	if err != nil {
		return nil, false, err
	}

	bestScore := -2.0
	var bestResponse []byte

	for _, value := range values {
		if value == nil {
			continue
		}

		var encoded []byte
		switch typed := value.(type) {
		case string:
			encoded = []byte(typed)
		case []byte:
			encoded = typed
		default:
			continue
		}

		if len(encoded) == 0 {
			continue
		}

		var entry redisEntry
		if err := json.Unmarshal(encoded, &entry); err != nil {
			continue
		}

		score := cosineSimilarity(queryEmbedding, entry.Embedding)
		if score > bestScore {
			bestScore = score
			bestResponse = append(bestResponse[:0], entry.Response...)
		}
	}

	if bestScore >= s.cfg.SimilarityThreshold && len(bestResponse) > 0 {
		return bestResponse, true, nil
	}

	return nil, false, nil
}

func (s *Service) trimScope(ctx context.Context, indexKey string) error {
	maxEntries := int64(s.cfg.MaxEntriesPerScope)
	if maxEntries <= 0 {
		return nil
	}

	count, err := s.redis.ZCard(ctx, indexKey).Result()
	if err != nil {
		return err
	}

	overflow := count - maxEntries
	if overflow <= 0 {
		return nil
	}

	staleEntryIDs, err := s.redis.ZRange(ctx, indexKey, 0, overflow-1).Result()
	if err != nil {
		return err
	}
	if len(staleEntryIDs) == 0 {
		return nil
	}

	members := make([]any, 0, len(staleEntryIDs))
	entryKeys := make([]string, 0, len(staleEntryIDs))
	for _, entryID := range staleEntryIDs {
		members = append(members, entryID)
		entryKeys = append(entryKeys, entryKeyPrefix+entryID)
	}

	pipe := s.redis.TxPipeline()
	pipe.ZRem(ctx, indexKey, members...)
	pipe.Del(ctx, entryKeys...)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *Service) newEntryID() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(s.randReader, b); err != nil {
		return "", fmt.Errorf("failed generating semantic cache entry ID: %w", err)
	}
	return fmt.Sprintf("%d-%s", s.nowFn().UnixNano(), hex.EncodeToString(b)), nil
}

func buildScopeKey(keyID, model, contextHash string) string {
	normalizedKeyID := strings.TrimSpace(keyID)
	if normalizedKeyID == "" {
		normalizedKeyID = "unknown"
	}

	material := normalizedKeyID + "|" + strings.TrimSpace(model) + "|" + strings.TrimSpace(contextHash)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func isJSONContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(contentType)), "application/json")
}
