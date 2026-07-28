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

CLI generates and retains the private key in Linux Secret Service or macOS
Keychain without plaintext fallback. Preflight, approval, and execution are
separate process invocations. Approval requires a TTY and exact phrase; execute
retrieves the pending grant from secure storage and removes it after a terminal
result.

Agent is the execution authority. It verifies project/principal/device and
Ed25519 signature, enforces expiry/nonce/hash/risk policy, locks per target,
rechecks factual state inside the lock, executes one internally constructed
typed operation, performs a post-check, and persists the terminal result and
audit before releasing the lock. Resolver failure, R4 risk, stale state,
replay, and ownership mismatch fail closed.

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
disposable PostgreSQL 16 for device durability, race tests, and four-platform
CLI cross-builds. UI approval, browser testing, live Agent/K3s acceptance, and
full manual E2E are deferred to R5-017. MCP/AI remain later work. This decision
does not make R5-012, R5-017, MCP, AI, or production readiness complete.
