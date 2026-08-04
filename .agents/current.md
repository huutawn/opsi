# Opsi Current Snapshot

The consolidated manual backlog source fixes are implemented locally.
`R5_014_UI_REWORK_SOURCE_PRESENT / PROJECT_REFRESH_AND_ERROR_GATE_PASS /
REPOSITORY_VERIFY_TOOLCHAIN_BLOCKED`.
`R5_012_SOURCE_FIXED / LIVE_RETEST_PENDING`.
`R5_015_AGENT_SERVICE_IDENTITY_PASS / LIVE_AGENT_PENDING`.
`R5_016_SOURCE_FIXED / LIVE_AGENT_AND_UI_DEFERRED_TO_R5_017`.
`R5_017_BARRIER_SECURITY_CORRECTION_SOURCE_PRESENT /
ALIGNED_RUNTIME_REPUBLISH_REQUIRED / NEW_RUN_1_PENDING`.
`R5_017_NODE_RETIREMENT_IDEMPOTENCY_SOURCE_FIXED / RUNTIME_PUBLISH_PENDING`.
`R5_017_KNOWN_GOOD_HISTORY_VALIDATION_SOURCE_FIXED /
ALIGNED_RUNTIME_REPUBLISH_REQUIRED` follows commit `7623266f…`, which fixed
cross-target inheritance and failed-job poisoning. Review before republishing
found incomplete historical-row validation; Cloud now also rejects legacy,
malformed, or internally inconsistent exact-target rows unless rollout mode,
current schemas, canonical intent/snapshot, factual success or rollback, and
normalized terminal fields agree through one shared memory/PostgreSQL
predicate. Run `r5-017-run1-20260802T142745Z` remains blocked and immutable;
deployment `dep-255109f89b9efb64` remains terminal `failed` and was not retried,
rewritten, or migrated. The correction is not deployed. A new aligned runtime
publication and a new live Run 1 with a new Run ID are required; R5-017, release
readiness, and production readiness remain unclaimed.
`R5_017G_CONTROL_PLANE_PUBLISH_PASS` replaces the Worker-only workflow with one
canonical Cloud + Bootstrap Worker publisher; run `30750598790` records the
published `246c924f56f7d28db43d06154b39c558f214c686` manifest and digests.
One Agent-only publisher source now derives an immutable prerelease from the
same exact reviewed revision, proves two clean builds byte-identical, and
verifies the three public assets anonymously. It does not add another Cloud or
Worker publisher.
This source correction did not mutate staging or Agent VPS state, deploy a
runtime, or perform new live UI/browser acceptance.
ActionPlane restart recovery is one non-blocking root-context loop with an
immediate pass, five-second default retry, 30-second pass budget, and bounded
per-record opportunity. It is read/post-check only and retains unresolved locks until factual completion;
reservation/completion are guarded SQLite transitions. Kubernetes reads
require authoritative full ownership identity. Linux Secret Service is the
source-tested ActionPlane backend; Darwin ActionPlane secret operations fail
closed pending native acceptance, while PAT behavior remains unchanged.
The canonical mapping is
`docs/manual_ui_parity_matrix.md`; all 21 R5-013 supported capabilities have a
Local route/view and the three backend gaps remain disabled. Installed bundles
include `opsi-ui`; Agent-live acceptance is deferred to R5-017 because the
former Agent VPS no longer exists. R5-012 still requires a live delivery retest.

Detailed state: `docs/current_state.md`. Architecture: `docs/architecture.md`.
Requirements: `docs/opsi_srs.md`. Evidence: `docs/status_matrix.md`.
Canonical roadmap: `docs/opsi_roadmap_v5_production.md`.

### R5-017G — unified immutable control-plane publisher

- One manual workflow publishes both `cloud` and `bootstrap-worker` targets
  from `cloud/Dockerfile`, root context, `linux/amd64`, and the same full
  reviewed revision.
- Exact revision tags are built only when absent. Existing tags are reused only
  after repository, target/component, platform, OCI labels, and one lowercase
  SHA-256 digest validate; mismatches fail closed.
- One strict combined manifest is created and uploaded only after both images
  and immutable references cross-check. The old Worker-only workflow and its
  publisher-specific test assumptions are removed.
- Source gates are required before publish. No staging deploy, Agent/VPS work,
  live idempotency replay, Run 1, or Run 2 is claimed here.

### R5-017H — immutable Agent artifact publisher

- One manual `developer`-only workflow accepts a lowercase full revision and
  exact `publish-agent` confirmation; the revision must equal `GITHUB_SHA`.
- The canonical build script derives `agent-<full-revision>`, emits only the
  Linux amd64 Agent binary, checksum, and strict metadata, and refuses an
  existing output directory.
- Two clean Go 1.26.4 builds must be byte-identical. The immutable prerelease
  refuses an existing tag/release and is re-downloaded without credentials for
  checksum, ELF/amd64, version, embedded revision, metadata, URL, and asset-set
  verification.
- Registration/publication evidence is external to the source commit. No
  staging deploy, Agent/VPS mutation, Run 1, Run 2, R5-017 completion, or
  release-readiness claim follows from this publisher alone.

