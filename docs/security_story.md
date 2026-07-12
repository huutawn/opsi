# Opsi Security Story

Status: active boundary summary, last updated 2026-07-12. Detailed requirements
are in `docs/opsi_srs.md`; implementation status is in
`docs/status_matrix.md`.

## Current trust model

Opsi is local-first. Agent owns runtime execution, secrets, telemetry, factual
incidents, and local audit. CLI local backend owns the Browser mediation boundary
and OS-keychain PAT access. Cloud owns identity, membership, registration,
bootstrap/deployment envelopes, OTP, and durable control-plane metadata.

Cloud has no AI runtime, model/provider integration, prompt path, or RCA fallback.
Agent has no AI analyzer or RCA-backed executor. Historical RCA/mitigation data
is storage-only and is never execution authority.

## Credentials and secrets

- Cloud stores PATs as bcrypt hashes; the CLI stores the usable PAT in the OS
  keychain. The Browser must not receive a long-lived PAT.
- Secret reveal requires Owner plus OTP/TOTP and a trusted local path.
- PATs, OTP/TOTP material, Agent tokens, device private keys, kubeconfig,
  application secrets, and approval grants must not appear in logs, audit,
  Cloud runtime metadata, MCP output, or AI context.
- Secret values are supplied to Kubernetes through stdin/API data, not process
  command arguments.

## Authorization and audit

- Every operation is project-scoped. Owner/Administrator lifecycle actions,
  Developer deployment/service actions, and Viewer read-only access are enforced
  at the owning boundary.
- Sensitive actions and denials write redacted audit records. The Postgres Cloud
  path uses append-only protections for control-plane audit.
- Retryable mutations require request identity/idempotency; authorization must
  not be inferred from user-supplied role text alone when auth is enabled.

## Future user-owned AI boundary

The planned AI bridge is local and user-owned through `opsi mcp serve`. It is not
implemented at M0.

- MCP returns bounded, structured, redacted evidence and excludes all credentials
  and secret values.
- Application output, logs, commit messages, events, labels, and AI output are
  untrusted. They must be tagged, redacted, bounded, and isolated from policy and
  approval instructions.
- AI must not connect directly to Agent, approve an action, receive an
  ApprovalGrant, or invoke an execute tool.
- Human approval occurs outside the AI/MCP channel through the trusted Local UI
  or interactive CLI.
- Agent owns deterministic policy, risk classification, preflight, grant
  verification, locks, typed allowlisted execution, post-check, and audit.
- R4 operations are forbidden. Free-form shell/kubectl, arbitrary SQL,
  `kube-system` mutation, K3s uninstall, host deletion, credential export,
  firewall/package mutation, database mutation, and autonomous destructive
  remediation are not made valid by approval.

## Data minimization

Cloud must not persist raw logs, raw metric streams, app secret values,
kubeconfig, private source code, or unrestricted manifests. Future
`IncidentEvidence v1` remains Agent-owned and contains only bounded facts,
redacted excerpts, hashes, and sanitization/prompt-injection metadata.

## Current security limitations

Production readiness remains unproven. Missing evidence includes the complete
clean VPS pass, managed gateway security, public evidence API, Safe ActionPlane,
CLI MCP hardening, release provenance, and repeated recovery/acceptance runs.
