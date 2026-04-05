# Prioritized Implementation Plan

This plan sequences the highest-value hardening work for gollem and tracks status.

## P0 - Security and correctness (must-have)

1. Separate admin and client credentials
   - Require `GATEWAY_ADMIN_API_KEY`; remove legacy fallback behavior.
   - Status: completed.

2. Clarify Azure-compatible deployment routing behavior
   - Reject `/openai/deployments/{deployment}/chat/completions` requests when `{deployment}` does not match configured `AZURE_OPENAI_DEPLOYMENT`.
   - Status: completed.

3. Reduce sensitive logging surface by default
   - Disable prompt and response previews by default.
   - Add opt-in controls: `LOG_PROMPT_SUMMARIES`, `LOG_RESPONSE_SUMMARIES`.
   - Status: completed.

## P1 - Runtime resilience and repo hygiene (should-have)

4. Add graceful shutdown
   - Handle `SIGINT`/`SIGTERM` and drain in-flight requests with timeout.
   - Status: completed.

5. Prevent tracked build artifacts in source control
   - Ignore local binaries (`go-llm`, `gollem`).
   - Add CI guard that fails if these artifacts are tracked.
   - Status: completed.

## P2 - Scale path for key storage

6. Improve key lookup performance and consistency
   - Replace O(n) per-request scans with indexed lookups in memory.
   - Detect and reload when backing file changes.
   - Status: completed for file-backed mode.

7. Migrate to shared storage for multi-instance deployments
   - Introduce PostgreSQL-backed `gatewaykeys.Store` implementation.
   - Add containerized local stack for gateway + PostgreSQL.
   - Status: completed.

## Validation checklist

- `go vet ./...`
- `go test ./...`
