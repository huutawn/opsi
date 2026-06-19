# Opsi Cloud

Cloud is the stateless relay and identity boundary. Current code still has no provider runtime; Phase 2 adds the public webhook relay contract consumed by Agent long-poll clients.

## Phase 1 Auth Contract

### Login

Future endpoint:

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
  "token": "plain-token-presented-by-agent"
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

## Build/Test

No runtime Cloud implementation exists yet. Add provider-specific build/test commands when Cloud code is introduced.

## Phase 2 Webhook Relay Contract

Contract source: `../contracts/cloud/v1/webhook_relay.md`.

Cloud receives GitHub push webhooks at `POST /v1/webhooks/github`, keeps the signed envelope for at most 24 hours, and exposes `GET /v1/agents/{agent_id}/webhooks/next?wait=30s` for Agent long-poll. Agent validates `X-Hub-Signature-256` locally with its configured `deployment.webhook_secret` before deployment.
