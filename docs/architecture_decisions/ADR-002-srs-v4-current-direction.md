# ADR-002: SRS v4 Current Direction

## Status

Superseded.

Superseded by: ADR-003 — User-owned CLI AI and deterministic Agent action
boundary, and by SRS v5.0. The Cloud AI proxy direction below is retained as
historical decision context and is not current architecture.

## Context

The previous SRS described Opsi as local-first with a minimal stateless Cloud relay. The codebase has since grown useful Cloud coordination features: project registry, membership/PAT verification, agent registration, deployment job envelope, OTP, support summary, and AI RCA proxy.

This direction is acceptable only if Cloud remains a coordination plane and does not become the runtime control plane.

## Decision

Adopt SRS v4.0 as the current product requirement contract.

Opsi remains local-first:

```text
Browser UI -> CLI local backend -> Agent gRPC -> local runtime
```

Cloud may own:

- identity and PAT verification;
- project membership and roles;
- node/agent registration metadata;
- bootstrap session/credential envelope;
- webhook relay and deployment job envelope;
- OTP request/verify state;
- notification metadata;
- sanitized AI proxy and support summaries.

Cloud must not own:

- runtime execution;
- raw logs;
- raw metrics;
- app secrets;
- TOTP secrets;
- private keys;
- kubeconfig;
- source code;
- arbitrary operational payloads.

## Consequences

- Local Web UI must migrate core workflows to `/api/local/...`.
- Browser production code must stop importing Cloud registry clients for core workflows.
- Deployment jobs must carry complete `DeploymentIntent v1`.
- Unsupported image-source deploy must be blocked before job creation or implemented end-to-end.
- Cloud inline UI is dev/debug-only.
- HA clustering, conversational AI, natural-language config generation, and drift detection are P2/Future unless explicitly promoted.

## Rejection Criteria

Reject future changes that:

- increase Cloud ownership of runtime state;
- let browser call Cloud directly for deploy/secret/incident/telemetry/logs/runtime audit;
- expose long-lived PATs to browser;
- send raw logs/secrets/kubeconfig/source code to AI;
- allow fake-success UI for unsupported actions;
- claim SRS completion without status matrix evidence.
