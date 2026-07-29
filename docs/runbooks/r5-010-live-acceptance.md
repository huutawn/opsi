# R5-010 immutable deployment acceptance

Status: `DONE / LIVE_ACCEPTANCE_PASS` at revision
`27906f52bfdc1ea0ffb1db5dfb9587e4bbd82fb7`.

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

- Git tag: `r5-010-27906f5`
- Agent version: `0.0.0-r5.010.27906f5`
- Agent binary SHA-256:
  `20686468d5922d78739378fdfa892f9cdc3f900417d0e71b55d85ebb2fba85e5`
- Cloud image:
  `ghcr.io/huutawn/opsi-cloud@sha256:43a04fd265742bdd7d35bf591fecb2a5ac4d20c65fd9f748a607d78e4bca88a5`
- Bootstrap Worker image:
  `ghcr.io/huutawn/opsi-bootstrap-worker@sha256:c552b0a0ac4ef8369c878e5c9435b34da50c4e5a1be3bc4e94f2327c944a7b7b`
- Public prerelease:
  `https://github.com/huutawn/opsi/releases/tag/r5-010-27906f5`

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

## Recorded live evidence

- BuildRecord: `br-c3d7654507dae1383b0e52eebe67eebf`.
- Image: `ghcr.io/huutawn/opsi-r5-005-fixture/api@sha256:9f02ca2cb19bc61f322ee6174f057d00b3bde17ae787b390d0abbb0d750dea6a`.
- Topology: `topo-ee3f2ebe51872ba698534c0252cc9ea3` revision 1.
- Policy: `pol-4c5499f7852fe941e2f187d537156eda` revision 1.
- Target: runtime `rt-2486bf7e46161f77`, node
  `node-c69fe70180d359d7`, Agent `agent-d1e13723f6e06ff7`.
- CLI job: `dep-a8ddc1ac840a6ae8`; UI job:
  `dep-ff93adea6e0f8a0e`; both terminal `succeeded`, and exact replay returned
  `reused=true` without creating another Kubernetes resource.
- Namespace: `opsi-proj-b1b9ba6457f59-env-4245cff4e8ce1f-e035e29615`.
- Deployment and Service:
  `opsi-api-rt-2486bf7e46161f7-d6638c4918`.
- Kubernetes corroboration: one owned Deployment, one owned ClusterIP Service,
  observed generation equals desired generation, one available replica, named
  `app` container ready, and imageID equals the BuildRecord digest.
- Supported atomic Agent upgrade and later `opsi-agent.service` restart kept
  K3s `v1.36.2+k3s1`, node/Agent identity, workload readiness, durable job
  result, and resource uniqueness.
- No Git clone/build process, external exposure resource, secret in evidence,
  VPS/K3s reset, or Cloudflare/DNS/TLS change was observed or performed.

R5-010 is `DONE / LIVE_ACCEPTANCE_PASS`. R5-011 exposure and rollback remain
not started.
