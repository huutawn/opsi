# Opsi Cloud

Opsi Cloud is the durable control-plane and identity boundary. It does not run workloads or store raw Agent logs/metrics. With PostgreSQL configured it owns organization/project membership, PAT/OAuth identity linkage, OTP requests, node/Agent registration, bootstrap and immutable deployment jobs, GitHub App inventory, idempotency, rate limits, and append-only audit events.

## Authentication currently implemented

- **Initial owner bootstrap:** local operator command `opsi-cloud admin bootstrap-owner`; creates the first user, organization, project, Owner membership, and optionally a one-time PAT file and/or prelinked OAuth subject.
- **PAT:** bearer token verification scoped to a project or organization; bcrypt hashes only; expiry and revocation; issue through prelinked GitHub identity; client-safe rotate flow; revoke endpoint.
- **GitHub App user authorization:** authorization-code mediation through `/v1/auth/browser/start`, `/callback`, and `/redeem`, using fixed GitHub endpoints, PKCE S256, five-minute one-time state, and provider `github`. The subject is the canonical decimal form of the positive numeric GitHub user ID. The identity must already be linked to an Opsi user; login/email are never used as fallback identity and no user or membership is created automatically.
- **GitHub App installation authentication:** an RSA PKCS#1 or RSA-in-PKCS#8 private key is loaded once from an absolute, non-symlink, non-writable regular file. Cloud creates nine-minute RS256 App JWTs and obtains installation access tokens from the fixed GitHub API endpoint. Installation tokens are cached only in memory and refreshed when fewer than two minutes remain.
- **Build runner auth:** Cloud dispatches only configured Opsi executor workflows through the GitHub App, then accepts `opsi-build` GitHub OIDC from the exact configured repository, workflow, and ref. One atomic claim changes a Dockerfile BuildJob from `ready` to `running` and returns a ten-minute, BuildJob/attempt/run-scoped lease whose SHA-256 hash is stored separately from the immutable source snapshot. `POST /v1/build-runner/source-access` revalidates that lease, attempt, GitHub run, running BuildJob, and current binding identity before minting a non-cached installation token restricted to the exact repository ID and `contents: read`. GitHub controls its returned expiry (normally one hour); Cloud returns that expiry and never persists or audits the token.
- **GitHub installation claim:** PAT-authenticated Owner/Admin starts a purpose-bound OAuth flow for one project and numeric installation ID. The callback compares GitHub `/user` with the prelinked Opsi identity, proves installation visibility through `/user/installations`, syncs visible repositories, and returns only a 90-second one-time local grant. Setup/query `installation_id`, account login, and repository full name are not proof.
- **OTP:** PAT-authenticated `/v1/otp/request` and `/v1/otp/verify`; the recipient email is derived from the verified PAT identity, with salted hashes, five-minute expiry, one-time use, rate limiting, SMTP or file outbox.
- **Agent auth:** one-time registration token exchange, then a scoped bearer credential stored as a bcrypt hash. Production also requires an HMAC timestamp/signature on Agent requests.
- **Bootstrap worker auth:** the daemon pool uses its shared worker token only to lease arbitrary SSH sessions. A reviewed command session uses an expiring one-time `Bootstrap` token to claim that exact session; all later checkpoint, heartbeat, progress, and finish calls use the existing worker ID and per-lease token.
- **Internal alert auth:** dedicated internal token.

There is no password login and no public self-sign-up endpoint.

The GitHub user access token is held only for the callback's `/user`,
`/user/installations`, and visible-repository requests; it is not persisted,
audited, or returned to the CLI. For a user-account installation, account ID
must equal the numeric GitHub user ID. For an organization installation, MVP
proof means only that the token can see the installation; it does not prove the
user is a GitHub organization owner. Pending OAuth state and local one-time
grants are in memory, so a Cloud restart invalidates them. The App private key
is not hot-reloaded; replacement requires a Cloud restart. Installation tokens
remain in memory. The flow has not yet been exercised against a real GitHub App.

## Main runtime responsibilities

