# ADR-008: Agent runtime truth and exact known-good rollback

Status: Accepted for R5-011.2 local reconciliation and rollback.

## Context

Cloud deployment metadata records authority and intent, but it cannot prove
that Kubernetes currently runs the requested application digest or routes the
owned Ingress to the correct Service endpoints. Agent restart can occur after
any Kubernetes mutation and before the next control-plane checkpoint.

## Decision

The existing Agent `deploy.Engine`, `ProductionAdapter`, and deployment SQLite
store remain the single runtime path. A versioned rollout intent is written to
the existing SQLite database after read-only ownership preflight and before
mutation. The WAL has one active rollout per
project/environment/runtime/service target, allowlisted monotonic states,
append-only versioned events, bounded attempts/errors, immutable terminal
results, and state hashes. It stores contracts and evidence hashes, never raw
manifests, kubectl output, lease tokens, credentials, certificates, or keys.

Kubernetes create/update uses the existing renderer and command boundary.
Absence is success plus empty output from `--ignore-not-found`; non-zero reads
are failures. The adapter captures UID/resourceVersion/functional hash and
uses create-or-replace compare-and-swap semantics. A changed UID/version/hash,
foreign owner, foreign spec field manager, unsupported Ingress metadata, or
host/path collision fails closed. It never uses force conflicts or deletes a
foreign resource.

Known-good is committed atomically with `succeeded` only after factual runtime
readiness: Deployment observed generation/replicas, named `app` container and
exact imageID digest, exact Service selector/port and ready endpoints, exact
owned Traefik Ingress, and a bounded trusted local routing probe. Public
readiness is a separate evidence field and is not claimed in R5-011.2.

Rollback uses only the exact previous known-good snapshot referenced by the
WAL. It re-renders and revalidates that immutable digest/workload/exposure and
ownership, reapplies idempotently, then repeats readiness before
`rolled_back`. Missing/corrupt known-good ends in typed `failed`; rollback
apply/readiness failure ends in `rollback_failed`.

On restart, the Agent scans bounded nonterminal WAL records and resumes the
same rollout ID from actual Kubernetes state. Cancellation is terminal only
before mutation; after `applying`, reconciliation or rollback continues under
a bounded internal recovery context.

## Consequences

- Display-only Exposure metadata does not participate in runtime hashes; the
  R5-011.1 metadata-inclusive hash remains decode-compatible and canonicalizes
  to the functional hash.
- R5-011.2 has local/disposable Kubernetes proof only. Cloud persistence/API,
  CLI/UI, DNS/certificate lifecycle, and live public endpoint proof remain
  R5-011.3/R5-011.4.
