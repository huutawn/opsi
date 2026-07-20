# Opsi Current State

| Metadata | Value |
|---|---|
| Status | Implemented-state snapshot; not a production-readiness claim |
| Last updated | 2026-07-20 |
| Requirements | `docs/opsi_srs.md` |
| Evidence matrix | `docs/status_matrix.md` |
| Canonical roadmap | `docs/opsi_roadmap_v5_production.md` |
| Trusted artifact target | `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md` |

## M0 boundary state

Earlier baseline work removed the old AI/RCA execution boundary and source
artifacts. At this snapshot:

- Cloud has no AI runtime, provider configuration, Gemini integration, fixture
  RCA, prompt path, or `/v1/ai/*` route.
- Agent has no HTTP analyzer, fallback RCA, provider/model metadata, AI network
  call, RCA-backed approval/execution, analyze/approve RPC, or incident-owned
  Kubernetes mutation executor.
- CLI, local API, contracts, and UI expose factual incident list/get/resolve only.
- Historical `rca_result` and `mitigation_actions_json` columns remain for
  storage compatibility but are not read, exposed, or executed.
- `IncidentEvidence v1`, Safe ActionPlane, and `opsi mcp serve` are not
  implemented.
- GitHub App user authorization, installation authentication/webhook intake,
  durable installation/repository inventory, secure installation claim, and
  project/service mapping are implemented. `opsi init` now performs safe local
  GitHub origin matching, numeric repository claim, service binding, and atomic
  repository bootstrap file generation. GitHub Actions OIDC, `BuildRecord`,
  manual `TopologyPlan`, exact-match `DeploymentPolicy`, and deterministic
  routing preflight are implemented. R5-010 adds the immutable-digest
  `DeploymentJob` path and deploys its Cloud revision on staging; real Agent
  workload acceptance is still blocked. Pull request
  preview environments are not implemented.
- Opsi does not render or manage Ingress, Gateway API resources, domains, or TLS.
- Source packaging rejects local config, credentials, private keys, runtime
  certificate directories, databases, logs, and generated output.

## R5-001 credential incident and source-package hygiene

The historical root cause was the former canonical `package-source` recipe,
which ran `tar` over the working-directory `.` with an incomplete exclusion
list. Git-ignored runtime environment and secret files could therefore enter an
archive. The Git-aware `scripts/source-package.sh` path was introduced after
that exposure and is now the only source-package implementation.

The current policy rejects runtime environment/config paths, runtime secret and
certificate directories, private-key markers, key stores, databases, logs,
generated output, nested archives, unsafe archive paths, and escaping symlinks.
It builds from Git tracked plus untracked/non-ignored candidates, validates the
temporary artifact before publication, and validates the final artifact again.
Release validation shares the same path/content policy, while `make release`
recreates `release/` before copying its allowlisted artifacts.

Containment code and local tests do not rotate disclosed credentials. R5-001
incident status is `OPERATOR_REQUIRED` pending rotation or revocation and
post-rotation verification for every applicable credential class, plus a
repository-owner Git history decision. History rewriting is not part of this
task. See `docs/runbooks/credential-incident.md`.

## Repository shape

The workspace contains independent Go modules under `agent/`, `cli/`, `cloud/`,
and `contracts/go/`, plus the CLI web application. Public shared schemas and
bindings live under `contracts/`; business logic remains in the owning domain.

## Implemented Agent slice

- The `opsi-agent` executable is restored as the single entrypoint over the
  existing `config.Load` and `server.Run` composition. It requires `--config`
  for runtime, supports config-only validation, and reports injected version
  plus full commit metadata without requiring configuration.
- A deterministic Linux amd64 release builder produces the direct executable,
  `checksums.txt`, and stable `release.json` SHA-256 metadata. A local verifier
  rebuilds twice with separate Go caches and compares the binary and metadata
  byte-for-byte within the same source tree and Go toolchain.
- Status, deployment, service management, telemetry, secret, and incident gRPC
  services; HTTP health; TLS 1.3 configuration with optional client certificate
  verification.
- SQLite WAL stores for deployment, services, managed services/bindings,
  telemetry, incidents, and runtime audit.
- Git-source deployment with safe relative-path validation, containerd-first
  build, Docker fallback, dry-run adapters, rollout watch, deploy-time rollback,
  rollback verification, redacted errors, and service binding injection.
- Managed PostgreSQL/Redis renderers and external service registration with
  project-scoped storage and deletion/purge distinction.
- Kubernetes/cAdvisor/runtime telemetry collection, bounded logs, retention,
  compressed sync chunks, redacted summaries, and service health queries.
