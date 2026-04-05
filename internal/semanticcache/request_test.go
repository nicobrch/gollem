package semanticcache

import "testing"

func TestParseRequest_ParsesCoreFields(t *testing.T) {
	body := []byte(`{"model":"gpt4o","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello world"}]}`)

	parsed, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("expected parse success, got error: %v", err)
	}
	if parsed.Model != "gpt4o" {
		t.Fatalf("expected model gpt4o, got %q", parsed.Model)
	}
	if parsed.Query != "hello world" {
		t.Fatalf("expected query hello world, got %q", parsed.Query)
	}
	if parsed.ContextHash == "" {
		t.Fatalf("expected non-empty context hash")
	}
	if parsed.Stream {
		t.Fatalf("expected stream false")
	}
}

func TestParseRequest_StreamBypass(t *testing.T) {
	body := []byte(`{"model":"gpt4o","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	parsed, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("expected parse success, got error: %v", err)
	}
	if !parsed.Stream {
		t.Fatalf("expected stream true")
	}
}

func TestParseRequest_ContextHashChangesWhenContextChanges(t *testing.T) {
	bodyA := []byte(`{"model":"gpt4o","temperature":0.1,"messages":[{"role":"system","content":"brief"},{"role":"user","content":"hello"}]}`)
	bodyB := []byte(`{"model":"gpt4o","temperature":0.9,"messages":[{"role":"system","content":"brief"},{"role":"user","content":"hello"}]}`)

	parsedA, err := ParseRequest(bodyA)
	if err != nil {
		t.Fatalf("expected parse success for bodyA, got error: %v", err)
	}
	parsedB, err := ParseRequest(bodyB)
	if err != nil {
		t.Fatalf("expected parse success for bodyB, got error: %v", err)
	}

	if parsedA.ContextHash == parsedB.ContextHash {
		t.Fatalf("expected different context hash when context changes")
	}
}
