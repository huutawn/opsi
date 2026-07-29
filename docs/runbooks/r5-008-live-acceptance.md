# R5-008 Live Acceptance Evidence

Recorded 2026-07-19 for the disposable fixture `huutawn/opsi-r5-005-fixture`.
This runbook contains only IDs, hashes, digests, typed outcomes, and sanitized
metadata. It does not contain PATs, private keys, webhook secrets, OIDC JWTs,
GitHub tokens, JWKS responses, or raw workflow logs.

## Revisions and Pins

- Opsi code-bearing revision used by the live helper/verifier: `b1435f0029e0ad65c019ff692bfa80e1f2aa1476`.
- Final Opsi checkout and origin: see repository revision after this evidence commit.
- Final fixture `main`: `c0ae78e0c1b5df93ae0f67a4de860849cbf71c97`.
- Generated canonical workflow source pin: `f782c84f60c1d657b11e7a74a2bd55f6c2ae31e1`.
- Actions are pinned to full commit SHAs; the product workflow has plan/build and trusted publish/record jobs.
- Trusted publish permissions are job-scoped `contents: read`, `packages: write`, and `id-token: write`; plan/build has `contents: read` only.

## Identity and Runtime

- Fixture repository `1304594095` is public, active, default branch `main`, and claimed by project `proj-b1b9ba6457f59185`.
- Installation `147333403` is active and unsuspended; `api` and `worker` bindings are active.
- Production audience is exactly `https://opsidev.site/v1/build-records`.
- Production policy contains exact repository, service, workflow, ref `refs/heads/main`, event `push`, and OCI repository entries. The observed direct-workflow `job_workflow_ref` is exact-allowlisted.
- Final Cloud image: `ghcr.io/huutawn/opsi-cloud@sha256:c3c63a1724a8b17876c200251293156773b172b782257811c8d3d848eac61bf6`.
- Staging: 4/4 healthy; public `/health` HTTP 200; PostgreSQL/Cloud named volumes unchanged; Worker and reverse proxy were not recreated during Cloud-only updates; secret-marker count 0.

## Baseline Proof

Run `29676422752`, attempt `2`, SHA `0a0fd25bc40b83f0ca5b3d1a143a9b6a69386b41`:

| Service | BuildRecord | OCI digest |
| --- | --- | --- |
| `api` | `br-21170479e7f2bda0a9b2ef89ef821b47` | `sha256:8f047360501903f63616236c592f5721ffc027cf548ac74d02205f8985e89654` |
| `worker` | `br-9e549bbbaecdd41f4264d03c2c573a30` | `sha256:ffbcdfb7cef0a50353c81fdffbe31de421905ed1dcde88a507acb4b7403717e2` |

Both anonymous GHCR manifest requests returned HTTP 200 and the registry
`Docker-Content-Digest` exactly matched the corresponding BuildRecord. Both
records carried repository `1304594095`, owner `143307746`, ref `refs/heads/main`,
event `push`, workflow ref
`huutawn/opsi-r5-005-fixture/.github/workflows/opsi-cd.yaml@refs/heads/main`,
run `29676422752` attempt `2`, platform `linux/amd64`, config hash
`419314c2e3eaea280cd2e58b41bba84b6472833053aedc7c59db4e359e97a242`, and plan
hash `e65ab35fe0c791da96a310c9dbad67b0b7dc172cff90a5af92189162048d3eb3`.

## Changed-Service Proof

Fixture commit `06a617d8d323c06502f37c1f874e871c7845429b` changed only
`services/api/index.html`. Run `29676722594`, attempt `1`, had successful
`plan`, `build(api)`, and `publish-and-record(api)` jobs; no worker job or
BuildRecord was present. The planner returned `affected_service_keys=[api]`
and reason `service_path_changed`.

The API BuildRecord is `br-c3d7654507dae1383b0e52eebe67eebf` with digest
`sha256:9f02ca2cb19bc61f322ee6174f057d00b3bde17ae787b390d0abbb0d750dea6a`.
Anonymous GHCR manifest-by-digest returned HTTP 200 with that same digest.

## Negative and Safety Proofs

- Negative suite run `29677167827`: wrong audience `401 OIDC_AUTH_INVALID`; body SHA mismatch `403 BUILD_CLAIM_BODY_MISMATCH`; unbound service `403 BUILD_BINDING_INVALID`; wrong OCI `403 BUILD_WORKLOAD_FORBIDDEN`; tag-only digest `400 BUILD_ARTIFACT_INVALID`; exact create `201`; exact replay `200` with the same record ID and `reused=true`; changed payload `409 BUILD_RECORD_CONFLICT`.
- Unallowlisted workflow run `29677168926`: `403 BUILD_WORKLOAD_FORBIDDEN`.
- Wrong-ref run `29677231864`: `403 BUILD_WORKLOAD_FORBIDDEN`.
- Failed-image-build run `29676999186`: successful controlled assertion that the build failed before submission; no BuildRecord had that run ID.
- Pull request run `29677363299`: `plan` and both untrusted build jobs succeeded; `publish-and-record` was skipped.
- Rate limit: 35 bounded requests with one token-hash marker yielded 5 typed `429 BUILD_RECORD_RATE_LIMITED` responses, each with `Retry-After: 60`; no token marker appeared in the response.
- Temporary negative workflows and policy entries were removed after these checks. Final fixture history remains available; no temporary workflow is enabled in the final production path.

## CLI and Local UI

- CLI human and JSON list/detail returned the baseline `api`/`worker` records and the changed-run `api` record with factual IDs, source SHA, run, workflow, platform, OCI repository, and digest.
- Local backend `/api/local/projects/proj-b1b9ba6457f59185/build-records` returned the same two baseline IDs and one changed-run API ID; detail returned the same API digest. The browser source uses only `/api/local/...`, does not store credentials, and exposes no deploy/submit action.
- `opsi/ui` lint/build and BuildRecord browser-local-only tests passed; headless Chrome loaded the local console shell successfully.

## Scope Boundary

No workload was deployed to Agent/K3s. Agent VPS `52.77.226.123` was not contacted. R5-009, R5-010, MCP, and AI work were not started.
