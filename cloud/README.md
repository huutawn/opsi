# Opsi Cloud

Opsi Cloud is the durable control-plane and identity boundary. It does not run workloads or store raw Agent logs/metrics. With PostgreSQL configured it owns organization/project membership, PAT/OAuth identity linkage, OTP requests, node/Agent registration, bootstrap/deployment queues, webhook relay metadata, idempotency, rate limits, and append-only audit events.

## Authentication currently implemented

- **Initial owner bootstrap:** local operator command `opsi-cloud admin bootstrap-owner`; creates the first user, organization, project, Owner membership, and optionally a one-time PAT file and/or prelinked OAuth subject.
- **PAT:** bearer token verification scoped to a project or organization; bcrypt hashes only; expiry and revocation; issue through OAuth; client-safe rotate flow; revoke endpoint.
- **Browser OAuth:** authorization-code mediation through `/v1/auth/browser/start`, `/callback`, and `/redeem`. The provider subject must already be linked to a user; provider email alone is not trusted.
- **OTP:** PAT-authenticated `/v1/otp/request` and `/v1/otp/verify`; the recipient email is derived from the verified PAT identity, with salted hashes, five-minute expiry, one-time use, rate limiting, SMTP or file outbox.
- **Agent auth:** one-time registration token exchange, then a scoped bearer credential stored as a bcrypt hash. Production also requires an HMAC timestamp/signature on Agent requests.
- **Bootstrap worker auth:** shared worker token plus worker ID and per-lease token for internal bootstrap endpoints.
- **Internal alert auth:** dedicated internal token.

There is no password login and no public self-sign-up endpoint.

## Main runtime responsibilities

- Registry APIs for organizations, projects, memberships, nodes, services, bootstrap sessions, deployments, and node lifecycle jobs.
- Durable PostgreSQL migrations and stores when `database_url` is set.
- GitHub webhook intake with route-specific SHA-256 HMAC verification, followed by a bounded sanitized relay to an authenticated Agent. Every configured route requires a webhook secret of at least 32 bytes.
- Bootstrap session credential handoff. PostgreSQL mode encrypts SSH credentials and one-time Agent registration tokens with AES-GCM using `bootstrap_secret_key`.
- Health and Prometheus metrics endpoints.

## Bootstrap Worker

`opsi-bootstrap-worker` is a separate daemon built from the same module. It leases one pending bootstrap session, retrieves the short-lived SSH credential and Agent registration token, builds the deterministic `first-server-v1` plan, verifies its SHA-256 fingerprint, and resumes from the durable Cloud checkpoint. The stable remote step IDs are `preflight`, `install_k3s`, `install_agent`, and `register_agent`; Agent heartbeat verification follows after all four are acknowledged.

Step execution is at-least-once: a remote step runs, Cloud durably acknowledges the next-step checkpoint, and only then may the worker continue. If acknowledgement fails, the worker schedules the existing retry path without advancing locally, so that step may run again. A fully checkpointed plan skips SSH and proceeds directly to Agent heartbeat verification. P05 still owns installer idempotency, artifact/transport hardening, K3s pinning, and the canonical systemd layout.

The worker has two Cloud URLs:

- `cloud_url`: internal worker-to-Cloud control URL, such as `http://cloud:9800` inside Docker Compose.
- `agent_cloud_url`: URL reachable from the target VPS and later used by the installed Agent. For a remote VPS this must be a public/private-routable HTTPS URL, not a Docker service name or `127.0.0.1`.

Password and unencrypted SSH private-key authentication are supported. Production requires a known-hosts file and HTTPS URLs.

## Build and test

```bash
go test ./...
go build ./cmd/opsi-cloud
go build ./cmd/opsi-bootstrap-worker
```

Run configuration validation without starting either daemon:

```bash
go run ./cmd/opsi-cloud --check --config config.example.json
go run ./cmd/opsi-bootstrap-worker --check --config ../deploy/dev-control-plane/config/bootstrap-worker.example.json
```
