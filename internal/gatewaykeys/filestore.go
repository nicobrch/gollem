package gatewaykeys

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const currentSnapshotVersion = 1

type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Create(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked()
	if err != nil {
		return err
	}

	for _, existing := range snapshot.Keys {
		if existing.ID == record.ID {
			return fmt.Errorf("key id already exists")
		}
		if existing.KeyHash == record.KeyHash {
			return fmt.Errorf("key hash already exists")
		}
	}

	snapshot.Keys = append(snapshot.Keys, record)
	return s.writeLocked(snapshot)
}

func (s *FileStore) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked()
	if err != nil {
		return nil, err
	}

	records := make([]Record, len(snapshot.Keys))
	copy(records, snapshot.Keys)
	return records, nil
}

func (s *FileStore) GetByID(id string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked()
	if err != nil {
		return Record{}, false, err
	}

	for _, record := range snapshot.Keys {
		if record.ID == id {
			return record, true, nil
		}
	}

	return Record{}, false, nil
}

func (s *FileStore) GetByHash(hash string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked()
	if err != nil {
		return Record{}, false, err
	}

	for _, record := range snapshot.Keys {
		if record.KeyHash == hash {
			return record, true, nil
		}
	}

	return Record{}, false, nil
}

func (s *FileStore) Update(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.readLocked()
	if err != nil {
		return err
	}

	updated := false
	for i, existing := range snapshot.Keys {
		if existing.ID == record.ID {
			snapshot.Keys[i] = record
			updated = true
			break
		}
	}
	if !updated {
		return ErrKeyNotFound
	}

	return s.writeLocked(snapshot)
}

func (s *FileStore) readLocked() (Snapshot, error) {
	if err := s.ensureParentDirLocked(); err != nil {
		return Snapshot{}, err
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{Version: currentSnapshotVersion, Keys: []Record{}}, nil
		}
		return Snapshot{}, fmt.Errorf("failed reading keys file: %w", err)
	}

	if len(data) == 0 {
		return Snapshot{Version: currentSnapshotVersion, Keys: []Record{}}, nil
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("failed parsing keys file JSON: %w", err)
	}
	if snapshot.Version == 0 {
		snapshot.Version = currentSnapshotVersion
	}
	if snapshot.Keys == nil {
		snapshot.Keys = []Record{}
	}

	return snapshot, nil
}

func (s *FileStore) writeLocked(snapshot Snapshot) error {
	if err := s.ensureParentDirLocked(); err != nil {
		return err
	}

	snapshot.Version = currentSnapshotVersion

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed encoding keys JSON: %w", err)
	}

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("failed writing temporary keys file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("failed replacing keys file: %w", err)
	}

	return nil
}

func (s *FileStore) ensureParentDirLocked() error {
	parentDir := filepath.Dir(s.path)
	if parentDir == "." || parentDir == "" {
		return nil
	}
	if err := os.MkdirAll(parentDir, 0o700); err != nil {
		return fmt.Errorf("failed creating key storage directory: %w", err)
	}
	return nil
}