### R5-017F — durable node retirement replay

- Node retirement now passes the authenticated actor, actual idempotency key,
  and request ID into one authoritative registry operation.
- In-memory replay is serialized under the service lock. PostgreSQL replay uses
  the existing `idempotency_keys` table, an operation/key advisory transaction
  lock, target row locking, conditional state writes, and one transactional
  `NODE_MARKED_OFFLINE` audit for a factual transition.
- Exact replay and a new key against the exact retired state do not churn node,
  Agent, runtime, or project timestamps. Reusing a key for another node returns
  `IDEMPOTENCY_CONFLICT` without mutation or another success audit.
- Memory, HTTP, race, and disposable PostgreSQL restart/concurrency regressions
  pass. No migration, second idempotency store, live request, publish, deploy,
  Run 1 resume, or Run 2 start was performed.

### R5-017D1 — source barrier orchestration

- Normal same-image Worker release remains a health/RepoDigest/Cloud-health
  no-op with no pull, `.env` mutation, backup, or recreate.
- Explicit deploy-only `--force-recreate-same-image` is accepted only for the
  canonical staging barrier override, private placeholder-free run config,
  matching `armed` marker, exact expected digest, and one Worker target. It
  proves container ID replacement, health, immutable RepoDigest, and Cloud
  health without changing `.env`.
- `verify-k3s.sh` remains the operator controller for the loopback Local API,
  Agent VPS bootstrap key, single bootstrap POST, protected local state, and
  later acceptance. It no longer invokes local staging Docker/Compose.
- One committed `staging-barrier-remote.sh` executor owns staging repository,
  Compose, Worker, config, marker, and remote state operations. Pinned direct
  SSH uses a separate protected key/known_hosts/fingerprint and a sanitized
  environment; strict bounded receipts bind the exact revision and helper blob.
- Pre-session failure restores through remote `abort`. Post-session failure
  preserves the factual session and stopped/barrier Worker; continuation uses
  the same state and never sends a second bootstrap POST. Ambiguous mutation
  loss is reconciled only through read-only `status`.
- Local fake-SSH/Docker regressions pass. No image publish, staging deploy,
  live SSH, VPS reset, or E2E run was performed; aligned Cloud, Worker, and
  Agent republication and a new Run 1 remain pending.

### R5-017 barrier security correction

- Pinned SSH now runs one fixed bounded launcher, never the staging
  working-tree helper. The launcher validates the closed non-secret request,
  exact revision, clean tracked worktree/index, repository identity, and
  expected helper blob, then materializes and rehashes the exact committed Git
  object in private temporary files before execution.
- `OPSI_E2E_STAGING_HOST` is only the transport endpoint;
  `OPSI_E2E_STAGING_EXPECTED_HOSTNAME` independently binds remote
  `hostname -f`. Request, receipt, remote-state, and local-state schemas were
  bumped and bind both identities plus expected/executed helper blobs.
- One remote runtime validator proves every durable phase against current
  Worker profile, running/health state, immutable digest, exact container,
  marker, barrier config, and unchanged Cloud/PostgreSQL/reverse-proxy
  containers. State-only status and reconciliation fail closed.
- Proven remote restart/restore completion can be adopted after a local
  protected-state write failure without replaying the mutation or sending
  another bootstrap POST. No live staging, Agent VPS, K3s, PostgreSQL,
  publication, deployment, or Run 1 action occurred; aligned Cloud, Worker,
  and Agent republication remains required.

### R5-017D2 — canonical replay/restore and failure cleanup

- Barrier generation preserves the staging `cloud_url`, forces
  `production: false` and `allow_insecure_internal_cloud_url: false`, and
  writes one private run/session-scoped barrier config without changing the
  production source config.
- Replay and normal restoration use dedicated `barrier-replay` and
  `barrier-restore` operations in `bootstrap-worker-release.py`; both prove
  expected binding/RepoDigest, singleton replacement, Worker health, and
  Cloud health under the release lock. Normal restoration uses base Compose
  only and never pulls or edits `.env`.
- The staging executor reuses the canonical barrier and release helpers; no
  second Worker deployment implementation or local fallback remains. Normal
  restoration is permitted only after completed marker evidence or a proved
  pre-session abort, and reached/consumed/completed evidence is preserved.
- No live publish, staging deploy, SSH, VPS reset, or E2E run was performed.

### R5-015 corrective — R5_015_AGENT_SERVICE_IDENTITY_PASS

- Opsi-managed telemetry accepts only a valid exact `opsi.dev/service`
  ServiceKey and never guesses identity from resource names or Cloud `svc-*`
  values.
- IncidentEvidence uses that same Agent ServiceKey for local rollout lookup and
  exact Deployment -> ReplicaSet -> Pod ownership; only validated Pods affect
  the application digest.
- Missing exact target Pods and zero owned Pods remain bounded partial coverage
  even when matching Deployment or Service events exist. Missing-label,
  mixed-digest, and incomplete-digest evidence is also bounded partial coverage.
  `IncidentRecord.ServiceID` carrying ServiceKey is explicit technical debt for
  a separate contract migration.
