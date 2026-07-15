# Opsi Current Snapshot

Detailed state: `docs/current_state.md`. Architecture: `docs/architecture.md`.
Requirements: `docs/opsi_srs.md`. Evidence: `docs/status_matrix.md`.

## Active Repair Task

- R5-002 is the only active task. R5-003 has not started.
- R5-002 adds a separate production-like staging control-plane profile with
  origin TLS, fail-closed production configuration, isolated service exposure,
  individual read-only secret mounts, offline validation, and a Cloudflare Full
  (strict) operator runbook.
- The development profile remains an independent local HTTP package and its
  Make targets cannot start the staging Compose project.
- The historical archive leak came from the former canonical `package-source`
  recipe archiving working-directory `.` with incomplete exclusions.
- Source-package and release containment is implemented through the Git-aware
  candidate set, shared path/content validation, focused negative tests, and
  pre-publication archive validation.
- Incident status remains `OPERATOR_REQUIRED`: external credential rotation or
  revocation, post-rotation verification, distributed-artifact review, and the
  repository-owner Git history decision have not been performed by this task.
- Operator procedure: `docs/runbooks/credential-incident.md`.
- Staging and Full (strict) procedure:
  `docs/runbooks/staging-control-plane.md`.

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
- The control-plane staging package terminates origin TLS at Caddy. This is
  deployment infrastructure for Cloud and is not an Agent-managed application
  gateway capability.
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
- A separate `deploy/staging-control-plane` package is code/config validated. It
  uses production Cloud/Worker flags, HTTPS public identity, PostgreSQL,
  authenticated worker calls, non-root Cloud/Worker/proxy containers, read-only
  filesystems, bounded logs, named volumes, an internal backend network, and
  file-backed runtime secrets. PostgreSQL, Worker, and Cloud publish no host
  ports; Caddy alone publishes 80/443 and denies internal/metrics paths.
- Live origin TLS, Cloudflare Full (strict), direct-origin restriction, and
  restart/persistence evidence remain `UNPROVEN` and belong to R5-003.
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

R5-003 is the next repair task but has not started. It owns live origin TLS,
Cloudflare Full (strict), direct-origin restriction, and VPS
restart/persistence evidence. IncidentEvidence, Safe ActionPlane, CLI MCP, and
production acceptance remain later ordered work.

## Verification

For R5-002 run focused Cloud tests, `make dev-control-plane-validate-source`,
`make staging-control-plane-validate-source`, `make source-hygiene`, and
`git diff --check`. Runtime staging validation requires operator-created
gitignored secrets and is not evidence of a live cutover.
