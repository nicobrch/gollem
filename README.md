# gollem

Minimal, fast Go AI gateway.

This version supports:

- OpenAI-compatible endpoints:
  - `POST /v1/chat/completions`
  - `POST /chat/completions`
- Azure-compatible endpoint:
  - `POST /openai/deployments/{deployment}/chat/completions` (optional `api-version` query is accepted and ignored by the gateway)
- OpenAI-compatible Chat Completions request format
- Gateway-issued API key auth for your clients
- Admin key management endpoints backed by file or PostgreSQL persistence
- Forwards to Azure AI Foundry (Azure OpenAI) chat completions deployment endpoint
- Live request/response logging in terminal with redaction-safe summaries

## Quick start

1. Set env vars:

```bash
export GATEWAY_ADMIN_API_KEY="gw-admin-dev-key"
export GATEWAY_KEYS_BACKEND="file"
export GATEWAY_KEYS_FILE="./data/gateway_keys.json"
export AZURE_OPENAI_API_KEY="..."
export AZURE_OPENAI_BASE_URL="https://<your-account>.openai.azure.com"
export AZURE_OPENAI_DEPLOYMENT="gpt4o"
export PORT="8000"
```

2. Run:

```bash
go run .
```

3. Create a gateway API key (one-time return of plaintext key):

```bash
curl -sS http://localhost:8000/admin/keys \
  -H "Authorization: Bearer gw-admin-dev-key" \
  -H "Content-Type: application/json" \
  -d '{
    "metadata": {
      "email": "dev@example.com",
      "user_id": "u-123",
      "name": "Dev User"
    }
  }'
```

4. Call the gateway with the issued key:

```bash
curl -sS http://localhost:8000/v1/chat/completions \
  -H "Authorization: Bearer <issued-gateway-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt4o",
    "messages": [
      {"role": "user", "content": "Say hello in one sentence."}
    ]
  }'
```

## Config

- `GATEWAY_ADMIN_API_KEY` (required): admin key for key management endpoints.
- `GATEWAY_KEYS_BACKEND` (optional, default `file`): key storage backend (`file` or `postgres`).
- `GATEWAY_KEYS_FILE` (optional, default `./data/gateway_keys.json`): used when `GATEWAY_KEYS_BACKEND=file`.
- `GATEWAY_KEYS_POSTGRES_DSN` (required when `GATEWAY_KEYS_BACKEND=postgres`): PostgreSQL DSN, e.g. `postgres://gollem:gollem@postgres:5432/gollem?sslmode=disable`.
- `AZURE_OPENAI_API_KEY` (required): Azure OpenAI key.
- `AZURE_OPENAI_BASE_URL` (required): Foundry endpoint base URL, e.g. `https://<account>.openai.azure.com`.
- `AZURE_OPENAI_DEPLOYMENT` (required): deployment name created in Azure Foundry (for this repo, from Terraform output `openai_deployment_name`, default `gpt4o`).
- `AZURE_OPENAI_API_VERSION` (optional, default `2024-10-21`): Azure OpenAI API version query parameter.
- `PORT` (optional, default `8000`): server port.
- `LISTEN_ADDR` (optional): full listen address, overrides `PORT` behavior.
- `AZURE_OPENAI_CHAT_COMPLETIONS_URL` (optional): full override for upstream URL.
- `DEFAULT_MODEL` (optional): used if request does not include `model`.
- `REQUEST_TIMEOUT_SECONDS` (optional, default `60`).
- `MAX_BODY_BYTES` (optional, default `1048576`).
- `MAX_INFLIGHT_REQUESTS` (optional, default `0`): max concurrent in-flight chat requests accepted by gateway. `0` disables the limit.
- `LOG_PROMPT_SUMMARIES` (optional, default `false`): include prompt previews in terminal logs.
- `LOG_RESPONSE_SUMMARIES` (optional, default `false`): include response previews in terminal logs.

## Key management API

- `POST /admin/keys`: create key. Returns plaintext key once, plus metadata and key id.
- `GET /admin/keys`: list key records.
- `GET /admin/keys/{id}`: lookup key metadata/status.
- `POST /admin/keys/{id}/revoke`: revoke key.