- No VPS/live E2E, R5-017, MCP, or production-readiness claim is included.

### Corrective Prompt 07 — UNRESOLVED_ROLLOUT_OWNERSHIP_PASS

- A canonical rollout owns its service until Cloud commits a factual Agent
  `TerminalResult` or safely cancels it before the first lease.
- Lease expiry requeues and renews the same owner. Max-attempt exhaustion keeps
  `DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED` retryable without deleting the lock or
  manufacturing terminal truth.
- In-memory and PostgreSQL acquisition reject another deployment despite an
  expired/missing lock row, while retry renews the same deployment ID and
  bounded attempt window idempotently.
- Cancellation requires zero attempts and prepared/queued history. Disposable
  PostgreSQL 16 tests cover restart, expired/missing rows, retry/create races,
  rollback safety, and factual terminal release without a migration.
- No SSH, live E2E, VPS, DNS, TLS, Cloudflare, R5-012, MCP, or AI action was
  performed. R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`.

### Corrective Prompt 06 — ROLLOUT_FAILURE_PHASE_TRUTHFUL_PASS

- Terminal rollout results carry bounded `pre_mutation` or `post_mutation`
  failure phase; failure-code inequality is not mutation evidence.
- Runner rejects `failed` WAL records with `TerminalAt=nil`, performs one
  bounded same-lease resume, and calls `CompleteDeployment` only for factual
  terminal results (`rolled_back`, `rollback_failed`, terminal failed, success).
- Cloud in-memory and PostgreSQL paths reject forged pre-mutation results after
  mutation progress, keep the service lock until a factual terminal result,
  and persist/replay the phase through existing terminal-result JSON.
- `NO_KNOWN_GOOD` remains post-mutation/no-snapshot only. No database migration,
  rollout state, dependency, route, worker, or legacy delivery path was added.
- No SSH, live E2E, VPS, DNS, TLS, Cloudflare, R5-012, MCP, or AI action was
  performed. R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`.

### Corrective Prompt 05 — PRE_MUTATION_FAILURE_REPORTING_PASS

- Pre-WAL failures after a canonical rollout lease now report deterministic
  `failed` results from the leased `RolloutIntent`, not an empty WAL record.
- Typed rollout codes are preserved; generic preflight uses
  `ROLLOUT_PREFLIGHT_FAILED`. No readiness/resources are fabricated, and the
  factual previous known-good/current digest is retained when present.
- In-memory and PostgreSQL completion accept the strict pre-mutation shape,
  release the service lock, persist one immutable terminal result, accept exact
  replay after restart/lost response, and never turn the cause into lease
  attempts exhausted. `NO_KNOWN_GOOD` remains post-mutation/no-snapshot only.
- The manual E2E incident selector requires matching `service_id` plus
  `created_at_unix` no older than the timestamp immediately before broken B.
- No SSH, live E2E, VPS, DNS, TLS, Cloudflare, R5-012, MCP, or AI action was
  performed. R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`.

### R5-011-S4 — SINGLE_ROLLOUT_EXECUTION_PATH_PASS

- The discovered blocker was active BuildRecord `immutable_image` jobs beside
  rollout reconciliation. New BuildRecord deployments now create only durable
  `rollout` jobs with a canonical `RolloutIntent`.
- Agent commands without an intent fail with `LEGACY_DEPLOYMENT_RETIRED` before
  Kubernetes mutation. `Engine.Deploy` and `ProductionAdapter.Deploy` are no
  longer executable delivery entry points; PollJob enters `ReconcileRollout`.
- Queued historical rows are terminalized without blocking the next canonical
  lease. No-external workloads render no Ingress, while image redeployments
  preserve existing authoritative exposure.
- The local harness requires healthy A -> broken B -> restored A and validates
  exact digests, known-good/readiness/resource evidence, terminal states, and
  final K3s imageID readiness. Its self-test uses a local ssh-keyscan stub and
  JSON fixtures without network access.
- No SSH, VPS, DNS, TLS, release, public endpoint, R5-012, MCP, or AI action was
  performed. R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`.

### R5-011-S3 — MANUAL_ACCEPTANCE_TRUTH_REPAIR

- Correction only: the manual K3s acceptance is PEM-only, pins one exact SSH
  host fingerprint, submits accepted healthy/bad BuildRecords through the
  canonical project deployment route, and rejects PEM material in evidence.
- The false GitHub-hosted K3s workflow, dated readiness snapshot, and obsolete
  future-work duplicate are deleted. Active documents now record one delivery
  path from GitHub Actions OIDC through accepted BuildRecord, immutable digest,
  topology/policy routing, durable job, Agent PollJob, ProductionAdapter/
  ReconcileRollout, Opsi-owned K3s resources, and factual readiness/rollback.