- Kubernetes Secret application through stdin, Cloud PAT verification cache,
  Owner plus OTP/TOTP reveal gate, rotation/restart, and redacted audit.
- Deterministic bounded incident context from metric windows and log
  fingerprints, plus list/get/resolve authorization, MTTR, and resolve audit.
- Cloud relay client and runner for signed heartbeat, deployment lease/result,
  legacy `DeploymentIntent`-scoped Git execution, and the R5-010 immutable-image
  command/result contract. Production image jobs never enter the Git clone or
  Dockerfile build branch.

Agent does not currently provide public incident evidence or a unified action
policy/approval/executor contract.

The pinned Agent artifact is hosted over HTTPS and R5-004 exercised it through
Bootstrap Worker on a clean Ubuntu 24.04 VPS. Its checksum-addressed release
layout, atomic `current` symlink, systemd unit, registration, heartbeat, and
post-reboot recovery passed. A safe live mid-step Worker fault/resume proof is
still unproven because no production fault-injection hook exists.

## Implemented CLI/local backend slice

- Cobra commands for login, init, start, status, deploy, sync, service, secret, and
  incident list/get/resolve.
- `opsi server bootstrap` (also available as `opsi node bootstrap`) creates a
  Cloud bootstrap session from a protected credential file; `server status` and
  `server events` read the same durable session/checkpoint/event flow used by
  the Local UI. Secret values are never accepted as command-line arguments.
- CLI PAT, OTP, and TOTP inputs use protected files or `/dev/stdin`; the old
  secret-valued argv flags are removed.
- `opsi init` uses a bounded no-redirect Cloud HTTP client and OS-keychain PAT,
  detects only supported GitHub.com origins without reading credential helpers,
  and treats local `owner/repo` only as metadata for selecting a numeric Cloud
  repository ID. A missing repository can use the P09 OAuth installation-claim
  flow through a one-time loopback callback.
- Repository bootstrap validates service/binding conflicts and both output
  files before repository-claim or binding mutation. It supports idempotent
  reruns, secret-free JSON dry-run, explicit `--force --yes`, atomic writes, and
  two-file rollback. The generated CD config contains build/deployment intent
  only; the generated workflow is manual bootstrap status and does not request
  OIDC, build, push, call Cloud, or deploy.
- OS-keychain PAT storage and Agent gRPC TLS/client-certificate/certificate-pin
  support.
- `opsi start` localhost server with short local sessions, `/api/local/...`
  Browser mediation, static UI serving, and honest 503 behavior when assets are
  unavailable.
- Local facades for Cloud project/registry metadata and Agent status,
  telemetry/log summaries, secret create/reveal/rotate, and incident
  list/get/resolve. Mutations require local session and idempotency headers.
- Removed incident analyze/approve commands and routes return no active surface;
  removed paths are not compatibility aliases.

The CLI has no MCP server, AI provider integration, approval signer flow, or
Safe ActionPlane client.

## Implemented Cloud slice

- Organization/project/membership metadata, RBAC, PAT verification and GitHub
  App user authorization grant mediation, OTP, Agent/node registration, bootstrap sessions,
  deployment job envelopes, webhook relay, audit, and support metadata.
- GitHub webhook routes require a per-route secret and Cloud verifies the
  SHA-256 HMAC before accepting and sanitizing the payload.
- The browser login path uses fixed GitHub authorization, token, and `/user`
  endpoints with PKCE S256 and five-minute one-time in-memory state. Provider is
  fixed to `github`; subject is the canonical decimal positive numeric GitHub
  user ID. The identity must be prelinked, and Cloud does not trust login/email
  or create an Opsi user or membership during login.
- GitHub user access tokens exist only during login or installation-claim
  callback requests and are not persisted, audited, or returned to the CLI.
  Installation claim state binds purpose, actor, project, numeric installation,
  local callback/state, PKCE verifier, and expiry. Callback verification
  compares the numeric `/user` ID with the prelinked Opsi identity, requires the
  installation in `/user/installations`, and syncs only repositories visible to
  that token. Missing repositories are not marked removed. Organization
  visibility proves installation access for this MVP, not organization-owner
  status. Pending state and local grants remain in memory and are lost on Cloud
  restart. The flow has focused test coverage but no live GitHub App verification.
- GitHub App installation authentication loads an RSA PKCS#1 or RSA-in-PKCS#8
  private key once from a protected read-only file, creates RS256 App JWTs with
  a one-minute `iat` backdate and nine-minute expiry, and requests installation
  access tokens from the fixed GitHub endpoint. Tokens are cached only in
  memory per installation and refreshed with a two-minute safety window.
