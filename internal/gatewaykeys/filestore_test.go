package gatewaykeys

import (
	"path/filepath"
	"testing"
)

func TestFileStore_ReloadsAfterExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway_keys.json")

	writerStore := NewFileStore(path)
	readerStore := NewFileStore(path)

	writer := NewManager(writerStore)
	reader := NewManager(readerStore)

	_, plainOne, err := writer.Create(CreateInput{Metadata: map[string]string{"name": "one"}})
	if err != nil {
		t.Fatalf("create first key: %v", err)
	}

	if _, ok, err := reader.Authenticate(plainOne); err != nil {
		t.Fatalf("authenticate first key: %v", err)
	} else if !ok {
		t.Fatalf("expected first key to authenticate")
	}

	_, plainTwo, err := writer.Create(CreateInput{Metadata: map[string]string{"name": "two"}})
	if err != nil {
		t.Fatalf("create second key: %v", err)
	}

	if _, ok, err := reader.Authenticate(plainTwo); err != nil {
		t.Fatalf("authenticate second key after external write: %v", err)
	} else if !ok {
		t.Fatalf("expected second key to authenticate after reload")
	}
}