- No SSH, VPS, DNS, TLS, release, public endpoint, R5-012, MCP, or AI action was
  performed. R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`.

### R5-011-S2 — SINGLE_IMMUTABLE_DELIVERY_PATH_PASS

- Direct Git/build/manifest Agent deployment, CLI direct deploy, Local/Cloud
  service-scoped deployment creation, generic GitHub push relay, and Cloud
  inline debug UI/configuration are retired. Only the BuildRecord -> immutable
  digest -> DeploymentJob/RolloutIntent -> PollJob -> ProductionAdapter/
  ReconcileRollout -> K3s readiness/rollback path remains executable.
- Historical deployment columns and relay tables remain for restore/read
  compatibility only; legacy queued jobs are terminalized or skipped
  deterministically and cannot reach an Agent.
- Health command output is bounded to 256 KiB per probe with a five-second
  timeout and fail-closed overflow/truncation behavior.
- Local/PostgreSQL tests, race tests, source hygiene, build, and E2E self-test
  were run. No VPS, DNS, TLS, public endpoint, or full K3s E2E acceptance was
  performed; the full E2E remains `MANUAL_GATED`.
- R5-011 remains `PARTIAL`; R5-011.4 remains `MANUAL_GATED`. No R5-012 or MCP/AI
  work was performed.

## Active Task

### R5-011.1 — Local external exposure contract and renderer

- Starting revision is `9caf01bbbff868546d4065195dd70004c2002ef9` on
  `developer`. R5-010 remains `DONE / LIVE_ACCEPTANCE_PASS` at its recorded live
  evidence and is reused without changing the running workload.
- `ExposureSpec v1` owns bounded project/environment/runtime/service/job
  identity, canonical hostname and Prefix path, exact service port, bounded TLS
  mode/opaque reference, optional display metadata, and deterministic spec hash.
- The existing R5-010 namespace/name/label helpers, exact ClusterIP Service
  renderer result, and `CommandRunner`/kubectl boundary remain authoritative.
  The sole gateway resource is `networking.k8s.io/v1` Ingress with fixed
  `ingressClassName: traefik` and field manager `opsi-r5-011-exposure`.
- Read-only preflight distinguishes create, unchanged, and deterministic owned
  diff, and fails closed for foreign names, backend mismatch, cross-workload
  identity, TLS reference failures, and Opsi/foreign hostname-path conflicts.
- R5-011.1 is `DONE / LOCAL_CONTRACT_RENDERER_PASS` after the recorded local
  contract/Agent test, vet, race, deterministic, source-hygiene, diff, and
  secret-marker checks. R5-011 is not DONE.
- No SSH/VPS/K3s mutation, Ingress apply, external endpoint, Agent release,
  Cloud/Worker deploy, readiness reconciliation, rollback/WAL, Cloud API,
  CLI/UI, DNS/Cloudflare/certificate, MCP, or AI work was performed.

### R5-011.2 — Agent-local durable reconciliation and exact rollback

- Starting revision is `7587d5892fee53844ca50f0ad8f91db3b3d67d81` on
  `developer`; the pre-existing UI test change remains outside this task.
- `ExposureSpec.SpecHash` is now functional-only (display metadata excluded),
  with compatibility decoding for the metadata-inclusive R5-011.1 hash.
  Kubernetes absence uses fixed `--ignore-not-found` plus bounded empty output;
  ingress inventory has timeout, byte/item bounds, strict decode, sorting, and
  fail-closed overflow handling.
- The existing Agent `deploy.Engine`, `ProductionAdapter`, and SQLite store
  now implement versioned rollout intent/WAL/events, one active target lock,
  CAS UID/resourceVersion ownership, factual app-digest/Service-endpoint/
  Ingress/local-routing readiness, immutable known-good snapshots, bounded
  restart reconciliation, and exact automatic rollback. No second renderer or
  deployment engine was added.
- `R5-011.2` verdict is `DONE / LOCAL_RECONCILIATION_ROLLBACK_PASS` after the
  disposable pinned K3s A -> broken immutable B -> exact A verifier, including
  Agent/store restart while B was waiting and one-resource-count proof.
- R5-011.3 still owns Cloud PostgreSQL lifecycle/state plus Agent/CLI/Local
  API/UI wiring. R5-011.4 still owns live public endpoint proof. No VPS,
  Cloudflare, DNS, certificate, MCP, AI, or release action was performed.

### R5-011.3 — Durable Cloud rollout/exposure lifecycle and Local UI

- The existing R5-010 `DeploymentJob`, lease, event, and Agent command path now
  carries immutable `RolloutIntent` authority, including exact digest,
  WorkloadSpec/ExposureSpec hashes, topology/policy/routing authority, and
  expected known-good references. Agent SQLite remains runtime truth.
- Append-only PostgreSQL migration `MigrateR5011Rollout` extends the existing
  deployment tables with rollout intent/state, exposure, digest, known-good,
  readiness, and sanitized terminal result fields. Existing R5-010 rows are
  preserved. In-memory and PostgreSQL services enforce project scope,
  idempotency, target locking, allowlisted monotonic transitions, terminal
  immutability, stale lease rejection, and sanitized bounded metadata.
- Canonical Cloud routes are exposure preview/diff/apply/list/detail plus the
  existing deployment events/status/rollback routes. Manual CLI commands are
  `opsi exposure create|diff|apply|status|history` and
  `opsi deploy rollback|status|list|events`; mutations require explicit
  confirmation and idempotency keys. Browser calls only Local API paths.
- Local acceptance passed success, automatic B -> A rollback, explicit
  rollback, no-known-good, rollback-failed, target/route/ownership/identity
  conflicts, stale/out-of-order/terminal/idempotency/concurrency/security
  negatives, PostgreSQL/backend/Agent restart fixtures, and CLI/Local API/UI
  factual state parity. UI source-state tests, lint, build, and a disposable
  headless Chrome interaction fixture (preview -> apply -> succeeded -> explicit
  rollback -> rolled_back) pass. Playwright is not installed, so the browser
  evidence uses Chrome DevTools Protocol instead.
- Verdict: `DONE / LOCAL_CONTROL_PLANE_UI_PASS` for R5-011.3 only. R5-011
  remains partial; R5-011.4 still owns live Agent/VPS, public endpoint, and
  certificate/DNS acceptance. No VPS, staging deployment, MCP, AI, or R5-012
  action was performed.

### R5-011-S1 — Agent trust boundary and truthful runtime health

- Starting revision was `95227edb6ed3def2c3c5dde209465d9746610ecc` on
  `developer`, with `HEAD == origin/developer` and a clean worktree at entry.
- Agent PAT verification now uses `Authorization: Bearer`, a project-only JSON
  body, digest-keyed identity caching, exact project matching, complete verified
  identity, and expiry-boundary fail-closed behavior. Secret and incident Agent
  RPC authority is derived only from verified Bearer metadata; CLI, Local API,
  and Local UI no longer accept caller-selected user/role/PAT authority.
- Heartbeats and gRPC status use a bounded direct-argv kubectl probe for API
  readiness and Kubernetes node Ready conditions. Runtime failures report
  `not_ready`/`unavailable`, disable deploy capability, and make status
  `degraded`/`unavailable`; Cloud connectivity remains separate.
- UI source tests run through Node's built-in test runner and `make verify`
  includes `ui-test`. Local/code verification passed with Go `1.26.4`, Node
  `24.16.0`, and npm `11.17.0`; the exact revision has not been deployed or
  exercised against a VPS/live public endpoint.
- Checkpoint: `R5-011-S1 — TRUST_BOUNDARY_AND_HEALTH_TRUTH_PASS`. R5-011 remains
  `PARTIAL`; R5-011.4 remains `MANUAL_GATED`, and no Cloud, MCP, AI, or staging
  changes were made.

### R5-009 — Manual placement, DeploymentPolicy, and routing preflight

- R5-009 local acceptance passed on 2026-07-19 with disposable PostgreSQL,
  loopback Cloud, real CLI, Local API, built Local UI, and headless Chrome.
- `TopologyPlan v1` and `DeploymentPolicy v1` use immutable PostgreSQL
  revisions, mutable heads, authenticated audit, bounded exact fields, scoped
  unknown-capacity override, expected revision/state hash, and idempotency.
- Positive route selected exactly one fresh healthy runtime/node/deploy Agent for
  both `api` and `worker`; stale, unknown, oversubscribed, foreign, zero-Agent,
  ambiguous-Agent, wrong-identity, and disabled-policy cases failed closed.
- CLI and Local API/UI returned identical plan/policy hashes. Browser wizard
  preview/apply rendered revision and audit results. PostgreSQL restart and
  concurrent one-winner apply passed.
- No SSH, Agent VPS, reboot/reset/bootstrap, workload, `DeploymentJob`, Agent
  mutation, MCP, AI, or R5-010 work was performed.

### R5-008 — Live GitHub runner, GHCR, and BuildRecord proof

- R5-007 hardening and live R5-008 acceptance passed on 2026-07-19.
- Opsi code-bearing revision: `b1435f0029e0ad65c019ff692bfa80e1f2aa1476`.
- Final fixture revision: `c0ae78e0c1b5df93ae0f67a4de860849cbf71c97`.
- Canonical generated workflow source pin: `f782c84f60c1d657b11e7a74a2bd55f6c2ae31e1`.
- Baseline run `29676422752` attempt `2` selected `api` and `worker`; changed run
  `29676722594` selected only `api`. Public GHCR digests and Cloud BuildRecords
  matched for both runs.
- Cloud staging runs `4/4` healthy on immutable image
  `ghcr.io/huutawn/opsi-cloud@sha256:c3c63a1724a8b17876c200251293156773b172b782257811c8d3d848eac61bf6`.
- Temporary negative workflows/policy were removed after exact live 401/403/400/
  409/replay/rate-limit/failed-build/PR checks. No Agent VPS was used during
  R5-008; R5-009 is recorded separately above.

### R5-007 — GitHub Actions OIDC verifier and BuildRecord v1

- R5-006 remains `DONE / FUNCTIONAL_ACCEPTANCE_PASS`. Its focused R5-007 entry
  review repaired Local repository apply so a bounded safe `Idempotency-Key`
  replays only the same canonical request, conflicting reuse fails typed, and
  apply requires the exact filesystem-bound `preview_hash` returned by preview.
- R5-007 is `DONE / LOCAL_FUNCTIONAL_ACCEPTANCE_PASS / LIVE_EVIDENCE_DEFERRED`.
  Cloud pins GitHub issuer/JWKS, verifies signed bounded claims, authorizes the
  active repository/service binding and exact workload policy, and stores
  append-only `opsi.build_record/v1` rows idempotently in PostgreSQL.
- CLI and Local API/UI expose project-scoped PAT-authenticated BuildRecord
  list/detail only. The browser receives no PAT/OIDC token and has no submit or
  deploy action.
- R5-005 remains `OPERATOR_REQUIRED / FUNCTIONAL_ACCEPTANCE_PASS / LIVE_LIFECYCLE_EVIDENCE_DEFERRED`.
- R5-005 and R5-006 business scope outside the focused repair is frozen.
- The two missing live webhook deliveries (`installation_repositories: removed` and
  `repository`) and the live wrong-user check using a second GitHub account remain
  deferred; no evidence is fabricated and R5-005 is not marked `DONE`.

### R5-004D acceptance status

- `GET /api/projects/{project_id}/nodes` now has one canonical response
  contract: `{"nodes":[...]}`. The CLI rejects malformed or unexpected node
  response schemas without reflecting response bodies or credentials.
- The Cloud-only image update was deployed to staging with an immutable digest;
  the Bootstrap Worker and Agent images were intentionally retained because
  their source did not change.
- R5-004 remains `PARTIAL`. Fedora Secret Service canary store/get/delete and
  the bounded Linux keychain path passed. The final CLI resolved the existing
  `R5-004` project through an idempotent `bootstrap-owner` repeat
  (`reused: true`, no PAT issued), then passed atomic Cloud TLS resolution,
  direct pinned TLS/PAT status, real pin/name/auth negatives, and Local UI
  shared-state proof.
- The old Agent node was decommissioned and only Opsi/K3s-managed paths were
  reset under the trusted ED25519 host key. Recovery session
  `boot-7b843526dff6842b` completed as node `node-c69fe70180d359d7`, but the
  single poller first observed checkpoint `4/register_agent`; it never
  restarted the Worker during `install_k3s`. No second reset, recovery session,
  Worker fault, or target reboot was attempted.

- R5-004 live clean-VPS bootstrap ran on 2026-07-17 at revision
  `d3df6b8d2b3a029ea3f589dfb840ff296e7bdbd5`. The final CLI created one
  durable session and node through Cloud/Worker strict SSH; pinned K3s
  `v1.36.2+k3s1` and Agent `0.0.0-staging.a0d5315` installed, registered,
  reached healthy heartbeat, and survived a controlled target reboot.
- The first live attempt correctly dead-lettered before mutation because Go SSH
  selected an unpinned ECDSA key while only the operator-confirmed ED25519 key
  was trusted. Worker now constrains host-key negotiation to algorithms present
  for that host in `known_hosts`; the same session/idempotency/node completed
  after the supported credential re-submit and manual retry path.
- The first R5-003 public-port start was rolled back because Caddy sorted the
  general HTTP redirect before the loopback health response. Raw evidence showed
  `/health` return 308 to `https://127.0.0.1/health`, after which `wget` followed
  to unused container port 443. Caddy remained running with restart count zero.