- `/v1/webhooks/github-app` verifies the App-wide SHA-256 webhook secret before
  decoding and emits typed installation/repository events using numeric IDs as
  identity. P09 wires the registry sink when installation integration is
  enabled. PostgreSQL atomically inserts each delivery ID, applies the mutation,
  and records non-sensitive audit metadata; duplicate delivery IDs remain
  idempotent after Cloud restart. The 24-hour, 10,000-entry P08 replay store is
  retained as the fast in-memory layer. The legacy `/v1/webhooks/github` route
  and `routes[].webhook_secret` behavior remain unchanged.
- PostgreSQL and in-memory registries store installation/repository lifecycle
  status without physical deletion, link a verified installation to a project,
  enforce one active project claim per repository, and bind monorepo service
  keys only to services in that project. Each service has at most one active
  GitHub binding; repository ownership never binds directly to Agent, Node,
  runtime, or VPS identity. Project-scoped repository inventory also reports
  `available`, same-project `active`, or cross-project `conflict` claim state;
  conflict responses do not reveal the other project ID.
- Manual GitHub control-plane parity now uses the existing `/v1` endpoints.
  CLI commands list installations/repositories/bindings, claim/release
  repositories, create/remove bindings, and run a browser-backed installation
  claim with a bounded loopback callback. The Local API maps the same inventory
  and mutation routes, redeems installation grants server-side with the PAT
  from OS keychain, and exposes a Local UI for installation connection,
  repository claim/release, multi-service bindings, conflict/retry states, and
  explicit release/remove confirmation. Browser code does not call Cloud or
  GitHub directly and does not use browser credential storage.
- Postgres-backed registry/relay/audit/idempotency/bootstrap/PAT/OTP state when
  configured; development/test modes may use in-memory implementations where
  production validation permits.
- Agent registration/rotation/revocation, HMAC request validation, bootstrap and
  deployment rate limits, durable deployment leases/retry/dead-letter state,
  and append-only Cloud audit protections for the Postgres path.
- Bootstrap Worker is now a long-running, single-concurrency daemon. It polls
  Cloud and atomically leases the oldest eligible pending or due retry session
  without an operator-provided session ID. Each lease increments a server-owned
  bounded attempt count. Worker progress, heartbeat, and finish requests require
  the lease owner and raw lease token; storage retains only its hash.
- Bootstrap sessions now carry a durable checkpoint independent of status,
  progress events, lease owner, and attempt count. The authoritative
  `first-server-v2` plan uses stable step IDs `preflight`, `install_k3s`,
  `install_agent`, and `register_agent`, plus a deterministic SHA-256
  fingerprint over the version, ordered step command hashes, K3s pin/installer,
  Agent artifact URL/checksum, Agent Cloud URL, and canonical systemd unit.
- Fresh workers initialize checkpoint index zero. After each successful remote
  step, Cloud persists the next index under the active lease before another
  step may run. Retry, manual retry, lease recovery, and a new worker lease keep
  the checkpoint and resume from the next unacknowledged step. New sessions use
  `first-server-v2`; unfinished `first-server-v1` checkpoints fail closed and
  require a new session. A session with
  all four steps checkpointed skips SSH and waits for Agent heartbeat directly.
- Bootstrap resume semantics are at-least-once. If a remote step succeeds but
  checkpoint acknowledgement fails, no later step runs and the successful step
  may execute again on retry. P05 adds idempotent K3s version detection and
  verified upgrade, checksum-addressed Agent staging, atomic activation,
  registration-marker replay, and rollback contracts. R5-004 proved the normal
  live checkpoint sequence and retry-before-step-zero path; a live restart
  between destructive steps remains unproven.
- Worker configuration no longer accepts fixed `session_id`. It requires an
  operator-pinned K3s version, installer URL/SHA-256, Agent artifact URL/SHA-256,
  and an Agent-reachable Cloud URL. Production requires HTTPS by default and a
  trusted, non-empty, non-writable `known_hosts` file; SSH has no insecure
  fallback. SSH host-key negotiation is restricted to algorithms actually
  pinned for the target host, preventing an unpinned ECDSA key from preempting
  an operator-confirmed ED25519 key. The staging-only `http://cloud:9800`
  control URL requires an explicit opt-in that is rejected for every other
  endpoint or non-production configuration; `agent_cloud_url` remains HTTPS.
- Bootstrap accepts password or unencrypted SSH private-key credentials. Worker
  control traffic uses `cloud_url`, while the installed Agent receives the
  separately configured, target-reachable `agent_cloud_url`.
