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

## Staging Internal Worker Transport Addendum

Status: accepted temporary exception, 2026-07-16.

Production Bootstrap Worker configuration requires an HTTPS `cloud_url` by
default. The production-like staging Compose profile may explicitly set
`allow_insecure_internal_cloud_url=true` only with the exact endpoint
`http://cloud:9800`. This opt-in is valid only while all of these deployment
controls remain authoritative:

- Cloud and Bootstrap Worker share the Compose `backend` network;
- `backend` is declared `internal: true`;
- neither Cloud nor Bootstrap Worker publishes a host port;
- public traffic terminates TLS at Caddy, and `agent_cloud_url` remains HTTPS;
- the staging source/runtime validator checks the opt-in and network boundary.

The hostname `cloud` and port `9800` alone do not prove isolation. The Worker
validator therefore fails closed without the explicit opt-in and rejects the
opt-in for any other URL or non-production configuration. This is not a general
production HTTP waiver.

Owner: Bootstrap Worker and staging control-plane maintainers. Reason: the
current Cloud container serves its private control endpoint over HTTP behind the
staging TLS proxy. Removal condition: once the staging Worker-to-Cloud link has
an internal TLS listener and trust distribution, remove the opt-in field and
the HTTP exception in the same change. Focused Go tests cover default rejection,
the exact staging opt-in, wrong endpoints, and non-production use; staging
validator tests cover the opt-in and internal Compose network.