- The staging HTTP listener now uses `route` to preserve health-before-redirect
  order. The validator rejects the former unordered form, and a focused
  loopback smoke covers health, redirect isolation, Origin CA TLS, protected
  paths, hardening, and log markers without stopping the dev profile.
- R5-002 added a separate production-like staging control-plane profile with
  origin TLS, fail-closed production configuration, isolated service exposure,
  individual read-only secret mounts, offline validation, and a Cloudflare Full
  (strict) operator runbook.
- The staging validator cross-checks the URL-decoded PostgreSQL DSN username,
  password, and database against the Compose PostgreSQL identity and secret.
  Production Worker HTTP is fail-closed unless the staging-only internal
  endpoint is explicitly opted into and the profile validates its isolated
  backend boundary.
- The development profile remains an independent local HTTP package and its
  Make targets cannot start the staging Compose project.
- The historical archive leak came from the former canonical `package-source`
  recipe archiving working-directory `.` with incomplete exclusions.
- Source-package and release containment is implemented through the Git-aware
  candidate set, shared path/content validation, focused negative tests, and
  pre-publication archive validation.
- Incident status remains `OPERATOR_REQUIRED`: external credential rotation or
  revocation, post-rotation verification, distributed-artifact review, and the
  repository-owner Git history decision have not been performed by this task.
