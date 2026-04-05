package gatewaykeys

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const currentSnapshotVersion = 1

type FileStore struct {
	path      string
	mu        sync.Mutex
	loaded    bool
	snapshot  Snapshot
	idIndex   map[string]int
	hashIndex map[string]int
	lastMod   time.Time
	lastSize  int64
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Create(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}

	if _, exists := s.idIndex[record.ID]; exists {
		return fmt.Errorf("key id already exists")
	}
	if _, exists := s.hashIndex[record.KeyHash]; exists {
		return fmt.Errorf("key hash already exists")
	}

	s.snapshot.Keys = append(s.snapshot.Keys, cloneRecord(record))
	idx := len(s.snapshot.Keys) - 1
	s.idIndex[record.ID] = idx
	s.hashIndex[record.KeyHash] = idx

	if err := s.writeLocked(); err != nil {
		s.snapshot.Keys = s.snapshot.Keys[:idx]
		delete(s.idIndex, record.ID)
		delete(s.hashIndex, record.KeyHash)
		return err
	}

	return nil
}

func (s *FileStore) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}

	return cloneRecords(s.snapshot.Keys), nil
}

func (s *FileStore) GetByID(id string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return Record{}, false, err
	}

	idx, ok := s.idIndex[id]
	if !ok {
		return Record{}, false, nil
	}

	return cloneRecord(s.snapshot.Keys[idx]), true, nil
}

func (s *FileStore) GetByHash(hash string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return Record{}, false, err
	}

	idx, ok := s.hashIndex[hash]
	if !ok {
		return Record{}, false, nil
	}

	return cloneRecord(s.snapshot.Keys[idx]), true, nil
}

func (s *FileStore) Update(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoadedLocked(); err != nil {
		return err
	}

	idx, ok := s.idIndex[record.ID]
	if !ok {
		return ErrKeyNotFound
	}

	if existingIdx, exists := s.hashIndex[record.KeyHash]; exists && existingIdx != idx {
		return fmt.Errorf("key hash already exists")
	}

	oldRecord := s.snapshot.Keys[idx]
	s.snapshot.Keys[idx] = cloneRecord(record)

	if oldRecord.KeyHash != record.KeyHash {
		delete(s.hashIndex, oldRecord.KeyHash)
		s.hashIndex[record.KeyHash] = idx
	}

	if err := s.writeLocked(); err != nil {
		s.snapshot.Keys[idx] = oldRecord
		if oldRecord.KeyHash != record.KeyHash {
			delete(s.hashIndex, record.KeyHash)
			s.hashIndex[oldRecord.KeyHash] = idx
		}
		return err
	}

	return nil
}

func (s *FileStore) ensureLoadedLocked() error {
	if err := s.ensureParentDirLocked(); err != nil {
		return err
	}

	fileInfo, statErr := os.Stat(s.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			if !s.loaded || len(s.snapshot.Keys) > 0 {
				s.snapshot = Snapshot{Version: currentSnapshotVersion, Keys: []Record{}}
				if err := s.rebuildIndexesLocked(); err != nil {
					return err
				}
				s.loaded = true
				s.lastMod = time.Time{}
				s.lastSize = 0
			}
			return nil
		}
		return fmt.Errorf("failed reading keys file: %w", statErr)
	}

	if s.loaded && !s.fileChangedLocked(fileInfo) {
		return nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("failed reading keys file: %w", err)
	}

	if len(data) == 0 {
		s.snapshot = Snapshot{Version: currentSnapshotVersion, Keys: []Record{}}
		if err := s.rebuildIndexesLocked(); err != nil {
			return err
		}
		s.loaded = true
		s.setFileInfoLocked(fileInfo)
		return nil
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("failed parsing keys file JSON: %w", err)
	}
	if snapshot.Version == 0 {
		snapshot.Version = currentSnapshotVersion
	}
	if snapshot.Keys == nil {
		snapshot.Keys = []Record{}
	}

	s.snapshot = cloneSnapshot(snapshot)
	if err := s.rebuildIndexesLocked(); err != nil {
		return err
	}
	s.loaded = true
	s.setFileInfoLocked(fileInfo)

	return nil
}

func (s *FileStore) writeLocked() error {
	if err := s.ensureParentDirLocked(); err != nil {
		return err
	}

	snapshot := cloneSnapshot(s.snapshot)
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

	if fileInfo, err := os.Stat(s.path); err == nil {
		s.setFileInfoLocked(fileInfo)
	}

	return nil
}

func (s *FileStore) fileChangedLocked(fileInfo os.FileInfo) bool {
	if s.lastSize != fileInfo.Size() {
		return true
	}
	if !s.lastMod.Equal(fileInfo.ModTime()) {
		return true
	}
	return false
}

func (s *FileStore) setFileInfoLocked(fileInfo os.FileInfo) {
	s.lastSize = fileInfo.Size()
	s.lastMod = fileInfo.ModTime()
}

func (s *FileStore) rebuildIndexesLocked() error {
	s.idIndex = make(map[string]int, len(s.snapshot.Keys))
	s.hashIndex = make(map[string]int, len(s.snapshot.Keys))

	for idx, record := range s.snapshot.Keys {
		if _, exists := s.idIndex[record.ID]; exists {
			return fmt.Errorf("duplicate key id found in keys file")
		}
		if _, exists := s.hashIndex[record.KeyHash]; exists {
			return fmt.Errorf("duplicate key hash found in keys file")
		}
		s.idIndex[record.ID] = idx
		s.hashIndex[record.KeyHash] = idx
	}

	return nil
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	return Snapshot{
		Version: snapshot.Version,
		Keys:    cloneRecords(snapshot.Keys),
	}
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, len(records))
	for i := range records {
		cloned[i] = cloneRecord(records[i])
	}
	return cloned
}

func cloneRecord(record Record) Record {
	cloned := record
	if record.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(record.Metadata))
		for key, value := range record.Metadata {
			cloned.Metadata[key] = value
		}
	}
	if record.ExpiresAt != nil {
		expiresAt := *record.ExpiresAt
		cloned.ExpiresAt = &expiresAt
	}
	return cloned
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
