# Opsi Architecture

| Metadata | Value |
|---|---|
| Version | 6.0 |
| Status | Active architecture map |
| Last updated | 2026-07-13 |
| Requirements | `docs/opsi_srs.md` |
| Implementation truth | `docs/current_state.md`, `docs/status_matrix.md` |
| Canonical roadmap | `docs/opsi_roadmap_v5_production.md` |
| Trusted artifact decision | `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md` |

This document deliberately separates the current M0 architecture from the
target architecture. Target components must not be inferred to exist unless the
status matrix provides implementation evidence.

## 1. Current architecture at M0

```text
Browser
  -> CLI local backend
       -> Cloud metadata/auth/bootstrap/deployment APIs
       -> Agent gRPC runtime APIs

Cloud
  -> bootstrap/deployment job relay
       -> Agent cloud runner

Git repository
  -> Agent clone/build legacy/manual path

Agent
  -> K3s/containerd
  -> local SQLite/runtime stores
```

No current path contains an AI provider, MCP server, `IncidentEvidence v1`,
`ActionPlan`, approval grant, Safe ActionPlane, GitHub Actions OIDC,
`BuildRecord`, digest-based image deployment, `DeploymentPolicy`, or pull
request preview environment.

### 1.1 Repository domains

| Domain | Current responsibility |
|---|---|
| `cloud/` | Identity, organization/project/membership, PAT/OTP, Agent/node registration, bootstrap and deployment job envelopes, audit/control-plane metadata, Postgres durability where configured |
| `cli/` | Cobra CLI, OS-keychain PAT, localhost session/API facade, Browser mediation, Agent gRPC client, Cloud metadata client |
| `agent/` | Deployment, service runtime, secrets, telemetry, factual incidents, local audit, cloud job runner, K3s/containerd execution |
| `contracts/` | Public schemas and bindings only; no business policy |

The Go modules are separate ownership boundaries. `agent/`, `cli/`, and
`cloud/` must not import each other's internal packages.

### 1.2 Current Cloud boundary

Cloud owns durable control-plane metadata and coordination. It has no AI runtime,
AI provider key, prompt orchestration, RCA generation, raw telemetry store, or
Kubernetes executor. It must not persist raw logs, raw metrics, app secret
values, kubeconfig, source code, or device private keys.

Cloud may relay versioned deployment/bootstrap work to an authenticated Agent.
The work is not complete merely because Cloud metadata changed; Agent must
perform and report the runtime operation.

Cloud currently provides generic browser OAuth mediation and a webhook route
with per-route HMAC verification. Those implemented pieces are not GitHub App
user authorization, App installation-token support, installation/repository
event ownership, or GitHub Actions OIDC verification.

Bootstrap Worker is a long-running, single-concurrency Cloud-side worker. It
polls `POST /internal/bootstrap/sessions/lease`; the registry atomically claims
the oldest eligible pending or due retry session, increments its attempt count,
and stores only a hash of the one-time lease token. The worker renews active
leases through authenticated heartbeat requests. Progress and finish calls also
require worker identity and the raw lease token.

Cloud recovers expired leases before polling. Retryable outcomes receive
persisted bounded backoff; exhausted or permanent outcomes enter
`dead_letter`. Credential retrieval remains durable across attempts and
registration tokens rotate per attempt. Owner/Admin manual retry is
project-scoped and idempotent. The worker still processes at most one session,
and P04 owns the future per-step resumable BootstrapJob state machine.

The worker uses a private control-plane URL for lease traffic and a distinct
Agent-reachable Cloud URL when installing a remote Agent. Bootstrap credential
handoff supports passwords and unencrypted SSH private keys; production requires
known-host verification and encrypted PostgreSQL credential storage.

### 1.3 Current CLI/local backend boundary

The production-oriented Browser boundary is localhost `/api/local/...` served by
`opsi start`. The CLI backend holds the usable PAT in the OS keychain, issues a
short local session to the Browser, and mediates Cloud metadata and Agent runtime
calls. The Browser must not receive a long-lived PAT.

The CLI currently has no `opsi mcp serve`, local AI integration, approval device
signing, or ActionPlane client.

### 1.4 Current Agent boundary

Agent owns runtime facts and execution. Current services include status,
deployment, service management, telemetry, secrets, and incident list/get/resolve.
The incident package builds bounded deterministic sanitized context and performs
authorization and resolve audit. It does not perform AI analysis or execute a
stored RCA/recommended action.

Historical `rca_result` and `mitigation_actions_json` database columns are
storage-only. They are not execution authority.

### 1.5 Current deployment and gateway boundary

Git deployment and user-provided manifest application exist. Those manifests may
contain Service, Ingress, Gateway, TLS, or lifecycle objects owned by the user.
Opsi itself does not currently render or manage a gateway resource, domain, or
TLS certificate. The removed `deployment.ingress_enabled` key fails fast. Agent
currently clones/builds Git source for this path and rejects image-source
deployment before runtime execution.

### 1.6 Current incident path