- Operator procedure: `docs/runbooks/credential-incident.md`.
- Staging and Full (strict) procedure:
  `docs/runbooks/staging-control-plane.md`.

## M0 State

- Phase 1 V3-001 through V3-007 removed Cloud AI runtime, Agent analyzer/fallback
  RCA, RCA-backed execution, analyze/approve contracts and user surfaces,
  Nginx-specific incident mitigation, fake ingress config, and tracked runtime
  credential/config artifacts.
- Active incident behavior is factual list/get/resolve with authorization,
  deterministic bounded sanitized context, MTTR, and resolve audit.
- Historical `rca_result` and `mitigation_actions_json` columns are storage-only;
  active runtime does not read, expose, or execute them.
- Cloud has no AI provider/runtime. Agent has no LLM/provider/prompt path.
- `IncidentEvidence v1` and Safe ActionPlane v1 are implemented with fake
  runtime durability/race evidence. `opsi mcp serve` is not implemented.
- Opsi renders its owned Deployment, ClusterIP Service, and Traefik Ingress.
  Caller-supplied manifests are not executable input. DNS and certificate
  provisioning remain outside the implemented boundary.
- The control-plane staging package terminates origin TLS at Caddy. This is
  deployment infrastructure for Cloud and is not an Agent-managed application
  gateway capability.
