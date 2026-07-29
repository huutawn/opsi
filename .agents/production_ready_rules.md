# Opsi Production-readiness Rules

Production readiness is `UNPROVEN`. Do not claim it until roadmap v3 hardening,
release/DR acceptance, and repeated protected real-infrastructure gates pass with
reviewed redacted artifacts.

## Mandatory boundaries

1. Browser production workflows use `/api/local/...`; long-lived PATs remain in
   CLI OS keychain.
2. Cloud owns control-plane metadata, not runtime execution, raw logs/metrics,
   application secrets, kubeconfig, source code, AI runtime, or AI providers.
3. Agent owns runtime facts and execution. Current incident surface is factual
   list/get/resolve only.
4. Historical RCA/mitigation columns are storage-only and never authority.
5. `IncidentEvidence v1`, Safe ActionPlane, CLI MCP, and managed gateway are not
   implemented at M0.
6. Future AI is user-owned through the CLI-side MCP boundary, cannot connect
   directly to Agent, and cannot execute or approve. Human approval is separate;
   Agent deterministic policy and typed executors are authoritative.

## Mandatory implementation rules

- No fake success or future-as-current claim.
- Every mutation requires project scope, RBAC, audit, request identity, and
  idempotency where retryable.
- Runtime operations execute through Agent or fail closed.
- Secret values never enter command arguments, logs, audit, Cloud metadata, MCP
  output, or AI context.
- Deployment paths reject traversal/symlink escape and unsupported source types
  before runtime work.
- R4/free-form shell, arbitrary kubectl/SQL, host/K3s destructive operations,
  credential export, and autonomous remediation are forbidden for AI origin.

## Active sources

- `docs/opsi_srs.md` — target requirements;
- `docs/architecture.md` — current and target boundaries;
- `docs/current_state.md` — implemented state;
- `docs/status_matrix.md` — evidence-backed status;
- `docs/production_ready/README.md` — acceptance-document index;
- `docs/opsi_roadmap_v3/09_PRODUCTION_HARDENING.md` and
  `docs/opsi_roadmap_v3/10_RELEASE_DR_ACCEPTANCE.md` — future production gates.

Minimum repository checks are `make verify`, `make test`, `make source-hygiene`,
`make package-source`, and the relevant protected real-infrastructure workflow.