```text
Agent telemetry/detectors
  -> factual incident store
  -> IncidentService list/get/resolve
  -> CLI local backend
  -> Browser/CLI
```

The clean VPS/K3s command path verifies this factual lifecycle and resolve audit.
There is no analyze, action approval, or mitigation execution step.

## 2. Target architecture - not implemented at the current snapshot

```text
GitHub
|-- App user/install/webhook trust
`-- Actions OIDC
       |
       v
OCI registry <- GitHub Actions build/test and push
       |
       v
Cloud BuildRecord / DeploymentPolicy / repository-service routing
       |
       v
durable DeploymentJob -> eligible healthy Agent
       |
       v
Agent pulls and deploys image@sha256:<digest>
       |
       v
K3s/containerd
```

Git commit SHA is source identity and provenance. The runtime artifact is the
immutable OCI reference `registry/repository@sha256:<digest>`. Mutable tags may
exist for people, but Agent deploys the digest.

### 2.1 GitHub and OCI trust paths

The target has five separate trust paths:

1. GitHub App user authorization uses App Client ID/Secret, state, and PKCE for
   login or identity linkage. GitHub numeric user ID is the subject.
2. GitHub App installation authorization uses App ID/private key to mint a
   short-lived installation access token for repository metadata or configured
   status/check operations.
3. GitHub webhook verification uses the per-App webhook secret plus event and
   delivery identity.
4. GitHub Actions OIDC uses GitHub JWKS and verified claims including `iss`,
   `aud`, `exp`, `nbf`, `repository_id`, `repository_owner_id`, `ref`, `sha`,
   `event_name`, `run_id`, `run_attempt`, `workflow`, and
   `job_workflow_ref`.
5. OCI registry authentication separately grants runner push authority and Agent
   pull authority.

OIDC does not replace a GitHub App installation token or the registry. Registry
credentials are not GitHub OAuth credentials.

### 2.2 BuildRecord, ownership, and routing

Repository-owned configuration defines build context, Dockerfile, platform,
tests, service identifier, and optional deployment metadata. Cloud-owned
configuration defines installation/repository identity, project/service
mapping, allowed workflow/event/refs, environment/runtime, `DeploymentPolicy`,
and OCI repository allowlist.

The workflow submits an OIDC-bound `BuildRecord` with repository ID, commit SHA,
ref, event, run ID/attempt, workflow identity, image repository/digest, and
optional provenance digest. Cloud compares request-body values with verified
claims and fails closed on mismatch.

Routing is:

```text
GitHub installation
-> GitHub repository ID
-> Opsi project
-> Opsi service
-> environment
-> runtime
-> eligible healthy Agent
```

A repository is never owned by or bound directly to an Agent/VPS. Agent is a
replaceable runtime target.

### 2.3 Target delivery and gateway flow

An allowed `BuildRecord` creates or reuses a durable `DeploymentJob`. Agent
validates the allowlisted image repository and full `sha256` digest, pulls the
immutable artifact, and deploys it through an Opsi-rendered Deployment and
ClusterIP Service. Typed `ExposureSpec` produces an Opsi-owned Traefik resource
with hostname/path conflict detection. Readiness, last-known-good digest,
automatic rollback, post-check, and restart reconciliation complete the
deployment transaction.

For image-source jobs Agent must not clone or build source. The current
Git-source clone/build implementation remains the explicitly non-production
legacy/manual path during migration.

### 2.4 Trusted CD policy and pull requests

`DeploymentPolicy` is preconfigured by an authorized user and allowlists the
repository, workflow, event, ref, environment/runtime, and OCI repository. An
allowed branch push may create a deployment without per-run human approval.

Same-repository pull requests may build and may deploy previews only when policy
allows. Fork pull requests fail closed by default, untrusted fork code receives
no write token or production secret, preview environments are isolated with
TTL cleanup, and pull request approval is not production approval.

### 2.5 Target evidence and action flow

```text
Codex / Claude Code / Antigravity / compatible MCP client
  -> opsi mcp serve
  -> CLI local backend
       -> Agent read-only IncidentEvidence APIs
       -> Agent typed ActionPreflight APIs

Human Local UI or trusted interactive CLI
  -> separate approval interaction
  -> signed ApprovalGrant
  -> Agent typed executor
  -> post-check and audit
