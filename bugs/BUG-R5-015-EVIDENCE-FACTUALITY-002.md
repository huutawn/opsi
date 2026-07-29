# BUG-R5-015-EVIDENCE-FACTUALITY-002

Status: R5_015_AGENT_SERVICE_IDENTITY_PASS / LIVE_AGENT_PENDING

R5-015 IncidentEvidence has factuality gaps that are intentionally not corrected in R5-016:

- Mixed pod digests currently select a digest in lexical order and can hide a mixed rollout.
- Kubernetes event selection is pod-biased and can omit Deployment, ReplicaSet, or Service events.
- Individually bounded sections can still produce a body larger than the 256 KiB total limit.

Source fix: application-container digests are authoritative only after an exact
Deployment -> ReplicaSet -> Pod ownership graph validates the Pod; mixed/incomplete
digests remain per-Pod only with bounded partial reasons, Kubernetes events use
the same Opsi ownership graph, and final evidence fitting keeps encoded bodies
within 256 KiB before hashing.

Identity boundary: Agent-local telemetry and rollout/evidence lookup use the
canonical `opsi.dev/service` ServiceKey (`api`). `IncidentRecord.ServiceID`
currently carries that Agent ServiceKey. Separating Cloud ServiceID from
ServiceKey is a separate contract migration and is not part of this corrective;
no Cloud `svc-*` value is inferred from a key or resource name.

Documented status: `R5_015_AGENT_SERVICE_IDENTITY_PASS / LIVE_AGENT_PENDING`. No live Agent workload
acceptance was performed in this task.
