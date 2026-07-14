# Opsi Current State

| Metadata | Value |
|---|---|
| Status | Implemented-state snapshot; not a production-readiness claim |
| Last updated | 2026-07-14 |
| Requirements | `docs/opsi_srs.md` |
| Evidence matrix | `docs/status_matrix.md` |
| Canonical roadmap | `docs/opsi_roadmap_v4.md` |
| Trusted artifact target | `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md` |

## M0 boundary state

Earlier baseline work removed the old AI/RCA execution boundary and source
artifacts. At this snapshot:

- Cloud has no AI runtime, provider configuration, Gemini integration, fixture
  RCA, prompt path, or `/v1/ai/*` route.
- Agent has no HTTP analyzer, fallback RCA, provider/model metadata, AI network
  call, RCA-backed approval/execution, analyze/approve RPC, or incident-owned
  Kubernetes mutation executor.
- CLI, local API, contracts, and UI expose factual incident list/get/resolve only.
- Historical `rca_result` and `mitigation_actions_json` columns remain for
  storage compatibility but are not read, exposed, or executed.
- `IncidentEvidence v1`, Safe ActionPlane, and `opsi mcp serve` are not
  implemented.
- GitHub App user authorization and P08 installation authentication/webhook
  intake are implemented. Durable installation/repository mapping,
  GitHub Actions OIDC, `BuildRecord`, digest-based deployment,
  `DeploymentPolicy`, and pull request preview environments are not implemented.
- Opsi does not render or manage Ingress, Gateway API resources, domains, or TLS.
- Source packaging rejects local config, credentials, private keys, runtime
  certificate directories, databases, logs, and generated output.

## Repository shape

The workspace contains independent Go modules under `agent/`, `cli/`, `cloud/`,
and `contracts/go/`, plus the CLI web application. Public shared schemas and
bindings live under `contracts/`; business logic remains in the owning domain.

## Implemented Agent slice

- The `opsi-agent` executable is restored as the single entrypoint over the
  existing `config.Load` and `server.Run` composition. It requires `--config`
  for runtime, supports config-only validation, and reports injected version
  plus full commit metadata without requiring configuration.
- A deterministic Linux amd64 release builder produces the direct executable,
  `checksums.txt`, and stable `release.json` SHA-256 metadata. A local verifier
  rebuilds twice with separate Go caches and compares the binary and metadata
  byte-for-byte within the same source tree and Go toolchain.
- Status, deployment, service management, telemetry, secret, and incident gRPC
  services; HTTP health; TLS 1.3 configuration with optional client certificate
  verification.
- SQLite WAL stores for deployment, services, managed services/bindings,
  telemetry, incidents, and runtime audit.
- Git-source deployment with safe relative-path validation, containerd-first
  build, Docker fallback, dry-run adapters, rollout watch, deploy-time rollback,
  rollback verification, redacted errors, and service binding injection.
- Managed PostgreSQL/Redis renderers and external service registration with
  project-scoped storage and deletion/purge distinction.
- Kubernetes/cAdvisor/runtime telemetry collection, bounded logs, retention,
  compressed sync chunks, redacted summaries, and service health queries.
- Kubernetes Secret application through stdin, Cloud PAT verification cache,
  Owner plus OTP/TOTP reveal gate, rotation/restart, and redacted audit.
- Deterministic bounded incident context from metric windows and log
  fingerprints, plus list/get/resolve authorization, MTTR, and resolve audit.
- Cloud relay client and runner for signed heartbeat, deployment lease/result,
  and `DeploymentIntent`-scoped Git execution. Image-source deploy is rejected
  before runtime execution.

Agent does not currently provide public incident evidence or a unified action
policy/approval/executor contract.

The Agent release artifact is not hosted over HTTPS, and Bootstrap Worker has
not been exercised against the real artifact. Target VPS evidence remains
`UNPROVEN`. P05 now uses the canonical checksum-addressed Agent layout and
systemd unit with atomic activation and tested rollback contracts, but P06 must
still prove the behavior on a clean target VPS.