```

Vendor names are examples of MCP clients. Opsi provides one vendor-neutral local
bridge, not provider-specific runtime architecture. Agent builds
`IncidentEvidence v1` from bounded redacted runtime facts; Cloud does not
receive the evidence body.

Trusted CD is not an AI action. `DeploymentPolicy` does not use an AI
`ActionPlan` or `ApprovalGrant`, AI cannot approve CD, and automatic rollback is
part of the authorized deployment transaction. AI-originated mutations still
require deterministic Agent preflight and separate human approval. MCP may read,
preflight, and request a challenge but exposes no execute or approve tool.

## 3. Trust boundaries

| Boundary | Trusted data | Untrusted data | Secret policy | Mutation policy |
|---|---|---|---|---|
| Browser <-> CLI | Short local session, explicit user input after validation | Browser state, request bodies, rendered runtime text | No long-lived PAT, Agent token, device key, or reusable grant in Browser | Mutations require local session, CSRF/origin controls, project scope, RBAC, and idempotency |
| AI client <-> MCP | Versioned tool schemas and bounded typed responses | AI output, prompts, copied logs, commit/application text | No PAT, Agent token, device private key, secret value, or ApprovalGrant | Read/preflight/challenge request only; no execute or approve tool |
| User <-> GitHub App authorization | Verified callback subject with state/PKCE | Callback parameters and profile fields before validation | App Client Secret remains Cloud-side | Login/link numeric GitHub user identity only |
| Cloud <-> GitHub App installation API | App JWT and short-lived installation token | Installation/repository metadata before mapping | App private key and installation tokens are Cloud-side, rotated, and scoped | Read mapped metadata or create configured status/check; no registry pull authority |
| GitHub webhook <-> Cloud | Valid per-App signature, event and delivery identity | Payload fields until mapped and validated | Webhook secret remains Cloud-side | Installation/repository metadata event only; not build identity |
| GitHub Actions <-> Cloud | Verified OIDC signature/claims and claim-bound `BuildRecord` | JSON body, repository/ref/SHA strings, unverified workflow output | OIDC token is short-lived and replay-protected | Create trusted build metadata only after policy and body binding |
| GitHub Actions <-> OCI registry | Scoped runner push identity | Image labels, tags, build output | Push credential is separate from GitHub OAuth and Agent pull | Push only to allowlisted repository |
| Agent <-> OCI registry | Scoped pull identity and immutable digest | Mutable tags and registry metadata before verification | Pull credential is separate, least privilege, and never a GitHub App token | Pull allowed repository/digest only |
| CLI <-> Cloud | Authenticated metadata and versioned envelopes | Provider callbacks, webhook metadata, user-supplied identifiers | PAT stays in OS keychain; Cloud stores hashes; no runtime secrets | Metadata/control-plane mutations only; Cloud does not execute K3s operations |
| CLI <-> Agent | Versioned gRPC types, authenticated project/actor context | User/AI reason text and evidence references | TLS/mTLS/pinning target; no credential forwarding to AI | Current typed APIs only; future actions require deterministic preflight and valid grant |
| Cloud <-> Agent | Signed registration, heartbeat, lease, digest job, and result envelopes | Remote errors and external webhook-derived metadata | Agent credential/HMAC protected; no raw runtime evidence or registry password plaintext in Cloud | Agent performs runtime work; Cloud records sanitized result metadata |
| Agent <-> Kubernetes/containerd | Agent-generated typed arguments and owned resource identity | Manifest/application output, logs, Kubernetes events | Secrets use stdin/API channels, never command arguments or audit text | Allowlisted, bounded execution; future action policy, locks, post-check, and audit |

## 4. Security invariants

1. External AI clients must not receive Agent credentials or establish a direct
   authenticated control connection to an Agent.
2. The AI request channel is not the human approval channel.
3. ApprovalGrant must not be returned in an MCP response or AI conversation.
4. AI content is advisory data; deterministic Agent policy is authoritative.
5. R4 operations and unsupported data/node/network/security mutations are
   forbidden, not made valid by approval.
6. Legacy RCA storage is never an action authorization source.
7. Cloud remains outside the runtime execution and raw evidence boundary.
8. Production deployment identity is a full OCI `sha256` digest, never a
   mutable tag or Git commit SHA.
9. OIDC claims and `BuildRecord` body fields must match; a body value is never
   trusted by itself.
10. Repository ownership terminates at the Opsi service mapping, not an Agent
    or VPS identity.
11. Trusted `DeploymentPolicy` authorization is separate from AI/manual
    ActionPlane approval.
12. Fork pull requests fail closed and never receive production secrets or
    write authority.

## 5. Data ownership

- Agent local stores own deployments, service runtime state, telemetry,
  incidents, runtime audit, and bounded evidence inputs.
- Cloud Postgres owns users, PAT hashes, organizations/projects/memberships,
  nodes/agents, bootstrap/deployment job metadata, OTP state, idempotency, and
  control-plane audit. The target adds installation/repository mapping,
  `BuildRecord` metadata, workflow/run identity, image digest, deployment policy,
  routing, deployment result metadata, and provenance references.
- CLI owns only local session state, OS-keychain credentials, configuration, and
  future device/MCP state necessary for the local trust boundary.
- The OCI registry owns artifact blobs and manifests. GitHub Actions owns the
  build execution; Agent owns digest pull, runtime rollout, readiness,
  reconciliation, and rollback.
- Cloud must not own source repository contents, Docker build context, raw build
  logs, raw runtime logs, application secrets, registry password plaintext, or
  kubeconfig.

## 6. Architecture update rule

Update this document whenever protocol, authentication, data ownership,
cross-domain flow, public contract, AI boundary, approval channel, or managed
gateway ownership changes. Update `docs/status_matrix.md` separately with actual
implementation evidence.
