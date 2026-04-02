# go-llm

Minimal, fast Go AI gateway.

This first version is intentionally simple:

- One endpoint: `POST /llm`
- OpenAI-compatible Chat Completions request format
- Gateway API key auth for your clients
- Forwards to Azure AI Foundry (Azure OpenAI) chat completions deployment endpoint

## Quick start

1. Set env vars:

```bash
export GATEWAY_API_KEY="gw-dev-key"
export AZURE_OPENAI_API_KEY="..."
export AZURE_OPENAI_BASE_URL="https://<your-account>.openai.azure.com"
export AZURE_OPENAI_DEPLOYMENT="gpt4o"
export PORT="8000"
```

2. Run:

```bash
go run .
```

3. Call the gateway:

```bash
curl -sS http://localhost:8000/llm \
  -H "Authorization: Bearer gw-dev-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt4o",
    "messages": [
      {"role": "user", "content": "Say hello in one sentence."}
    ]
  }'
```

## Config

- `GATEWAY_API_KEY` (required): key your clients must send.
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

Compatibility fallbacks:

- If `AZURE_OPENAI_API_KEY` is missing, `OPENAI_API_KEY` is accepted as a fallback.
- If `AZURE_OPENAI_BASE_URL` is missing, `OPENAI_BASE_URL` is accepted as a fallback.

## Notes

- Auth supports `Authorization: Bearer <GATEWAY_API_KEY>` and `X-API-Key`.
- `/healthz` is available for health checks.
- Streaming responses are forwarded to the client.
