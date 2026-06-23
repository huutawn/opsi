# Opsi Cloud

Cloud is the relay and identity boundary. Current code includes the Phase 2 in-memory Webhook Relay runtime consumed by Agent long-poll clients, plus production-minimum PAT verify and OTP runtime backed by Postgres when `database_url` is configured.

## Phase 1 Auth Contract

### Login

Implemented endpoint:

```http
POST /v1/auth/login
```

Returns a Personal Access Token once. Cloud must store only a bcrypt hash.

### Verify PAT

Future endpoint:

```http
POST /v1/auth/pat/verify
```

Request shape:

```json
{
  "token": "plain-token-presented-by-agent",
  "project_id": "dev-project"
}
```

Response shape:

```json
{
  "user_id": "uuid",
  "role": "Owner",
  "expires_at": "2026-09-16T00:00:00Z",
  "revoked": false
}
```

PAT verification compares the presented token against bcrypt hashes in `personal_access_tokens` and returns the project role from `project_memberships`. Cloud still needs OAuth login and PAT issuance/revoke endpoints.

### OTP Delivery

`POST /v1/otp/request` and `POST /v1/otp/verify` are implemented. OTP codes are salted and hashed, expire after 5 minutes, verify once, and are rate-limited per user. Configure SMTP for email delivery, or `otp.outbox_path` for a local file outbox. Codes are not returned by the API unless `otp.dev_echo` is enabled.

## Build/Test

```bash
rtk go test ./...
rtk go build ./cmd/opsi-cloud
rtk go run ./cmd/opsi-cloud --config config.example.json --addr 127.0.0.1:9800
```

## Phase 2 Webhook Relay Contract

Contract source: `../contracts/cloud/v1/webhook_relay.md`.

Cloud receives GitHub push webhooks at `POST /v1/webhooks/github`, maps repo/branch to `project_id` + `service_id`, keeps the signed envelope for at most 24 hours, and exposes `GET /v1/agents/{agent_id}/webhooks/next?project_id=...&wait=30s` for Agent long-poll. Agent validates `X-Hub-Signature-256` locally with its configured `deployment.webhook_secret` before deployment.

The current runtime keeps relay envelopes in process memory and purges by TTL. It is suitable for local/dev validation of the Phase 2 contract; production Cloud still needs a durable queue/provider implementation with the same endpoint shape.
