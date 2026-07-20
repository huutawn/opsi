# R5-010 immutable deployment acceptance

Status: local functional gates pass; live execution is blocked by product
`AUTH_REQUIRED` and an Agent preflight reporting `cloud_connected=false`.

## Safety boundaries

- Do not reset the VPS, reinstall K3s, alter Cloudflare/DNS/TLS, or delete
  non-Opsi workloads.
- Establish the Agent ED25519 key in a dedicated strict `known_hosts` file and
  stop on any mismatch.
- Never read a PAT from history/logs or place it in argv. If authentication is
  absent, the operator runs `opsi login --pat-file <protected-path>`.
- SSH and kubectl are corroboration only. The authoritative mutation is
  `opsi deploy apply` -> Cloud DeploymentJob -> Agent.

## Local gate

1. Run Go test/vet for contracts, Agent, Cloud, and CLI.
2. Run focused race tests for immutable job lease/progress/result and renderer.
3. Run PostgreSQL migration/restart/concurrency tests with a disposable DSN.
4. Run UI lint/build and headless Local UI acceptance.
5. Run staging validators, source hygiene, `git diff --check`, and secret scans.
6. Build the Agent release twice and compare bytes/checksums/release metadata.

## Live gate

1. Read the accepted BuildRecord through the product API; use its canonical OCI
   repository and digest without substituting prompt metadata.
2. Confirm current topology, policy, runtime, node, Agent freshness, and exact
   R5-009 routing for the selected service/environment.
3. Run `opsi deploy dry-run`, `diff`, then `apply` with one stable idempotency
   key. Record only non-secret IDs, hashes, revisions, and digest.
4. Confirm the Local UI resolves the same job, target, digest, events, state,
   attempt, and result. Reapply the same identity and confirm `reused=true`.
5. Read-only SSH corroborates Opsi ownership, Deployment/Service uniqueness,
   pod readiness, application container imageID, and absence of clone/build
   artifacts.
6. Restart only the Agent service through the supported atomic release
   lifecycle. Confirm heartbeat recovery, durable job/result, running workload,
   and no duplicate resources.

R5-010 is not DONE until every live gate has recorded sanitized evidence.
