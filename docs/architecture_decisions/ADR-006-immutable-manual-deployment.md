# ADR-006: Immutable manual production deployment

Status: Accepted; R5-010 reached `DONE / LIVE_ACCEPTANCE_PASS`; R5-011-S4
removed the legacy executable path and unified BuildRecord delivery with rollout.

## Context

Before R5-011-S2, the deployment job and Agent engine also owned a legacy
development path that cloned Git source, built a Dockerfile, and applied
repository-provided Kubernetes manifests. R5-010 required a production path whose authority comes
from an accepted BuildRecord and the exact R5-009 topology/policy decision.

## Decision

The existing Cloud `DeploymentJob` and Agent rollout engine remain the single
authoritative orchestration path. Every new BuildRecord deployment creates a
`rollout` job with a canonical `RolloutIntent`; no new executable
`immutable_image` job is created. The Agent rollout branch accepts only
`repository@sha256:<digest>`, pulls without Git or Docker build input, renders
one Opsi-owned Deployment and ClusterIP Service, and verifies readiness and the
application container image ID by the canonical container name `app`.

Cloud revalidates BuildRecord, active binding, topology, policy, routing, and
the exact node/Agent immediately before the first Agent command. The target is
never changed after lease. Progress is monotonic and lease-bound; terminal
results are immutable. Automatic lease recovery and the explicit retry API
reuse the same job ID. Explicit retry is limited to lease-exhausted jobs that
have no Agent terminal result.

R5-011-S4 removed the BuildRecord direct immutable execution branch in addition
to the service-scoped Git path, direct Agent deployment RPC, raw
Kubernetes input, generic GitHub push relay, and other legacy job execution.
Historical rows remain readable only for restore compatibility and fail closed
if queued; they cannot reach Agent execution.

Workloads without external exposure use an empty `ExposureSpec` and empty hash;
the renderer creates only the Opsi-owned Namespace, Deployment, and ClusterIP
Service. Existing authoritative exposure is preserved on image redeployments.

## Consequences

- PostgreSQL receives append-only columns on the existing job/event tables.
- CLI and browser use the same project deployment endpoints through Cloud and
  the loopback Local API.
- Private registry credentials have a typed provider boundary but no plaintext
  or argv fallback; R5-010 live acceptance uses anonymous public GHCR.
- Resource ownership collisions fail closed rather than adopting foreign
  Kubernetes objects.
- Agent upgrade evidence must use the existing checksum-addressed atomic
  release lifecycle; an unsupported copy-only replacement is not acceptance.

## R5-011-S2 implementation record

As of 2026-07-23, `PollJob` carries only canonical immutable deployment or node
lifecycle work. The route may retain the historical `/webhooks/next` transport
name, but it is not a generic webhook relay. Deployment authority is accepted
BuildRecord -> exact topology/policy/routing -> durable
DeploymentJob/RolloutIntent -> ReconcileRollout -> ProductionAdapter -> Opsi-owned
K3s resources -> factual readiness/known-good rollback.

## R5-011-S4 correction record

As of 2026-07-24, the discovered blocker was direct BuildRecord execution
through active `immutable_image` jobs. New BuildRecord deployments persist one
canonical `RolloutIntent`; Agent commands without that intent fail with
`LEGACY_DEPLOYMENT_RETIRED` before Kubernetes mutation. Delivery is exclusively
`PollJob` -> `ReconcileRollout` -> `ProductionAdapter` -> K3s readiness and
factual known-good rollback. Historical immutable rows remain readable, while
queued legacy rows are terminalized and skipped. No VPS, DNS, TLS, public
endpoint, R5-011, or R5-011.4 acceptance was performed.
