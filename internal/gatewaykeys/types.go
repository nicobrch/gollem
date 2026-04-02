package gatewaykeys

import "time"

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

type Record struct {
	ID        string            `json:"id"`
	KeyPrefix string            `json:"key_prefix"`
	KeyHash   string            `json:"key_hash"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Snapshot struct {
	Version int      `json:"version"`
	Keys    []Record `json:"keys"`
}

type Principal struct {
	KeyID    string
	Metadata map[string]string
}

type CreateInput struct {
	ExpiresAt *time.Time
	Metadata  map[string]string
}

type Store interface {
	Create(record Record) error
	List() ([]Record, error)
	GetByID(id string) (Record, bool, error)
	GetByHash(hash string) (Record, bool, error)
	Update(record Record) error
}
