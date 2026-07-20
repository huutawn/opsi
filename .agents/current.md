# Opsi Current Snapshot

Detailed state: `docs/current_state.md`. Architecture: `docs/architecture.md`.
Requirements: `docs/opsi_srs.md`. Evidence: `docs/status_matrix.md`.
Canonical roadmap: `docs/opsi_roadmap_v5_production.md`.

## Active Task

### R5-010 — Immutable manual production deployment

- Final code-bearing revision `4b7fe549f02fd47a07c0196e264971c31488850d`
  is committed from starting revision
  `0edaa4330c57377e7545ae5e3a86407b74e19107` on `developer`.
- The existing DeploymentJob and Agent deploy engine are extended with one
  `immutable_image` path; no second production engine is introduced.
- Local contracts, Cloud authority snapshot/state machine, Agent digest pull
  and Opsi renderer, canonical CLI subcommands, and loopback Local UI flow are
  implemented. Full Go test/vet, focused race, disposable PostgreSQL restart /
  concurrency, UI lint/build/source-state, deterministic renderer/release,
  source-hygiene, and diff checks pass locally.
- Product BuildRecord lookup is currently `AUTH_REQUIRED`. Before live work the
  operator must run `opsi login --pat-file <protected-path>`; no PAT is accepted
  in chat, argv, history, logs, or evidence.
- Cloud image
  `ghcr.io/huutawn/opsi-cloud@sha256:d3bacfc86d879a802a8912d7c11490a9f0f4468c83092d4863883acdad7ce704`
  is published and deployed on staging. PostgreSQL, Bootstrap Worker, proxy,
  and their volumes were retained; staging returned four healthy containers,
  zero restarts, Cloud version `4b7fe54`, and public `/health` success.
- Deterministic Agent release `0.0.0-r5.010.4b7fe54` is built locally from the
  committed revision with binary SHA-256
  `f25d00735dc7a92611b15986eea03fa050cb8893ee27a2e9485d9890503a6799`.
  Its exact code tag `r5-010-4b7fe54` is pushed to GitHub.
  GitHub prerelease `https://github.com/huutawn/opsi/releases/tag/r5-010-4b7fe54`
  is published with `opsi-agent-linux-amd64`, `checksums.txt`, and `release.json`;
  an anonymous download matched the same SHA-256.
- Read-only live preflight re-confirmed the trusted Agent ED25519 fingerprint,
  staging Cloud digest and four healthy staging containers, K3s `v1.36.2+k3s1`,
  node `node-c69fe70180d359d7`, and Agent `0.0.0-r5.004.af0ebce`. That old Agent
  local health reports its hard-coded `cloud_connected=false`; revision
  `4b7fe54` replaces it with factual heartbeat/poll connectivity tracking, but
  the supported upgrade cannot run until the release asset and product login
  are available.
- No Agent upgrade, workload apply, Agent restart, K3s reset, VPS reset, or
  Cloudflare/DNS/TLS change has been performed for R5-010.

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
- `IncidentEvidence v1`, Safe ActionPlane, and `opsi mcp serve` are not
  implemented.
- Opsi does not render or manage Ingress, Gateway API, domains, or TLS. User
  manifests may contain their own resources.
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