- Registry APIs for organizations, projects, memberships, nodes, services, bootstrap sessions, deployments, node lifecycle jobs, GitHub inventory, repository claims, and service bindings.
- Durable PostgreSQL migrations and stores when `database_url` is set.
- GitHub App intake at `/v1/webhooks/github-app` uses the separate App-wide webhook secret, verifies `X-Hub-Signature-256` before JSON decoding, and parses typed `installation`, `installation_repositories`, and `repository` mutations. Unknown events/actions are ignored with `202`. Supported mutations atomically insert the delivery ID and apply inventory changes in one registry transaction; PostgreSQL uniqueness deduplicates delivery after Cloud restart. The bounded P08 in-memory replay store remains as the fast in-process layer.
- A ready Dockerfile BuildJob can be dispatched to an Opsi-owned GitHub-hosted executor configured by `OPSI_BUILD_EXECUTOR_OWNER`, `OPSI_BUILD_EXECUTOR_REPOSITORY`, `OPSI_BUILD_EXECUTOR_WORKFLOW`, and `OPSI_BUILD_EXECUTOR_REF`. The executor repository supplies the HTTPS Cloud origin through its `OPSI_CLOUD_URL` Actions variable. Workflow inputs contain only the BuildJob and opaque attempt IDs; the trusted runner claims with GitHub OIDC and fetches the canonical source/build-path spec. The active workflow intentionally remains handshake-only until registry publication and BuildRecord creation exist.
- `opsi-build-executor` is the independent P05B2B1 primitive. It fetches only `resolved_commit_sha` with temporary `GIT_ASKPASS`, verifies detached `HEAD`, removes credentials and `.git`, preserves the repository-root monorepo layout, then runs `docker buildx build` for `linux/amd64` with plain progress, an isolated empty Docker config, OCI tar output, and BuildKit metadata. It never uses push, load, tags, host networking flags, insecure entitlements, SSH forwarding, BuildKit secrets, or inferred build args. The command fails closed unless Buildx is exactly `0.35.0` and BuildKit is exactly `v0.31.2`; no setup action is currently used.
- Git submodules and Git LFS are not supported in P05B2B1. Repositories containing `.gitmodules` or active `filter=lfs` attributes fail before BuildKit instead of building incomplete source.
- Existing GitHub Actions OIDC BuildRecord submission remains the accepted artifact handoff for immutable deployment creation.
- Historical relay/deployment tables remain for restore and read compatibility. Runtime code does not enqueue, claim, or lease legacy relay jobs.
- Numeric GitHub installation, account, repository, and owner IDs are authoritative. Installations and repositories are statused rather than physically deleted. One active repository claim belongs to one project; a repository may bind multiple services in that project through distinct service keys, while each service has at most one active GitHub binding. Bindings never target Agent, Node, runtime, or VPS identity.
- Bootstrap session credential handoff. PostgreSQL mode encrypts SSH credentials, command claim tokens, and one-time Agent registration tokens with AES-GCM using `bootstrap_secret_key`.
- Health and Prometheus metrics endpoints.

Remote GitHub Actions cancellation is deferred. Cancelling a BuildJob before claim rejects the runner; after claim, lease-authorized endpoints require the BuildJob to remain `running`, so a cancelled job cannot continue through the P05B2A handshake.

## Bootstrap Worker

`opsi-bootstrap-worker` is built from the same module and keeps one authoritative `first-server-v2` execution path. The daemon leases SSH sessions from the worker pool; the default Connect Server command downloads the checksum-pinned Linux amd64 worker and claims only its reviewed session from the target VPS. Both modes use the same plan, durable checkpoint, lease, Agent registration, and heartbeat contracts. The stable step IDs remain `preflight`, `install_k3s`, `install_agent`, and `register_agent`. Metadata for `first-server-v1` remains readable, but an unfinished v1 checkpoint fails with `BOOTSTRAP_PLAN_MISMATCH`; the operator must create a new bootstrap session.

Step execution is at-least-once: a remote step runs, Cloud durably acknowledges the next-step checkpoint, and only then may the worker continue. K3s uses an operator-pinned version and verified installer checksum. Agent artifacts are staged under `/opt/opsi/agent/releases/<sha256>`, activated atomically through `current`, and rolled back through `previous` when the new service is unhealthy. A root-owned registration identity marker prevents a completed registration script from POSTing again after checkpoint acknowledgement loss.

The registration flow still has one documented crash window: Cloud may consume the one-time registration token before the remote config and marker are durably installed. P05 does not add server-side credential replay; P06 must fault-inject around this boundary.

The worker has two Cloud URLs:

- `cloud_url`: internal worker-to-Cloud control URL, such as `http://cloud:9800` inside Docker Compose.
- `agent_cloud_url`: URL reachable from the target VPS and later used by the installed Agent. For a remote VPS this must be a public/private-routable HTTPS URL, not a Docker service name or `127.0.0.1`.

The default command flow requires no SSH credential or `known_hosts` input and must run as root on a Linux amd64 target. Advanced password and unencrypted SSH private-key authentication remain supported. SSH never falls back to insecure host-key acceptance. Operators using SSH must provide a trusted regular `known_hosts` file; production also requires it to be non-empty and requires HTTPS for K3s, Agent artifact, Cloud, and Agent-facing URLs. K3s version and both installer/artifact SHA-256 values must be explicitly pinned; the worker does not discover latest versions.

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