- Bootstrap-generated Agent configuration enables Cloud PAT verification even
  though the Agent gRPC listener remains loopback-only. Cloud OTP request and
  verification endpoints require the same project PAT and derive the delivery
  email from the verified identity rather than trusting request input.
- Active leases are renewed with authenticated heartbeats. Expired leases are
  recovered before polling, retryable failures use persisted exponential
  backoff, and exhausted or permanent failures enter `dead_letter`. Owner/Admin
  may request an idempotent manual retry when a valid bootstrap credential is
  available.
- Credential retrieval is non-destructive across attempts. Agent registration
  tokens rotate per attempt for the same session and node, and terminal session
  handling deletes credential and unused registration material.
- In-memory and PostgreSQL repositories share lease, heartbeat, recovery,
  retry, dead-letter, and manual retry semantics. The PostgreSQL restart test is
  mandatory in `make verify-postgres`; execution still requires
  `OPSI_TEST_DATABASE_URL`.
- P04-focused in-memory, worker, HTTP, PostgreSQL, concurrent transition, and
  pre-P04 row migration-upgrade tests pass. OTP/PAT baseline failure fixed;
  full Cloud suite PASS at this commit.
- P05-focused Bootstrap Worker/Registry tests and race tests pass. Full Agent
  tests pass. Development Compose build and isolated four-service health smoke
  pass.
- Registration replay is idempotent after config/marker persistence. A narrow
  crash window remains if Cloud consumes the token before those files are
  installed; a safe isolated fault test is still required around this boundary.
- The existing Cloud binary exposes the local operator command
  `opsi-cloud admin bootstrap-owner`. It requires PostgreSQL and transactionally
  creates or reuses the normalized user, organization, canonical project plus
  default environment/runtime, Owner memberships, OAuth identity and/or initial
  PAT hash, durable `first_owner` state, and a redacted audit event.
- Exact repeats return the same IDs without issuing another PAT. Conflicting
  owner tuples, project owners, or OAuth identities fail closed. Browser OAuth
  login now resolves the prelinked provider subject instead of authorizing by
  callback email alone. Initial PAT plaintext is never printed or logged and is
  finalized only to an explicitly requested non-overwritten mode-0600 file.
- The command is not an HTTP endpoint and never runs during Cloud startup.

Cloud has no AI runtime and does not own Kubernetes execution or raw runtime
evidence.

## Deployment and gateway truth

Opsi has separate development and production-like staging control-plane
profiles. `deploy/dev-control-plane` remains the supported local HTTP
development package. It starts PostgreSQL, Opsi Cloud, one Bootstrap Worker,
and Caddy with independent development Make targets and configuration paths.

`deploy/staging-control-plane` is the R5-002 production-like package. Caddy
terminates origin TLS on an unprivileged container port using an individually
mounted certificate and private key, while host ports 80/443 are published only
by the proxy. PostgreSQL, Cloud, and Worker publish no host ports. The backend
network is internal; Cloud and Worker receive a separate egress network. Cloud,
Worker, proxy, and PostgreSQL use read-only filesystems where applicable,
temporary tmpfs mounts, capability drops, no-new-privileges, health checks,
explicit restart policy, bounded logs, and named persistent volumes. Cloud,
Worker, and proxy run non-root; the official PostgreSQL entrypoint drops to its
image user after initialization.

Staging Cloud configuration requires production mode, Agent request signatures,
PostgreSQL durability, HTTPS public/callback identity, matching callback origin,
SMTP, disabled OTP development echo/outbox, disabled debug UI, authenticated
Bootstrap Worker calls, GitHub App installation authentication, and
non-placeholder secrets. Secret values are file-backed and mounted per service;
the GitHub App key and origin key are never placed in environment values or
command arguments. Runtime validation URL-decodes and cross-checks the
PostgreSQL DSN username, password, and database against `POSTGRES_USER`, the
`postgres-password` secret, and `POSTGRES_DB`. Caddy rejects `/internal`,
`/api/internal`, `/metrics`, their subpaths/trailing slashes, and encoded paths
before proxying.

The committed configuration examples contain placeholders only. Runtime
environment, Cloud/Worker configuration, secret directory, certificate/key
files, and initial PAT files are gitignored. Both profiles have source-only
Compose parse targets. Staging also has focused negative validation for
insecure flags, HTTP identity, callback mismatch, missing/writable TLS mounts,
placeholder secrets, public backend ports, internal route exposure, and mutable
`latest` images.

