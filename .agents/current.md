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
- Bootstrap Worker currently has a narrow `RunOnce(session_id)` Ubuntu
  first-server path; it is not a poll/lease daemon.

## Next Ordered Work

After V3-008/M0 review, Phase 2 starts at V3-009: convert Bootstrap Worker to a
poll/lease daemon. Do not claim V3-009 is in progress. IncidentEvidence is Phase
5, Safe ActionPlane Phase 6, CLI MCP Phase 7, and production acceptance later.

## Verification

Run `make test`, `make verify-e2e-k3s-selfcheck`, `make source-hygiene`,
`make package-source`, and, when the pinned toolchain/dependency cache permits,
`make verify`.
