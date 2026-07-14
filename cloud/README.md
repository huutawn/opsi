# Opsi Cloud

Opsi Cloud is the durable control-plane and identity boundary. It does not run workloads or store raw Agent logs/metrics. With PostgreSQL configured it owns organization/project membership, PAT/OAuth identity linkage, OTP requests, node/Agent registration, bootstrap/deployment queues, webhook relay metadata, idempotency, rate limits, and append-only audit events.

## Authentication currently implemented

- **Initial owner bootstrap:** local operator command `opsi-cloud admin bootstrap-owner`; creates the first user, organization, project, Owner membership, and optionally a one-time PAT file and/or prelinked OAuth subject.
- **PAT:** bearer token verification scoped to a project or organization; bcrypt hashes only; expiry and revocation; issue through prelinked GitHub identity; client-safe rotate flow; revoke endpoint.
- **GitHub App user authorization:** authorization-code mediation through `/v1/auth/browser/start`, `/callback`, and `/redeem`, using fixed GitHub endpoints, PKCE S256, five-minute one-time state, and provider `github`. The subject is the canonical decimal form of the positive numeric GitHub user ID. The identity must already be linked to an Opsi user; login/email are never used as fallback identity and no user or membership is created automatically.
- **GitHub App installation authentication:** an RSA PKCS#1 or RSA-in-PKCS#8 private key is loaded once from an absolute, non-symlink, non-writable regular file. Cloud creates nine-minute RS256 App JWTs and obtains installation access tokens from the fixed GitHub API endpoint. Installation tokens are cached only in memory and refreshed when fewer than two minutes remain.
- **OTP:** PAT-authenticated `/v1/otp/request` and `/v1/otp/verify`; the recipient email is derived from the verified PAT identity, with salted hashes, five-minute expiry, one-time use, rate limiting, SMTP or file outbox.
- **Agent auth:** one-time registration token exchange, then a scoped bearer credential stored as a bcrypt hash. Production also requires an HMAC timestamp/signature on Agent requests.
- **Bootstrap worker auth:** shared worker token plus worker ID and per-lease token for internal bootstrap endpoints.
- **Internal alert auth:** dedicated internal token.

There is no password login and no public self-sign-up endpoint.

The GitHub user access token is held only long enough to request GitHub `/user`;
it is not persisted, audited, or returned to the CLI. Pending browser login
state and local one-time grants are in memory, so a Cloud restart invalidates
them. The App private key is not hot-reloaded; replacement requires a Cloud
restart. Installation tokens and the 24-hour bounded webhook replay ledger are
also in memory. The flow has not yet been exercised against a real GitHub App.

## Main runtime responsibilities

- Registry APIs for organizations, projects, memberships, nodes, services, bootstrap sessions, deployments, and node lifecycle jobs.
- Durable PostgreSQL migrations and stores when `database_url` is set.
- Legacy GitHub push intake at `/v1/webhooks/github` retains route-specific SHA-256 HMAC verification and sanitized Agent relay behavior. Every configured route requires its own webhook secret of at least 32 bytes.
- GitHub App intake at `/v1/webhooks/github-app` uses the separate App-wide webhook secret, verifies `X-Hub-Signature-256` before JSON decoding, and parses typed `installation`, `installation_repositories`, and `repository` mutations. Unknown events/actions are ignored with `202`; supported mutations return `503` until P09 injects a durable event sink. Replay protection is bounded to 10,000 in-memory delivery IDs and is lost on restart.
- Bootstrap session credential handoff. PostgreSQL mode encrypts SSH credentials and one-time Agent registration tokens with AES-GCM using `bootstrap_secret_key`.
- Health and Prometheus metrics endpoints.

## Bootstrap Worker

`opsi-bootstrap-worker` is a separate daemon built from the same module. It leases one pending bootstrap session, retrieves the short-lived SSH credential and Agent registration token, builds deterministic `first-server-v2`, verifies its SHA-256 fingerprint, and resumes from the durable Cloud checkpoint. The stable remote step IDs remain `preflight`, `install_k3s`, `install_agent`, and `register_agent`; Agent heartbeat verification follows after all four are acknowledged. Metadata for `first-server-v1` remains readable, but an unfinished v1 checkpoint fails with `BOOTSTRAP_PLAN_MISMATCH`; the operator must create a new bootstrap session.

Step execution is at-least-once: a remote step runs, Cloud durably acknowledges the next-step checkpoint, and only then may the worker continue. K3s uses an operator-pinned version and verified installer checksum. Agent artifacts are staged under `/opt/opsi/agent/releases/<sha256>`, activated atomically through `current`, and rolled back through `previous` when the new service is unhealthy. A root-owned registration identity marker prevents a completed registration script from POSTing again after checkpoint acknowledgement loss.

The registration flow still has one documented crash window: Cloud may consume the one-time registration token before the remote config and marker are durably installed. P05 does not add server-side credential replay; P06 must fault-inject around this boundary.

The worker has two Cloud URLs:

- `cloud_url`: internal worker-to-Cloud control URL, such as `http://cloud:9800` inside Docker Compose.
- `agent_cloud_url`: URL reachable from the target VPS and later used by the installed Agent. For a remote VPS this must be a public/private-routable HTTPS URL, not a Docker service name or `127.0.0.1`.

Password and unencrypted SSH private-key authentication are supported. SSH never falls back to insecure host-key acceptance. Operators must provide a trusted regular `known_hosts` file; production also requires it to be non-empty and requires HTTPS for K3s, Agent artifact, Cloud, and Agent-facing URLs. K3s version and both installer/artifact SHA-256 values must be explicitly pinned; the worker does not discover latest versions.

## Build and test

```bash
go test ./...
go build ./cmd/opsi-cloud
go build ./cmd/opsi-bootstrap-worker
```

Run configuration validation without starting either daemon:

```bash
go run ./cmd/opsi-cloud --check --config config.example.json
go run ./cmd/opsi-bootstrap-worker --check --config ../deploy/dev-control-plane/config/bootstrap-worker.json
```

The Bootstrap Worker example intentionally contains operator placeholders. The
development workflow generates the ignored runtime JSON, substitutes a
syntactically valid nonfunctional K3s pin, and reports warnings until real P06
inputs are supplied.
