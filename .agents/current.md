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
- SSH ignores ambient configuration, uses only the explicit identity and
  known-hosts files, and allowlists only public-key authentication. Batch mode,
  identity-only operation, and a disabled ambient agent accompany explicit
  rejection of keyboard-interactive, challenge-response, host-based, and
  GSSAPI authentication; forwarding, tunnels, multiplexing, proxy commands,
  and proxy jumps are disabled.
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
