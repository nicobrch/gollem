package gatewaykeys

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManager_CreateAuthenticateRevoke(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStore(filepath.Join(tempDir, "gateway_keys.json"))
	manager := NewManager(store)

	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	record, plainKey, err := manager.Create(CreateInput{
		ExpiresAt: &expiresAt,
		Metadata: map[string]string{
			"email":   "owner@example.com",
			"user_id": "u-123",
		},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if plainKey == "" {
		t.Fatalf("expected plaintext key")
	}
	if record.KeyHash == "" {
		t.Fatalf("expected hashed key")
	}
	if record.Status != StatusActive {
		t.Fatalf("expected status active, got %q", record.Status)
	}

	principal, ok, err := manager.Authenticate(plainKey)
	if err != nil {
		t.Fatalf("authenticate key: %v", err)
	}
	if !ok {
		t.Fatalf("expected key to authenticate")
	}
	if principal.KeyID != record.ID {
		t.Fatalf("expected key id %q, got %q", record.ID, principal.KeyID)
	}
	if principal.Metadata["email"] != "owner@example.com" {
		t.Fatalf("expected metadata to be present")
	}

	if _, err := manager.Revoke(record.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}

	_, ok, err = manager.Authenticate(plainKey)
	if err != nil {
		t.Fatalf("authenticate revoked key: %v", err)
	}
	if ok {
		t.Fatalf("expected revoked key to be rejected")
	}
}

func TestManager_PersistsRecords(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "gateway_keys.json")

	store := NewFileStore(filePath)
	manager := NewManager(store)
	record, _, err := manager.Create(CreateInput{Metadata: map[string]string{"name": "owner"}})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	reloaded := NewManager(NewFileStore(filePath))
	loadedRecord, err := reloaded.GetByID(record.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if loadedRecord.ID != record.ID {
		t.Fatalf("expected id %q, got %q", record.ID, loadedRecord.ID)
	}
	if loadedRecord.KeyHash == "" {
		t.Fatalf("expected key hash to persist")
	}
}
