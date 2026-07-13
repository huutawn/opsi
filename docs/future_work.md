# Opsi Future Work

Status: roadmap classification, last updated 2026-07-13. Items in this document
are not current implementation claims.

## Planned before Production MVP

- GitHub App control plane: user authorization, installation authentication,
  per-App webhook verification, repository ID ownership, and project/service
  mapping without binding repositories to Agents.
- Trusted artifact CD: GitHub Actions build/test, OCI registry push, verified
  GitHub Actions OIDC, claim-bound `BuildRecord`, allowlisted
  `DeploymentPolicy`, durable routing, and Agent deployment of
  `registry/repository@sha256:<digest>`.
- Digest-only image-source deployment with scoped Agent registry pull
  credentials. Git SHA remains provenance, mutable tags are not production
  identity, and the current Git clone/build path remains legacy/manual during
  migration.
- Same-repository PR previews with explicit policy, fork fail-closed behavior,
  isolated environments, no production secrets, and TTL cleanup.
- `IncidentEvidence v1`: deployment diff, health/metric/event timeline,
  redacted log fingerprints/excerpts, topology impact, evidence hash, and
  prompt-injection tagging.
- Safe ActionPlane: versioned plan/preflight/challenge/grant/result contracts,
  deterministic risk policy, separate human approval, typed executors, locks,
  post-check, rollback status, and audit.
- CLI-side user-owned MCP bridge with read/preflight/challenge-request tools and
  no execute or approve tool.
- Typed Traefik `ExposureSpec`, Opsi-rendered Deployment and ClusterIP Service,
  hostname/path conflict checks, gateway readiness, and rollback.
- Complete protected Dev VPS E2E with redacted real-infrastructure artifacts.
- Production security hardening, signed release/supply-chain evidence,
  private registry, backup/restore, upgrade/rollback, disaster recovery, and
  repeated acceptance.

## Post-v1

- HA and multi-node K3s operation.
- Additional managed databases and full data lifecycle guarantees.
- Provider-specific notification, identity, and infrastructure integrations.
- Multi-cloud provisioning.
- Generic Helm charts and arbitrary manifest workflows.
- Conversational product chat and autonomous multi-step workflows.

User-owned AI remains an optional client of Opsi's local MCP boundary. Opsi does
not plan a Cloud-owned conversational AI runtime.
