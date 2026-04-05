package gatewaykeys

import (
	"testing"

	"github.com/lib/pq"
)

func TestNewPostgresStore_RequiresDSN(t *testing.T) {
	_, err := NewPostgresStore("")
	if err == nil {
		t.Fatalf("expected error when DSN is empty")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	err := &pq.Error{Code: "23505", Constraint: "gateway_keys_key_hash_key"}
	if !isUniqueViolation(err, "gateway_keys_key_hash_key") {
		t.Fatalf("expected unique violation to match constraint")
	}
	if isUniqueViolation(err, "other") {
		t.Fatalf("expected constraint mismatch to return false")
	}
	if isUniqueViolation(&pq.Error{Code: "22001"}, "") {
		t.Fatalf("expected non-unique code to return false")
	}
}
