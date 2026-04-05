package gatewaykeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

const dbOperationTimeout = 5 * time.Second

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(dsn string) (*PostgresStore, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN cannot be empty")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed opening postgres connection: %w", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed pinging postgres: %w", err)
	}

	if err := ensurePostgresSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresStore) Create(record Record) error {
	metadataJSON, err := marshalMetadata(record.Metadata)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO gateway_keys (id, key_prefix, key_hash, created_at, expires_at, status, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`,
		record.ID,
		record.KeyPrefix,
		record.KeyHash,
		record.CreatedAt.UTC(),
		record.ExpiresAt,
		record.Status,
		string(metadataJSON),
	)
	if err != nil {
		if isUniqueViolation(err, "gateway_keys_pkey") {
			return fmt.Errorf("key id already exists")
		}
		if isUniqueViolation(err, "gateway_keys_key_hash_key") {
			return fmt.Errorf("key hash already exists")
		}
		return fmt.Errorf("failed creating key: %w", err)
	}

	return nil
}

func (s *PostgresStore) List() ([]Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `SELECT id, key_prefix, key_hash, created_at, expires_at, status, metadata FROM gateway_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed listing keys: %w", err)
	}
	defer rows.Close()

	records := make([]Record, 0)
	for rows.Next() {
		record, scanErr := scanRecord(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading listed keys: %w", err)
	}

	return records, nil
}

func (s *PostgresStore) GetByID(id string) (Record, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `SELECT id, key_prefix, key_hash, created_at, expires_at, status, metadata FROM gateway_keys WHERE id = $1`, id)
	record, err := scanRecord(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}

	return record, true, nil
}

func (s *PostgresStore) GetByHash(hash string) (Record, bool, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return Record{}, false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `SELECT id, key_prefix, key_hash, created_at, expires_at, status, metadata FROM gateway_keys WHERE key_hash = $1`, hash)
	record, err := scanRecord(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}

	return record, true, nil
}

func (s *PostgresStore) Update(record Record) error {
	metadataJSON, err := marshalMetadata(record.Metadata)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
	defer cancel()

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE gateway_keys
		 SET key_prefix = $2,
		     key_hash = $3,
		     created_at = $4,
		     expires_at = $5,
		     status = $6,
		     metadata = $7::jsonb
		 WHERE id = $1`,
		record.ID,
		record.KeyPrefix,
		record.KeyHash,
		record.CreatedAt.UTC(),
		record.ExpiresAt,
		record.Status,
		string(metadataJSON),
	)
	if err != nil {
		if isUniqueViolation(err, "gateway_keys_key_hash_key") {
			return fmt.Errorf("key hash already exists")
		}
		return fmt.Errorf("failed updating key: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed checking update result: %w", err)
	}
	if rowsAffected == 0 {
		return ErrKeyNotFound
	}

	return nil
}

func ensurePostgresSchema(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS gateway_keys (
  id TEXT PRIMARY KEY,
  key_prefix TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NULL,
  status TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_gateway_keys_created_at ON gateway_keys (created_at DESC);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed ensuring postgres schema: %w", err)
	}

	return nil
}

func marshalMetadata(metadata map[string]string) ([]byte, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}

	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed encoding metadata: %w", err)
	}

	return encoded, nil
}

type scanFunc func(dest ...any) error

func scanRecord(scan scanFunc) (Record, error) {
	var record Record
	var expiresAt sql.NullTime
	var metadataJSON []byte

	err := scan(
		&record.ID,
		&record.KeyPrefix,
		&record.KeyHash,
		&record.CreatedAt,
		&expiresAt,
		&record.Status,
		&metadataJSON,
	)
	if err != nil {
		return Record{}, err
	}

	record.CreatedAt = record.CreatedAt.UTC()
	if expiresAt.Valid {
		expires := expiresAt.Time.UTC()
		record.ExpiresAt = &expires
	}

	if len(metadataJSON) == 0 {
		record.Metadata = map[string]string{}
		return record, nil
	}

	if err := json.Unmarshal(metadataJSON, &record.Metadata); err != nil {
		return Record{}, fmt.Errorf("failed decoding metadata: %w", err)
	}
	if record.Metadata == nil {
		record.Metadata = map[string]string{}
	}

	return record, nil
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pq.Error
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "23505" {
		return false
	}
	if constraint == "" {
		return true
	}
	return pgErr.Constraint == constraint
}