R5-003 reproduced a live staging proxy health failure before Cloudflare
cutover. Caddy was running with listeners on 8080/8443, but its container-local
`/health` request initially returned 308 to `https://127.0.0.1/health`; BusyBox
`wget` followed to container port 443 and then reported connection refused. The
root cause was Caddy's normal directive sorting placing `redir` before
`respond`, despite their textual order. The HTTP listener now uses one `route`
block to preserve the loopback health response before the general redirect.
The source validator rejects the former unordered form, and the focused
loopback smoke verifies container health, HTTP behavior, Origin CA TLS,
protected routes, hardening, and log markers while keeping the development
profile running.

The development package remains development-only. P01 code is complete, but
clean control-plane VPS checkpoint `CP-VPS-1` was not run because no clean
Ubuntu VPS was available. Its status is `DEFERRED / UNPROVEN`; no VPS evidence
exists, and the checkpoint remains a blocker before production acceptance.

R5-003 live evidence was executed on the Cloud VPS at revision
`d5b2e81c433d287369ad63e99fc4331db68bc420` on 2026-07-17. The fixed staging
profile passed runtime validation, Origin CA chain and certificate/key checks,
public 80/443 cutover, Cloudflare Full (strict) public health, TLS hostname
validation, protected-route denial, dynamic error caching, and redacted log
marker scans. Reverse proxy, Bootstrap Worker, Cloud, and PostgreSQL were
restarted independently; Compose down/up without `-v` preserved both named
volumes and the safe schema/count/hash metadata. The initial live database had
no business rows, so this evidence proves schema and volume persistence but
does not claim business-identifier recovery. Direct-origin firewall restriction
and certificate rotation remain operator work. The procedure is
`docs/runbooks/staging-control-plane.md`.

R5-004 live evidence ran on a separate clean Ubuntu 24.04 amd64 Agent VPS at
revision `d3df6b8d2b3a029ea3f589dfb840ff296e7bdbd5`. Final-revision CLI session
`boot-97705a044b859f66` kept one node identity through a fail-closed initial SSH
attempt, supported manual retry, successful K3s/Agent installation, Worker
verification, Worker restart after completion, and controlled target reboot.
K3s `v1.36.2+k3s1`, containerd `2.3.2-k3s2`, and Agent
`0.0.0-staging.a0d5315` were factual and healthy after reboot. CLI status,
Local API bootstrap/session/events, UI-backed node state, and Cloud registry
agreed on the completed session, checkpoint index four, node, Agent identity,
and advancing heartbeat. The live mid-step Worker restart/resume scenario was
not run because the completed healthy node has no safe production fault hook.

R5-004D kept the canonical `{"nodes":[...]}` response and resolved the existing
`R5-004` project through an idempotent staging `bootstrap-owner` repeat
(`reused: true`, no PAT issued). Fedora Secret Service canary and the bounded
final CLI keychain path passed; atomic TLS resolution, direct TLS-pinned
PAT-authenticated status, real pin/name/auth negatives, and Local UI shared
state all passed. The old Agent node was decommissioned, only Opsi/K3s-managed
paths were reset under the trusted ED25519 SSH key, and recovery session
`boot-7b843526dff6842b` completed as `node-c69fe70180d359d7`. The one permitted
poller started too late: its first observation was checkpoint `4/register_agent`,
so it did not restart the Worker during `install_k3s`. R5-004 stays `PARTIAL`;
no second reset/rebuild, production fault hook, or target reboot was attempted.
This is accepted as `FUNCTIONAL_ACCEPTANCE_PASS / RESILIENCE_EVIDENCE_DEFERRED`:
Gate B is accepted, `node-c69fe70180d359d7` remains the current Agent VPS, and
the destructive-step Worker restart moves to mandatory R5-017 evidence on a
disposable VPS or fresh reset with a deterministic staging-only barrier or fault
mechanism. R5-018/MCP is blocked until that deferred R5-017 gate passes.

Git-based deployment exists and can apply user-provided manifests. Such a
manifest may contain its own Service, Ingress, Gateway, TLS, lifecycle, or
shutdown configuration; those resources are user-owned input, not an
Opsi-managed gateway. `IngressEnabled` was removed from active contracts/config,
with a fail-fast error retained for old configuration.

R5-010 now implements the local migration path as:

```text
legacy/manual Git build (development compatibility only)
-> accepted BuildRecord + exact R5-009 route
-> durable immutable-image DeploymentJob
-> Agent digest pull + Opsi Deployment/ClusterIP Service
```

The production flow uses GitHub Actions build/test, an OCI registry, an OIDC-bound
`BuildRecord`, `DeploymentPolicy`, a durable `DeploymentJob`, and Agent
deployment of `registry/repository@sha256:<digest>`. Git commit SHA remains
source identity and provenance, not the runtime artifact. The local
implementation extends the existing job/engine rather than adding a parallel
deployment engine. It snapshots BuildRecord/topology/policy/routing/workload
authority, uses lease-bound monotonic progress, renders owned Deployment and
ClusterIP Service resources, rejects foreign collisions, and verifies the
named application container imageID. The Git clone/build path remains only for
legacy/manual development use.

