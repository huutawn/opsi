# Opsi Current State

| Metadata | Value |
|---|---|
| Status | Implemented-state snapshot; not a production-readiness claim |
| Last updated | 2026-07-12 |
| Requirements | `docs/opsi_srs.md` |
| Evidence matrix | `docs/status_matrix.md` |
| Canonical roadmap | `docs/opsi_roadmap_v3/12_EXECUTION_BACKLOG.md` |

## M0 boundary state

Phase 1 tasks V3-001 through V3-007 removed the old AI/RCA execution boundary
and source artifacts. At this snapshot:

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
- Opsi does not render or manage Ingress, Gateway API resources, domains, or TLS.
- Source packaging rejects local config, credentials, private keys, runtime
  certificate directories, databases, logs, and generated output.

## Repository shape

The workspace contains independent Go modules under `agent/`, `cli/`, `cloud/`,
and `contracts/go/`, plus the CLI web application. Public shared schemas and
bindings live under `contracts/`; business logic remains in the owning domain.

## Implemented Agent slice

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

- Organization/project/membership metadata, RBAC, PAT verification and browser
  OAuth grant mediation, OTP, Agent/node registration, bootstrap sessions,
  deployment job envelopes, webhook relay, audit, and support metadata.
- Postgres-backed registry/relay/audit/idempotency/bootstrap/PAT/OTP state when
  configured; development/test modes may use in-memory implementations where
  production validation permits.
- Agent registration/rotation/revocation, HMAC request validation, bootstrap and
  deployment rate limits, durable deployment leases/retry/dead-letter state,
  and append-only Cloud audit protections for the Postgres path.
- Bootstrap Worker is now a long-running, single-concurrency daemon. It polls
  Cloud and atomically leases the oldest pending bootstrap session without an
  operator-provided session ID. Worker status, progress, and finish requests
  require the lease owner and raw lease token; storage retains only its hash.
- Worker configuration no longer accepts fixed `session_id`. The existing SSH,
  K3s, Agent install, registration, and Agent heartbeat verification sequence is
  unchanged after lease acquisition.
- Durable lease heartbeat, renewal, expired-lease recovery, retry counters,
  backoff, and dead-letter handling remain unimplemented until V3-010. A worker
  crash after one-time credential handoff may still strand the session.

Cloud has no AI runtime and does not own Kubernetes execution or raw runtime
evidence.

## Deployment and gateway truth

Git-based deployment exists and can apply user-provided manifests. Such a
manifest may contain its own Service, Ingress, Gateway, TLS, lifecycle, or
shutdown configuration; those resources are user-owned input, not an
Opsi-managed gateway. `IngressEnabled` was removed from active contracts/config,
with a fail-fast error retained for old configuration.

Phase 4 work for exact Git SHA delivery plus Opsi-rendered Deployment,
ClusterIP Service, Traefik `ExposureSpec`, conflict checks, readiness, and
rollback has not started.

## E2E and production evidence

`scripts/e2e/verify-k3s.sh`, `make verify-e2e-k3s-preflight`, and
`make verify-e2e-k3s` define the protected clean VPS/K3s command path. The
incident segment checks factual incident list, detail, resolve, and resolve
audit. The command path exists, but no committed real-infrastructure pass
artifact currently proves the complete scenario. Status remains
`MANUAL_GATED`.

Production readiness remains unproven. Current gaps include repeatable
control-plane deployment evidence, automatic bootstrap leasing, clean VPS
bootstrap proof, managed gateway, public incident evidence, Safe ActionPlane,
CLI MCP, complete Dev VPS E2E, release hardening, supply-chain evidence, and
measured disaster recovery.

## Ordered next work

V3-010 is the next ordered task: add durable bootstrap lease heartbeat,
recovery, retry, and dead-letter behavior. V3-011 through V3-013 then complete
the remaining control-plane milestone work. M1 has not passed.
IncidentEvidence is Phase 5, Safe ActionPlane is Phase 6, CLI MCP is Phase 7,
and production gates remain later roadmap work.

## Verification commands

From repository root:

```bash
make test
make verify-e2e-k3s-selfcheck
make source-hygiene
make package-source
make verify
```

Go module tests run from `agent/`, `cli/`, `cloud/`, and `contracts/go/`, not
from the workspace root.