All `/admin/*` endpoints require:

- `Authorization: Bearer <GATEWAY_ADMIN_API_KEY>` or `X-API-Key: <GATEWAY_ADMIN_API_KEY>`

Stored key record fields include:

- `id`
- `key_prefix`
- `key_hash` (stored in JSON, not returned as plaintext key)
- `created_at`
- `expires_at` (optional)
- `status` (`active` or `revoked`)
- `metadata` (e.g. `email`, `user_id`, `name`, custom labels)

## Notes

- Client auth supports `Authorization: Bearer <gateway-key>` and `X-API-Key`.
- Gateway validates managed keys by hash, checks `status`, and enforces optional `expires_at`.
- If `model` is missing and you call `/openai/deployments/{deployment}/chat/completions`, the gateway uses `{deployment}` as `model`.
- Azure-compatible deployment paths are restricted to the configured deployment (`AZURE_OPENAI_DEPLOYMENT`) to avoid ambiguous routing.
- `/healthz` is available for health checks.
- Streaming responses are forwarded to the client.
- The server performs graceful shutdown on `SIGINT`/`SIGTERM`.

## Docker Compose (gateway + PostgreSQL)

Run both services with one command:

```bash
cp .env.docker.example .env.docker
docker compose up --build
```

Or use Make targets:

```bash
make up
```

The gateway starts on `http://localhost:8000` and stores key metadata in PostgreSQL.

You can still run file-backed mode locally with `go run .` and `GATEWAY_KEYS_BACKEND=file`.

Create a managed key and call the gateway in Docker mode:

```bash
curl -sS http://localhost:8000/admin/keys \
  -H "Authorization: Bearer gw-admin-dev-key" \
  -H "Content-Type: application/json" \
  -d '{"metadata":{"email":"dev@example.com"}}'
```

Useful commands:

```bash
make ps
make logs
make logs-gateway
make down
```

## Live logging

For chat requests, terminal logs always include:

- timestamp
- request id
- key id and owner hint from metadata (if available)
- route, provider, and model
- status code and latency

Optional logging controls:

- Set `LOG_PROMPT_SUMMARIES=true` to include request prompt summary.
- Set `LOG_RESPONSE_SUMMARIES=true` to include response summary.

Sensitive values like API keys and auth headers are not logged.

## Benchmark gateway overhead

You can benchmark latency/throughput overhead added by gollem compared to direct provider calls.

1. Start gollem gateway:

```bash
go run .
```

2. Install benchmark dependencies:

```bash
python -m pip install -r scripts/requirements-benchmark.txt
```

3. Set benchmark env vars:

```bash
export GATEWAY_URL="http://localhost:8000/v1/chat/completions"
export GATEWAY_API_KEY="<gateway-client-key>"

export PROVIDER_URL="https://<your-account>.openai.azure.com/openai/deployments/<deployment>/chat/completions?api-version=2024-10-21"
export PROVIDER_API_KEY="<azure-key>"
export PROVIDER_API_KEY_HEADER="api-key"
export PROVIDER_API_KEY_PREFIX=""

export BENCHMARK_MODEL="<deployment>"
```

4. Run the benchmark (LiteLLM-style load profile):

```bash
python scripts/benchmark_gateway_vs_provider.py --requests 2000 --max-concurrent 200 --runs 3
```

The script prints:

- Gateway vs direct throughput delta (`req/s`)
- Latency overhead for `mean`, `p50`, `p95`, and `p99` in milliseconds
- Success/failure rates, status-code distribution, and top error messages

Notes:

- Default execution mode is sequential (gateway then direct), which gives cleaner comparison numbers.
- For OpenAI-style direct calls, set `PROVIDER_API_KEY_HEADER="Authorization"` and `PROVIDER_API_KEY_PREFIX="Bearer"`.
- If you see provider throttling/timeouts under load, lower `--max-concurrent` and/or set `MAX_INFLIGHT_REQUESTS` (for example `10-50`) to protect upstream.

## Security

- Keep `.env` out of source control (already ignored).
- Never commit real keys in docs/examples.
- Rotate any key immediately if it is ever exposed.
