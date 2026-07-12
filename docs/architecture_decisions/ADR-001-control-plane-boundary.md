# ADR-001: Control Plane Boundary

## Status

Partially superseded.

Superseded by: ADR-003 — User-owned CLI AI and deterministic Agent action
boundary. The local-first Browser/CLI/Agent boundary remains accepted; the
Cloud AI ownership statement below is historical and no longer active.

## Decision

Opsi is local-first.

- Local Web UI served by `opsi start` is the primary production UX.
- Browser core workflows call the local CLI backend, not Cloud directly.
- CLI owns PAT storage through the OS keychain and Agent gRPC presentation/proxying.
- Agent owns runtime execution, deployment state, service binding runtime, secrets, telemetry, incidents and audit.
- Cloud owns identity, project membership, node/agent registration, deployment intent/job envelopes, OTP, notification and AI proxy.
- Cloud inline UI is dev/debug-only.

## Consequences

- Cloud deployment jobs must be consumed by an Agent runner before they count as deployable product flow.
- Cloud may store sanitized deployment intent/result metadata, but not raw logs, raw metrics, kubeconfig, app secrets or long-lived operational payloads.
- UI work should prefer adding CLI local API facade endpoints over making the browser call Cloud registry APIs directly.