## Implemented CLI/local backend slice

- Cobra commands for login, start, status, deploy, sync, service, secret, and
  incident list/get/resolve.
- OS-keychain PAT storage and Agent gRPC TLS/client-certificate/certificate-pin
  support.
- `opsi start` localhost server with short local sessions, `/api/local/...`
  Browser mediation, static UI serving, and honest 503 behavior when assets are
  unavailable.
- Local facades for Cloud project/registry metadata and Agent status,
  telemetry/log summaries, secret create/reveal/rotate, and incident
  list/get/resolve. Mutations require local session and idempotency headers.
- Removed incident analyze/approve commands and routes return no active surface;
  removed paths are not compatibility aliases.

The CLI has no MCP server, AI provider integration, approval signer flow, or
Safe ActionPlane client.

## Implemented Cloud slice

- Organization/project/membership metadata, RBAC, PAT verification and GitHub
  App user authorization grant mediation, OTP, Agent/node registration, bootstrap sessions,
  deployment job envelopes, webhook relay, audit, and support metadata.
- GitHub webhook routes require a per-route secret and Cloud verifies the
  SHA-256 HMAC before accepting and sanitizing the payload.
- The browser login path uses fixed GitHub authorization, token, and `/user`
  endpoints with PKCE S256 and five-minute one-time in-memory state. Provider is
  fixed to `github`; subject is the canonical decimal positive numeric GitHub
  user ID. The identity must be prelinked, and Cloud does not trust login/email
  or create an Opsi user or membership during login.
- GitHub user access tokens exist only during the callback request and are not
  persisted, audited, or returned to the CLI. Pending state and local grants
  remain in memory and are lost on Cloud restart. The flow has focused test
  coverage but no live GitHub App verification.
- GitHub App installation authentication loads an RSA PKCS#1 or RSA-in-PKCS#8
  private key once from a protected read-only file, creates RS256 App JWTs with
  a one-minute `iat` backdate and nine-minute expiry, and requests installation
  access tokens from the fixed GitHub endpoint. Tokens are cached only in
  memory per installation and refreshed with a two-minute safety window.
- `/v1/webhooks/github-app` verifies the App-wide SHA-256 webhook secret before
  decoding and emits typed installation/repository events using numeric IDs as
  identity. Its 24-hour, 10,000-entry replay state is in memory and lost on
  restart. P08 intentionally provides no durable sink: supported mutations
  return `503` until P09 injects one. The legacy `/v1/webhooks/github` route and
  `routes[].webhook_secret` behavior remain unchanged.
- Postgres-backed registry/relay/audit/idempotency/bootstrap/PAT/OTP state when
  configured; development/test modes may use in-memory implementations where
  production validation permits.
- Agent registration/rotation/revocation, HMAC request validation, bootstrap and
  deployment rate limits, durable deployment leases/retry/dead-letter state,
  and append-only Cloud audit protections for the Postgres path.
- Bootstrap Worker is now a long-running, single-concurrency daemon. It polls
  Cloud and atomically leases the oldest eligible pending or due retry session
  without an operator-provided session ID. Each lease increments a server-owned
  bounded attempt count. Worker progress, heartbeat, and finish requests require
  the lease owner and raw lease token; storage retains only its hash.
- Bootstrap sessions now carry a durable checkpoint independent of status,
  progress events, lease owner, and attempt count. The authoritative
  `first-server-v2` plan uses stable step IDs `preflight`, `install_k3s`,
  `install_agent`, and `register_agent`, plus a deterministic SHA-256
  fingerprint over the version, ordered step command hashes, K3s pin/installer,
  Agent artifact URL/checksum, Agent Cloud URL, and canonical systemd unit.
