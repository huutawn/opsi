# BUG-R5-015-EVIDENCE-FACTUALITY-002

Status: deferred from R5-016

R5-015 IncidentEvidence has factuality gaps that are intentionally not corrected in R5-016:

- Mixed pod digests currently select a digest in lexical order and can hide a mixed rollout.
- Kubernetes event selection is pod-biased and can omit Deployment, ReplicaSet, or Service events.
- Individually bounded sections can still produce a body larger than the 256 KiB total limit.

These are evidence factuality issues, not blockers for the R5-016 ActionPlane security or correctness model.
