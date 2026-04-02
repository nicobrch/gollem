package gatewaykeys

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

var ErrKeyNotFound = errors.New("gateway key not found")

type Manager struct {
	store  Store
	nowFn  func() time.Time
	randIO io.Reader
}

func NewManager(store Store) *Manager {
	return &Manager{
		store:  store,
		nowFn:  time.Now,
		randIO: rand.Reader,
	}
}

func (m *Manager) Create(input CreateInput) (Record, string, error) {
	now := m.nowFn().UTC()
	if input.ExpiresAt != nil && input.ExpiresAt.UTC().Before(now) {
		return Record{}, "", fmt.Errorf("expires_at must be in the future")
	}

	plainKey, err := m.generatePlainKey()
	if err != nil {
		return Record{}, "", err
	}

	id, err := m.generateID()
	if err != nil {
		return Record{}, "", err
	}

	keyHash := HashToken(plainKey)
	record := Record{
		ID:        id,
		KeyPrefix: keyPrefix(plainKey),
		KeyHash:   keyHash,
		CreatedAt: now,
		Status:    StatusActive,
		Metadata:  normalizeMetadata(input.Metadata),
	}
	if input.ExpiresAt != nil {
		expiresAt := input.ExpiresAt.UTC()
		record.ExpiresAt = &expiresAt
	}

	if err := m.store.Create(record); err != nil {
		return Record{}, "", err
	}

	return record, plainKey, nil
}

func (m *Manager) List() ([]Record, error) {
	records, err := m.store.List()
	if err != nil {
		return nil, err
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})

	return records, nil
}

func (m *Manager) GetByID(id string) (Record, error) {
	record, ok, err := m.store.GetByID(strings.TrimSpace(id))
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, ErrKeyNotFound
	}
	return record, nil
}

func (m *Manager) Revoke(id string) (Record, error) {
	record, ok, err := m.store.GetByID(strings.TrimSpace(id))
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, ErrKeyNotFound
	}
	if record.Status == StatusRevoked {
		return record, nil
	}

	record.Status = StatusRevoked
	if err := m.store.Update(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (m *Manager) Authenticate(plainKey string) (Principal, bool, error) {
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return Principal{}, false, nil
	}

	hash := HashToken(plainKey)
	record, ok, err := m.store.GetByHash(hash)
	if err != nil {
		return Principal{}, false, err
	}
	if !ok {
		return Principal{}, false, nil
	}
	if subtle.ConstantTimeCompare([]byte(record.KeyHash), []byte(hash)) != 1 {
		return Principal{}, false, nil
	}
	if record.Status != StatusActive {
		return Principal{}, false, nil
	}
	if record.ExpiresAt != nil && m.nowFn().UTC().After(record.ExpiresAt.UTC()) {
		return Principal{}, false, nil
	}

	return Principal{KeyID: record.ID, Metadata: normalizeMetadata(record.Metadata)}, true, nil
}

func HashToken(token string) string {
	hashed := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hashed[:])
}

func (m *Manager) generatePlainKey() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(m.randIO, b); err != nil {
		return "", fmt.Errorf("failed generating random key: %w", err)
	}
	return "gk_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *Manager) generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(m.randIO, b); err != nil {
		return "", fmt.Errorf("failed generating key id: %w", err)
	}
	return "key_" + hex.EncodeToString(b), nil
}

func keyPrefix(plain string) string {
	const maxPrefixLen = 14
	if len(plain) <= maxPrefixLen {
		return plain
	}
	return plain[:maxPrefixLen]
}

func normalizeMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(metadata))
	for key, value := range metadata {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		normalizedValue := strings.TrimSpace(value)
		if len(normalizedValue) > 512 {
			normalizedValue = normalizedValue[:512]
		}
		if len(normalizedKey) > 128 {
			normalizedKey = normalizedKey[:128]
		}
		result[normalizedKey] = normalizedValue
	}

	return result
}
