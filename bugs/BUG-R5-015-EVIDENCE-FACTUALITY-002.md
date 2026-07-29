# BUG-R5-015-EVIDENCE-FACTUALITY-002

Status: SOURCE_FIXED / LIVE_AGENT_PENDING

R5-015 IncidentEvidence has factuality gaps that are intentionally not corrected in R5-016:

- Mixed pod digests currently select a digest in lexical order and can hide a mixed rollout.
- Kubernetes event selection is pod-biased and can omit Deployment, ReplicaSet, or Service events.
- Individually bounded sections can still produce a body larger than the 256 KiB total limit.

Source fix: application-container digests are authoritative, mixed/incomplete
digests remain per-Pod only with bounded partial reasons, Kubernetes events use
an exact Opsi ownership graph, and final evidence fitting keeps encoded bodies
within 256 KiB before hashing.

Documented status: `SOURCE_FIXED / LIVE_AGENT_PENDING`. No live Agent workload
acceptance was performed in this task.