Local implementation does not establish live Agent acceptance. Full Go test/vet,
focused race, disposable PostgreSQL migration/restart/concurrency, UI
lint/build/source-state, deterministic Agent release, source hygiene, and diff
checks pass at final code-bearing revision
`4b7fe549f02fd47a07c0196e264971c31488850d`. Immutable Cloud image
`ghcr.io/huutawn/opsi-cloud@sha256:d3bacfc86d879a802a8912d7c11490a9f0f4468c83092d4863883acdad7ce704`
is published and runs on staging with PostgreSQL, Bootstrap Worker, proxy, and
volumes retained; all four services are healthy with zero restarts. Agent
release `0.0.0-r5.010.4b7fe54` is reproducibly built with binary SHA-256
`f25d00735dc7a92611b15986eea03fa050cb8893ee27a2e9485d9890503a6799`,
and exact code tag `r5-010-4b7fe54` is pushed,
but GitHub prerelease publication is blocked by GitHub CLI `401`. Product login
and canonical live BuildRecord lookup remain blocked by `AUTH_REQUIRED`;
the still-installed R5-004 Agent reports its historical hard-coded
`cloud_connected=false`; the final R5-010 binary reports factual Cloud
connectivity after upgrade. Headless live
UI parity, published Agent artifact, supported live Agent upgrade, real K3s
workload proof, and restart recovery remain unproven. R5-010 creates no
Ingress/Gateway/DNS/TLS resource and implements no automatic rollback; those
remain R5-011 scope.

## E2E and production evidence

`scripts/e2e/verify-k3s.sh`, `make verify-e2e-k3s-preflight`, and
`make verify-e2e-k3s` define the protected clean VPS/K3s command path. The
incident segment checks factual incident list, detail, resolve, and resolve
audit. The command path exists, but no committed real-infrastructure pass
artifact currently proves the complete scenario. R5-004 proves the bootstrap,
registration, shared CLI/UI state, and reboot subset; the wider E2E scenario
remains `MANUAL_GATED`.

Production readiness remains unproven. Current gaps include direct-origin
restriction and certificate rotation, clean control-plane VM proof, live
mid-step bootstrap resume, live GitHub App installation and
user-auth/repository-bootstrap verification, Actions OIDC, trusted
OCI artifact delivery, managed
gateway, public incident evidence, Safe ActionPlane, CLI MCP, complete Dev VPS
E2E, release hardening, supply-chain evidence, and measured disaster recovery.

The R5-005 live checkpoint is `OPERATOR_REQUIRED`, not `DONE`. The fixture,
installation, and numeric identities exist. Sanitized App preflight confirms
manual events `repository`, default lifecycle events
`installation`/`installation_repositories`, Metadata read-only, the canonical
HTTPS callback/webhook, installation-token creation, and the expected selected
repository. Projectless browser login/callback, keychain Bearer PAT verification,
installation/repository idempotent claims, two active service bindings,
`opsi init` dry-run/apply/second-apply idempotency, and CLI/Local API parity pass
live. Cloud runs immutable digest
`sha256:b37677c0a3aed9e031a2460118bd761267bf4c30908a9b8e11980987ce7907fb`;
4/4 staging services and public health pass with unchanged named volumes.

The signed live `installation_repositories: added` delivery is processed, and
redelivery after Cloud recreation returns `duplicate=true`, proving PostgreSQL
durable dedupe without a second business mutation. GitHub's App delivery API
still exposes no `installation_repositories: removed` delivery and no
`repository` delivery for the fixture. A second GitHub account is also not
available for the canonical live wrong-user negative. These missing live
artifacts block R5-005 `DONE`; unit/mock evidence does not replace them.

## Ordered next work

P03 Agent executable and deterministic local release artifact code is complete.
P04 durable checkpoint/resume behavior is implemented and its Cloud closure
gate is green: OTP/PAT baseline failure fixed; full Cloud suite PASS at this
commit. P05 supply-chain,
transport, installer, checksum, HTTPS, K3s pinning, and canonical systemd layout
hardening is implemented with focused/race and development smoke evidence. The
P06 normal clean-target bootstrap and reboot path now has live evidence, while
mid-step Worker restart/resume remains unproven. P07 GitHub
App user authorization code and P08 installation authentication/webhooks are
code complete, while real GitHub verification is `UNPROVEN`. P09 durable
inventory, verified installation claim, single-project repository ownership,
and service binding are implemented with local and PostgreSQL tests; P09 live
GitHub verification remains `UNPROVEN`. P10 `opsi init` repository bootstrap is
`CODE COMPLETE` with local tests, while its live GitHub checkpoint remains
`UNPROVEN`. P11 is blocked until that checkpoint is run or explicitly recorded
as deferred;
OIDC-bound trusted artifact delivery, runtime delivery, and the later
evidence/ActionPlane/MCP phases remain ordered future work. The ordered source
of truth is `docs/opsi_roadmap_v5_production.md`.

