# ADR-007: Safe Human-Approved ActionPlane

Status: accepted for R5-016 source verification; live/UI acceptance deferred.

## Decision

```text
CLI preflight
  -> Agent factual state/risk/challenge
  -> separate interactive CLI approval
  -> Ed25519 ApprovalGrant
  -> Agent verify + lock + state recheck
  -> typed executor
  -> post-check + durable result + audit
```

ActionPlane v1 contains only `restart_workload`, `scale_workload`,
`gateway_reconcile`, and `incident_resolve`. Parameters are typed. The Agent
derives `origin=manual_cli` and the authenticated request/approval actors; caller
bodies cannot supply authority fields. Canonical plan and state hashes exclude
signature and mutable terminal metadata.

Cloud manages only durable public approval-device identity: project, owner,
display name, Ed25519 public key/fingerprint, active/revoked state, timestamps,
trusted actor, and idempotency identity. It never receives a private key,
ApprovalGrant, action body, runtime output, or runtime execution authority.

CLI generates and retains ActionPlane private keys and pending grants in Linux
Secret Service without plaintext fallback. The Darwin CLI still cross-builds,
but Darwin ActionPlane secret operations fail closed with an explicit unverified
backend error until native Keychain acceptance exists; PAT behavior is unchanged.
Preflight, approval, and execution are separate process invocations. Approval
requires a TTY and exact phrase. Execute removes the pending grant before
reporting cleanup success; a deletion failure returns the factual remote status
with `ACTION_SECURE_CLEANUP_REQUIRED`, and an exact replay retries cleanup.

Agent is the execution authority. One SQLite transaction reserves execution,
checks exact/conflicting replay, acquires the target lock, persists the approved
principal/device/grant hash, and creates at most one approved event. Mutation
starts only after a guarded transition persists nonce consumption and execution
start time. Terminal persistence is guarded and releases the lock atomically.
After restart, executing actions stay unresolved and locked until read-only
factual state plus the bounded post-check proves success or failure; recovery
never calls an executor. Resolver failure, R4 risk, stale state, replay, and full
Kubernetes ownership/backend mismatch fail closed.

Deploy, retry, and rollback remain exclusively on the canonical path:

```text
BuildRecord -> Cloud DeploymentJob -> RolloutIntent -> Agent PollJob
  -> rollout reconciliation
```

No direct deployment ActionPlane executor, arbitrary shell/kubectl/SQL/HTTP,
caller manifest/path, MCP executor, AI approval authority, or browser
approve/execute route is allowed.

## Evidence And Limits

R5-016 uses fake Agent/Cloud/Kubernetes adapters, existing Agent SQLite,
disposable PostgreSQL 16 for device durability, race tests, Linux Secret Service
source tests, and four-platform CLI cross-builds. macOS native ActionPlane
Keychain acceptance, UI approval, browser testing, live Agent/K3s acceptance,
and full manual E2E are deferred to R5-017. MCP/AI remain later work. This
decision does not make R5-012, R5-017, MCP, AI, or production readiness complete.