- Clean VPS/K3s automation checks incident list/get/resolve and resolve audit,
  but no committed real-infrastructure pass artifact exists. Production
  readiness remains unproven.

## Implemented Boundaries

- Browser core workflows use the CLI local backend and short local sessions;
  usable PATs remain in OS keychain.
- Cloud owns identity/project/membership, registration, bootstrap/deployment job
  envelopes, OTP, audit/control-plane metadata, and Postgres durability where
  configured. It does not own runtime execution or raw runtime evidence.
- `opsi-cloud admin bootstrap-owner` transactionally creates or reuses the
  normalized first user, organization, canonical project, Owner memberships,
  OAuth identity and/or initial PAT hash in PostgreSQL. A durable singleton
  marker makes exact restart-safe repeats idempotent and conflicting tuples fail
  closed. Raw initial PAT material is written only to an operator-selected
  mode-0600 file and is never printed or audited.
- Opsi has one supported development control-plane deployment path: Docker
  Compose starts PostgreSQL, Opsi Cloud, one Bootstrap Worker, and Caddy. The
  package uses named database and Cloud-data volumes, startup health ordering,
  uniform restart policies, bounded Docker logs, placeholder-only examples,
  and gitignored runtime configuration and initial PAT files.
- A separate `deploy/staging-control-plane` package is code/config validated. It
  uses production Cloud/Worker flags, HTTPS public identity, PostgreSQL,
  authenticated worker calls, non-root Cloud/Worker/proxy containers, read-only
  filesystems, bounded logs, named volumes, an internal backend network, and
  file-backed runtime secrets. PostgreSQL, Worker, and Cloud publish no host
  ports; Caddy alone publishes 80/443 and denies internal/metrics paths.
- The fixed Caddy configuration passed isolated Origin CA validation and the
  public staging origin passed Cloudflare Full (strict), TLS, route, restart,
  and persistence evidence in R5-003. Direct-origin firewall restriction and
  certificate rotation remain `OPERATOR_REQUIRED`.
- Agent owns deployment, service runtime, secrets, telemetry, factual incidents,
  local audit, and K3s/containerd execution.
- Bootstrap Worker is a long-running, single-concurrency daemon. It polls Cloud,
  atomically leases the oldest eligible bootstrap session, increments a bounded
  attempt count, renews the lease with authenticated heartbeats, and binds
  progress and finish calls to the worker identity and one-time lease token.
- Worker configuration no longer accepts a fixed `session_id`. Durable lease
  recovery persists retry backoff and moves exhausted or permanent failures to
  `dead_letter`. Credential handoff is non-destructive across retry attempts;
  registration tokens rotate per attempt. Owner/Admin manual retry is
  idempotent and requires an available credential.

## Next Ordered Work

R5-004 is `PARTIAL / FUNCTIONAL_ACCEPTANCE_PASS / RESILIENCE_EVIDENCE_DEFERRED`.
Gate B is accepted and recovery node `node-c69fe70180d359d7` remains the current
Agent VPS; do not reset or rebuild it again. The live Worker restart during
`install_k3s` is a mandatory R5-017 gate on a disposable VPS or fresh reset
with a deterministic staging-only E2E barrier or fault mechanism, never a
production fault hook. R5-018/MCP remains blocked unless that deferred gate
passes. R5-005 is `OPERATOR_REQUIRED`, not `DONE`. Projectless browser login and
callback, keychain PAT verification, installation/repository claims, two
service bindings, `opsi init` dry-run/apply/idempotency, and CLI/Local API parity
pass live. Repository inventory exposes durable `available`/`active`/`conflict`
ownership state without leaking another project's ID. Local API GitHub
mutations use the keychain PAT and one-time local session/idempotency headers,
while the browser receives no PAT or OAuth token. Full CLI/Cloud tests and vet,
UI lint/build, and disposable PostgreSQL GitHub inventory/durable-dedupe tests
pass at revision `12df6c9`.

The R5-005 fixture now exists and the operator supplied installation/repository
numeric identity. The App must keep Metadata read-only and manually subscribe
only to `repository`. GitHub sends `installation` and
`installation_repositories` as default lifecycle events for every App; they do
not need to appear in the App API `events` array, and `installation_target` is
not a substitute. The focused sanitized verifier and tests encode this boundary;
live selected-repository remove/add must prove lifecycle delivery. The live
`added` delivery is accepted and durable replay returns `duplicate=true` after
Cloud restart. GitHub's App delivery API still contains no matching `removed`
delivery and no `repository` delivery, despite the reported remove/save/add/save
operation. Those two sanitized deliveries, plus a live wrong-user check using a
second GitHub account, remain the acceptance blockers; evidence must not be
fabricated from mocks.