## R5-006 repository CD checkpoint

R5-006 implements one repository-owned application path for monorepo CD intent.
`.opsi/opsi-cd.yaml` v2 is strict and deterministic: it contains only version,
service keys, build context/Dockerfile/platform, watch/shared paths, dependency
keys, and production/preview intent. v1 `ServiceBuild` files migrate without
dropping a service or adding infrastructure identity; unknown fields, invalid
paths, traversal, escaping symlinks, duplicate keys, missing dependencies, and
dependency cycles fail closed. `opsi init` now has a local repository mode for
create, v1 migration, add, update, dry-run, atomic apply, and idempotent repeat;
the existing GitHub binding path uses the same repository mutation service.

The changed-service resolver runs the fixed argv form
`git -C ROOT diff --name-status -z BASE HEAD`, parses add/modify/delete/type,
copy, and rename source/destination paths, matches path components, expands
shared paths and dependent closure, and emits `opsi.cd.plan/v1` with config and
plan hashes. Missing/untrusted/shallow bases, failed or bounded/ambiguous diffs,
unmatched changed paths, and initial builds select every configured service with
typed reasons; a truly
empty trusted diff is the only empty plan. CLI `opsi cd plan` and Local API
`/api/local/repository/plan/preview` use the same DTO and service.

The generated workflow has read-only contents permission, immutable action and
Opsi planner source revisions, bounded plan/build jobs, deterministic
concurrency and fork-safe behavior;
it performs no OIDC, GHCR push, Cloud call, or deployment. Local UI repository CD
setup displays all services, previews config/migration/workflow changes, applies
with local session/idempotency confirmation, and previews affected services with
the same plan hash as CLI. The R5-007 focused entry review strengthened that
apply boundary: preview returns a hash over the canonical mutation, current and
rendered managed-file hashes, and ordered file actions; apply recomputes it from
the current filesystem, rejects stale previews before write, and uses a bounded
in-memory ledger so exact retries reuse the result while conflicting key reuse
returns a typed conflict. Live GitHub runner execution remains a later R5-008
checkpoint.

Capability matrix (R5-006): config v1/v2 parser-validator-writer and atomic
mutation path: implemented; `opsi init` create/add/update/migrate/dry-run/apply:
implemented; workflow renderer: deterministic secure changed-service matrix;
Git adapter: fixed-argv bounded diff parser; Local API: config/mutation/workflow/
plan preview plus confirmed apply; Local UI: service editor and plan/workflow
preview with loading/error/retry state and stable preview-bound apply retries.

## R5-007 trusted BuildRecord checkpoint

R5-007 adds a dedicated Cloud GitHub Actions OIDC verifier and a versioned
`opsi.build_record/v1` application path. Production configuration pins the
issuer to `https://token.actions.githubusercontent.com`, pins the official
JWKS endpoint, requires an exact audience and non-empty workload allowlist, and
fails closed on token/JWKS size, timeout, cache, algorithm, signature, issuer,
audience, time, type, numeric identity, ref, workflow, SHA, and redirect checks.
The verifier uses a bounded coalesced JWKS cache and never persists raw JWTs or
JWKS responses.

The official GitHub OIDC reference was checked on 2026-07-19. It lists
`job_workflow_ref` only for jobs using a reusable workflow; the generated R5-006
workflow is a direct workflow, so its trusted contract uses exact `workflow_ref`
and does not invent or silently require `job_workflow_ref`. A reusable workflow
is accepted only when an explicit workload policy allowlists its exact claim.

BuildRecord submission accepts only `Authorization: Bearer <OIDC JWT>` at
`POST /v1/build-records`; PATs, query/cookie tokens, unknown JSON fields, project
authority in the body, mutable OCI tags, and cross-binding identities are
rejected. Repository/owner/run/attempt/service identity is derived from the
verified token and active GitHub registry binding. PostgreSQL stores one
append-only row per `(repository_id, run_id, run_attempt, service_key)` with a
payload hash, immutable digest, hashes, workflow identity, and safe workload
metadata only. Exact retries reuse the row; conflicting retries return typed
409. Project-scoped PAT reads are available through the CLI and Local API/UI;
the browser has no Cloud/GitHub credential path and no deploy action.