- Fresh workers initialize checkpoint index zero. After each successful remote
  step, Cloud persists the next index under the active lease before another
  step may run. Retry, manual retry, lease recovery, and a new worker lease keep
  the checkpoint and resume from the next unacknowledged step. New sessions use
  `first-server-v2`; unfinished `first-server-v1` checkpoints fail closed and
  require a new session. A session with
  all four steps checkpointed skips SSH and waits for Agent heartbeat directly.
- Bootstrap resume semantics are at-least-once. If a remote step succeeds but
  checkpoint acknowledgement fails, no later step runs and the successful step
  may execute again on retry. P05 adds idempotent K3s version detection and
  verified upgrade, checksum-addressed Agent staging, atomic activation,
  registration-marker replay, and rollback contracts. Real VPS behavior remains
  unproven.
- Worker configuration no longer accepts fixed `session_id`. It requires an
  operator-pinned K3s version, installer URL/SHA-256, Agent artifact URL/SHA-256,
  and an Agent-reachable Cloud URL. Production requires HTTPS and a trusted,
  non-empty, non-writable `known_hosts` file; SSH has no insecure fallback.
- Bootstrap accepts password or unencrypted SSH private-key credentials. Worker
  control traffic uses `cloud_url`, while the installed Agent receives the
  separately configured, target-reachable `agent_cloud_url`.
- Bootstrap-generated Agent configuration enables Cloud PAT verification even
  though the Agent gRPC listener remains loopback-only. Cloud OTP request and
  verification endpoints require the same project PAT and derive the delivery
  email from the verified identity rather than trusting request input.
- Active leases are renewed with authenticated heartbeats. Expired leases are
  recovered before polling, retryable failures use persisted exponential
  backoff, and exhausted or permanent failures enter `dead_letter`. Owner/Admin
  may request an idempotent manual retry when a valid bootstrap credential is
  available.
- Credential retrieval is non-destructive across attempts. Agent registration
  tokens rotate per attempt for the same session and node, and terminal session
  handling deletes credential and unused registration material.
- In-memory and PostgreSQL repositories share lease, heartbeat, recovery,
  retry, dead-letter, and manual retry semantics. The PostgreSQL restart test is
  mandatory in `make verify-postgres`; execution still requires
  `OPSI_TEST_DATABASE_URL`.
- P04-focused in-memory, worker, HTTP, PostgreSQL, concurrent transition, and
  pre-P04 row migration-upgrade tests pass. OTP/PAT baseline failure fixed;
  full Cloud suite PASS at this commit.
- P05-focused Bootstrap Worker/Registry tests and race tests pass. Full Agent
  tests pass. Development Compose build and isolated four-service health smoke
  pass.
- Registration replay is idempotent after config/marker persistence. A narrow
  crash window remains if Cloud consumes the token before those files are
  installed; P06 must fault-inject around this boundary.
- The existing Cloud binary exposes the local operator command
  `opsi-cloud admin bootstrap-owner`. It requires PostgreSQL and transactionally
  creates or reuses the normalized user, organization, canonical project plus
  default environment/runtime, Owner memberships, OAuth identity and/or initial
  PAT hash, durable `first_owner` state, and a redacted audit event.
- Exact repeats return the same IDs without issuing another PAT. Conflicting
  owner tuples, project owners, or OAuth identities fail closed. Browser OAuth
  login now resolves the prelinked provider subject instead of authorizing by
  callback email alone. Initial PAT plaintext is never printed or logged and is
  finalized only to an explicitly requested non-overwritten mode-0600 file.
- The command is not an HTTP endpoint and never runs during Cloud startup.

Cloud has no AI runtime and does not own Kubernetes execution or raw runtime
evidence.

## Deployment and gateway truth