The R5-005 live browser checkpoint exposed stale-keychain and project-first
login UX defects. Browser login now starts from GitHub identity without asking
for a project ID. Cloud resolves the only active project membership and rejects
ambiguous multi-project identity explicitly. Local session startup verifies a
keychain PAT through Cloud, returns only safe org/project identity metadata, and
never stores browser auth state. Failed GitHub callbacks return one-time typed
errors to the Local UI instead of leaving the operator on a public JSON error
page. Focused Cloud/CLI tests and UI lint/build cover the recovery path.

The first live projectless login then proved the GitHub account itself was not
prelinked. The canonical `bootstrap-owner` command now has an explicit
`--link-existing-owner` recovery mode that reads the durable bootstrap marker,
restores its Owner memberships if required, conflict-checks the numeric OAuth
identity, and links it transactionally without requiring the original
email/org/project tuple or issuing a PAT. It remains a local admin operation;
there is no browser, public API, or parallel deployment path.

The next live callback successfully redeemed and returned `auth=ok`, but Local
session verification still reported the newly stored PAT as invalid. The cause
was a transport contract mismatch: CLI correctly sent the credential in the
Bearer header while Cloud `/v1/auth/pat/verify` read only a JSON-body token.
Cloud now uses the same fail-closed Bearer parser as rotate/revoke, the control
plane E2E verifier no longer sends PAT material in a JSON body or process
argument, and a focused endpoint test rejects body-only tokens. Signed-out UI
is now one centered auth gate; the duplicate topbar login and ineffective retry
paths are removed.

## Verification

R5-002 regression checks are focused Cloud tests,
`make dev-control-plane-validate-source`,
`make staging-control-plane-validate-source`, `make source-hygiene`, and
`git diff --check`. R5-003 additionally passed operator-run live TLS,
Cloudflare, restart, persistence, route, and redacted evidence checks. R5-004
additionally passed protected-input tests, Bootstrap Worker/Cloud race tests,
full Agent tests, UI build/lint, live bootstrap, Local API/UI parity, Worker
restart after completion, and target reboot recovery. Direct-origin firewall
restriction and live mid-step Worker resume remain separate unresolved gates.

## FE-04 continuation

FE-01 through FE-04 frontend source redesign and mock Local API browser
acceptance pass. Security and Settings now use their canonical implementations;
the old Secrets and Audit views are deleted. Delivery loading and observability
factuality regressions are fixed, protected values are bounded to the explicit
dialog lifecycle, and ActionPlane approval/execution remains CLI-only.

Dependency status is `OPEN / UPSTREAM_BLOCKED /
NOT_SHIPPED_TO_BROWSER_RUNTIME / BUILD_TIME_RISK_REMAINS`. No live Agent/VPS
acceptance occurred, R5-017 remains pending, and no release or production
readiness claim is made.

## 2026-07-31 UI corrective pass

The canonical frontend now serializes project-switch mutations and rejects
obsolete load/error/refresh results. Workspace project summaries use one
30-second TTL cache entry per project with factual value plus `fetchedAt`;
header refresh force-revalidates visible projects, expired/stale entries
revalidate on workspace navigation, and failed revalidation keeps the last
factual value with readable stale/retry state. Removed projects and signed-out
sessions clear their cached rows. Bootstrap credentials are
requested only at final confirmation, cleared from DOM/state before the request
waits, and required again after failure. Native modal/drawer behavior, APG tabs,
40px targets, complete activity outcomes, and URL-restorable service detail are
covered by browser regressions. Audit time filtering and paused-by-default
periodic Logs refresh close the remaining Prompt 01 acceptance gaps without a
new API or parallel UI path.

One shared six-request limiter now covers every Local API call used to build
workspace summaries, including per-service telemetry. The stress fixture uses
three projects with 24 services each, completes all 93 summary requests after
one telemetry 503, measures maximum concurrency six, and makes no project-switch
mutation. Obsolete queued work fails before starting and obsolete results cannot
update cache or UI.

The Playwright gate now records unexpected HTTP responses, request failures,
application `console.error`, resource errors, and `pageerror`. Intentional HTTP
or request failures require exact path/query, status, method, and (for browser
failures) error text declarations in the test that creates them; declarations
are page-local and reset per test.

Frontend evidence: 33 unit/source tests pass; lint and build pass; all 28
Playwright Chromium scenarios pass with the exact console/resource gate;
`make ui-test`, `make ui-lint`, and
`make ui-build` pass. `make verify` stops before repository verification because
the environment reports `go1.26.5-X:nodwarf5` and the Makefile requires
`go1.26.4`. Live Agent/VPS and screen-reader acceptance remain unproven,
R5-017 remains pending, and organization listing, members/RBAC, and secret
metadata/listing remain the three backend gaps.