R5-008 live acceptance passed on 2026-07-19. Baseline run `29676422752` attempt
2 selected `api` and `worker` and created BuildRecords
`br-21170479e7f2bda0a9b2ef89ef821b47` and
`br-9e549bbbaecdd41f4264d03c2c573a30`. The API-only run `29676722594` at
SHA `06a617d8d323c06502f37c1f874e871c7845429b` created
`br-c3d7654507dae1383b0e52eebe67eebf`; no worker record was created. In each
case the verified repository/owner/ref/SHA/workflow/run identity, config hash,
plan hash, platform, OCI repository, and registry digest matched. Anonymous
GHCR manifest-by-digest returned HTTP 200 with the same digest for both fixture
services and for the final Cloud image.

The controlled negative suite returned typed wrong-audience 401,
unallowlisted-workflow/wrong-ref/wrong-OCI 403, claim/body SHA mismatch 403,
unbound service 403, tag-only digest 400, exact replay 200 with the same ID and
`reused=true`, changed-payload conflict 409, failed-image-build with no
BuildRecord, and rate-limit 429 with `Retry-After: 60`. A pull request run had
`plan` and untrusted build jobs only; `publish-and-record` was skipped. Temporary
negative workflow/policy entries were removed afterward. No Agent deployment
path was added during R5-008.

The final staging Cloud image is
`ghcr.io/huutawn/opsi-cloud@sha256:c3c63a1724a8b17876c200251293156773b172b782257811c8d3d848eac61bf6`,
built from Opsi code-bearing revision
`b1435f0029e0ad65c019ff692bfa80e1f2aa1476`. PostgreSQL/Cloud named volumes,
Bootstrap Worker, and reverse proxy were preserved; staging was 4/4 healthy,
public `/health` returned 200, and the sanitized log-marker count was zero.

## R5-009 manual placement and routing checkpoint

R5-009 adds `opsi.topology_plan/v1` and `opsi.deployment_policy/v1` without
changing the R5-008 OIDC verifier or static workload-admission policy.
`DeploymentPolicy` is evaluated only for an already accepted `BuildRecord` and
cannot override issuer, JWKS, audience, signature, claim/body binding,
repository ownership, or active service binding checks. ADR-005 records this
authority boundary.

Topology and policy state use immutable PostgreSQL revisions with mutable heads,
expected revision/state-hash concurrency, project/operation/key/payload-bound
idempotency, authenticated audit actors, and disabled revisions instead of
physical deletion. Operator capacity is a separate audited
`source=operator_declared` record. Factual capacity comes from the node
heartbeat inventory; unknown capacity fails closed unless the active matching
policy explicitly grants a service/environment/runtime-scoped override.
Heartbeat freshness is computed from server time with a bounded server-side
TTL. Single-node runtimes with no deploy Agent, multiple deploy Agents, stale
heartbeats, unknown capacity without override, oversubscription, or foreign
runtime identity fail closed.

CLI supports topology plan/validate/diff/apply/get/facts, audited operator
capacity, policy create/diff/apply/disable/list/get, and deterministic routing
preflight. The loopback Local API exposes the same contracts. The Local UI
manual wizard selects repository, service, accepted BuildRecord, environment,
runtime, integer resources, and exposure intent; retains unrelated service
assignments; previews validation/diffs/hashes; confirms apply; and renders
revision/audit results. Browser storage and direct Browser-to-Cloud/Agent calls
remain absent, and there is no deploy button.

Disposable PostgreSQL plus real loopback Cloud, CLI, Local API, built Local UI,
and headless Chrome acceptance passed for `api` and `worker`. CLI and Local API/UI
returned identical topology/policy hashes; browser preview and apply produced
audited revisions; exact replay returned `reused=true`; conflicting replay
returned typed 409; concurrent topology apply produced one revision and one
state conflict; PostgreSQL restart preserved heads, revisions, and idempotency
rows. Routing selected one factual runtime/Agent and rejected stale, unknown,
oversubscribed, foreign, missing-Agent, ambiguous-Agent, wrong identity, and
disabled-policy cases. No SSH, Agent VPS, K3s, workload, `DeploymentJob`, MCP,
or AI mutation was performed.

## Verification commands

From repository root:

```bash
make test
make build
make agent-release
make verify-agent-release
make release
make smoke-release
make verify-e2e-k3s-selfcheck
make source-hygiene
make package-source
make verify
```

Go module tests run from `agent/`, `cli/`, `cloud/`, and `contracts/go/`, not
from the workspace root.
