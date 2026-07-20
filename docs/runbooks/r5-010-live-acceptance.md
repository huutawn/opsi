# R5-010 immutable deployment acceptance

Status: local functional gates and Cloud staging upgrade pass. Agent execution
is blocked by product `AUTH_REQUIRED` and GitHub prerelease publication `401`.
The installed R5-004 Agent reports a hard-coded `cloud_connected=false`; final
revision `4b7fe54` changes this to factual heartbeat/poll connectivity and must
be installed through the supported upgrade command before acceptance.

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

Current immutable release inputs:

- Git tag: `r5-010-4b7fe54`
- Agent version: `0.0.0-r5.010.4b7fe54`
- Agent binary SHA-256:
  `f25d00735dc7a92611b15986eea03fa050cb8893ee27a2e9485d9890503a6799`
- Cloud image:
  `ghcr.io/huutawn/opsi-cloud@sha256:d3bacfc86d879a802a8912d7c11490a9f0f4468c83092d4863883acdad7ce704`

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
