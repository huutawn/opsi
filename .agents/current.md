# Opsi Current Snapshot

Detailed state: `docs/current_state.md`. Architecture: `docs/architecture.md`.
Requirements: `docs/opsi_srs.md`. Evidence: `docs/status_matrix.md`.

## Active Repair Task

- R5-001 is the only active task. R5-002 has not started.
- The historical archive leak came from the former canonical `package-source`
  recipe archiving working-directory `.` with incomplete exclusions.
- Source-package and release containment is implemented through the Git-aware
  candidate set, shared path/content validation, focused negative tests, and
  pre-publication archive validation.
- Incident status remains `OPERATOR_REQUIRED`: external credential rotation or
  revocation, post-rotation verification, distributed-artifact review, and the
  repository-owner Git history decision have not been performed by this task.
- Operator procedure: `docs/runbooks/credential-incident.md`.

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
- `opsi-cloud admin bootstrap-owner` transactionally creates or reuses the
  normalized first user, organization, canonical project, Owner memberships,
  OAuth identity and/or initial PAT hash in PostgreSQL. A durable singleton
  marker makes exact restart-safe repeats idempotent and conflicting tuples fail
  closed. Raw initial PAT material is written only to an operator-selected
  mode-0600 file and is never printed or audited.
- Opsi has one supported development control-plane deployment path: Docker
  Compose starts PostgreSQL, Opsi Cloud, one Bootstrap Worker, and Caddy. The
  package uses named database and Cloud-data volumes, startup health ordering,
  uniform restart policies, bounded Docker logs, placeholder-only examples,
  and gitignored runtime configuration and initial PAT files.
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

V3-013 must prove clean-VM control-plane deployment and independent service
restart, so M1 has not passed. Per-step resumable bootstrap transitions remain
V3-014.
IncidentEvidence is Phase 5, Safe ActionPlane
Phase 6, CLI MCP Phase 7, and production acceptance later.

## Verification

Run `make test`, `make verify-e2e-k3s-selfcheck`, `make source-hygiene`,
`make package-source`, and, when the pinned toolchain/dependency cache permits,
`make verify`.