Opsi now has one supported development control-plane deployment path: Docker
Compose. The package starts PostgreSQL, Opsi Cloud, one Bootstrap Worker, and a
Caddy reverse proxy. PostgreSQL data and Cloud OTP/alert outboxes use named
volumes. All four services have health checks, `unless-stopped` restart policy,
and bounded Docker logs. Cloud performs the existing schema migration during
controlled startup after PostgreSQL becomes healthy, and Cloud health fails
closed if PostgreSQL later becomes unavailable. The development reverse proxy
does not expose worker-internal, alert-internal, or metrics endpoints.

The committed configuration examples contain placeholders only. Runtime
environment, Cloud/Worker configuration, secret directory, and initial PAT
files are gitignored. This package is development-only. P01 code is complete,
but clean control-plane VPS checkpoint `CP-VPS-1` was not run because no clean
Ubuntu VPS was available. Its status is `DEFERRED / UNPROVEN`; no VPS evidence
exists, and the checkpoint remains a blocker before production acceptance.

Git-based deployment exists and can apply user-provided manifests. Such a
manifest may contain its own Service, Ingress, Gateway, TLS, lifecycle, or
shutdown configuration; those resources are user-owned input, not an
Opsi-managed gateway. `IngressEnabled` was removed from active contracts/config,
with a fail-fast error retained for old configuration.

The migration target is:

```text
legacy/manual Git build
-> trusted OCI artifact delivery
```

The target flow uses GitHub Actions build/test, an OCI registry, an OIDC-bound
`BuildRecord`, `DeploymentPolicy`, a durable `DeploymentJob`, and Agent
deployment of `registry/repository@sha256:<digest>`. Git commit SHA remains
source identity and provenance, not the runtime artifact. This target has not
started: image-source deployment remains rejected, and the current Git-source
clone/build path remains implemented for legacy/manual development use.

Opsi-rendered Deployment/ClusterIP Service, managed Traefik `ExposureSpec`,
conflict checks, readiness, and rollback also have not started.

## E2E and production evidence

`scripts/e2e/verify-k3s.sh`, `make verify-e2e-k3s-preflight`, and
`make verify-e2e-k3s` define the protected clean VPS/K3s command path. The
incident segment checks factual incident list, detail, resolve, and resolve
audit. The command path exists, but no committed real-infrastructure pass
artifact currently proves the complete scenario. Status remains
`MANUAL_GATED`.

Production readiness remains unproven. Current gaps include clean control-plane
VM and restart proof, clean VPS bootstrap proof, live GitHub App installation
and user-auth verification, durable repository mapping, hosted and hardened
Agent delivery, Actions OIDC, trusted
OCI artifact delivery, managed
gateway, public incident evidence, Safe ActionPlane, CLI MCP, complete Dev VPS
E2E, release hardening, supply-chain evidence, and measured disaster recovery.

## Ordered next work

P03 Agent executable and deterministic local release artifact code is complete.
P04 durable checkpoint/resume behavior is implemented and its Cloud closure
gate is green: OTP/PAT baseline failure fixed; full Cloud suite PASS at this
commit. P05 supply-chain,
transport, installer, checksum, HTTPS, K3s pinning, and canonical systemd layout
hardening is implemented with focused/race and development smoke evidence. P06 clean target VPS proof
remains `DEFERRED / UNPROVEN`; there is no clean target VPS evidence. P07 GitHub
App user authorization code and P08 installation authentication/webhooks are
code complete, while real GitHub verification is `UNPROVEN`. P09 durable
installation/repository mapping is next;
OIDC-bound trusted artifact delivery, runtime delivery, and the later
evidence/ActionPlane/MCP phases remain ordered future work. The ordered source
of truth is `docs/opsi_roadmap_v4.md`.

## Verification commands

From repository root:

```bash
make test
make build
make agent-release
make verify-agent-release
make release
make smoke-release
make verify-e2e-k3s-selfcheck
make source-hygiene
make package-source
make verify
```

Go module tests run from `agent/`, `cli/`, `cloud/`, and `contracts/go/`, not
from the workspace root.
