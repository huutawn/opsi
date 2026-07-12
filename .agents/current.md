# Opsi Current Snapshot

Detailed state: `docs/current_state.md`. Architecture: `docs/architecture.md`.
Requirements: `docs/opsi_srs.md`. Evidence: `docs/status_matrix.md`.

## M0 State

- Phase 1 V3-001 through V3-007 removed Cloud AI runtime, Agent analyzer/fallback
  RCA, RCA-backed execution, analyze/approve contracts and user surfaces,
  Nginx-specific incident mitigation, fake ingress config, and tracked runtime
  credential/config artifacts.
- Active incident behavior is factual list/get/resolve with authorization,
  deterministic bounded sanitized context, MTTR, and resolve audit.
- Historical `rca_result` and `mitigation_actions_json` columns are storage-only;
  active runtime does not read, expose, or execute them.
- Cloud has no AI provider/runtime. Agent has no LLM/provider/prompt path.
- `IncidentEvidence v1`, Safe ActionPlane, and `opsi mcp serve` are not
  implemented.
- Opsi does not render or manage Ingress, Gateway API, domains, or TLS. User
  manifests may contain their own resources.
- Clean VPS/K3s automation checks incident list/get/resolve and resolve audit,
  but no committed real-infrastructure pass artifact exists. Production
  readiness remains unproven.

## Implemented Boundaries

- Browser core workflows use the CLI local backend and short local sessions;
  usable PATs remain in OS keychain.
- Cloud owns identity/project/membership, registration, bootstrap/deployment job
  envelopes, OTP, audit/control-plane metadata, and Postgres durability where
  configured. It does not own runtime execution or raw runtime evidence.
- Agent owns deployment, service runtime, secrets, telemetry, factual incidents,
  local audit, and K3s/containerd execution.
- Bootstrap Worker is a long-running, single-concurrency daemon. It polls Cloud,
  atomically leases the oldest eligible bootstrap session, increments a bounded
  attempt count, renews the lease with authenticated heartbeats, and binds
  progress and finish calls to the worker identity and one-time lease token.
- Worker configuration no longer accepts a fixed `session_id`. Durable lease
  recovery persists retry backoff and moves exhausted or permanent failures to
  `dead_letter`. Credential handoff is non-destructive across retry attempts;
  registration tokens rotate per attempt. Owner/Admin manual retry is
  idempotent and requires an available credential.

## Next Ordered Work

V3-010 implements restart-safe bootstrap lease/retry semantics in code and
tests. V3-011 is the next ordered task. M1 has not passed because V3-011 through
V3-013 remain. Per-step resumable bootstrap transitions remain V3-014.
IncidentEvidence is Phase 5, Safe ActionPlane
Phase 6, CLI MCP Phase 7, and production acceptance later.

## Verification

Run `make test`, `make verify-e2e-k3s-selfcheck`, `make source-hygiene`,
`make package-source`, and, when the pinned toolchain/dependency cache permits,
`make verify`.
