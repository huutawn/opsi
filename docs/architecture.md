# Opsi Architecture

| Metadata | Value |
|---|---|
| Version | 6.1 |
| Status | Active architecture map |
| Last updated | 2026-07-24 |
| Requirements | `docs/opsi_srs.md` |
| Implementation truth | `docs/current_state.md`, `docs/status_matrix.md` |
| Canonical roadmap | `docs/opsi_roadmap_v5_production.md` |
| Trusted artifact decision | `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md` |

This document separates implemented architecture from later roadmap work. The
status matrix remains the evidence authority.

## 1. Current architecture

```text
Browser
  -> CLI local backend
       -> Cloud metadata/auth/bootstrap/deployment APIs
       -> Agent gRPC runtime APIs

Cloud
  -> durable bootstrap jobs
  -> accepted BuildRecord + topology/policy/routing
  -> durable DeploymentJob/RolloutIntent
       -> Agent PollJob

Agent
  -> ReconcileRollout -> ProductionAdapter
  -> Opsi-owned K3s resources
  -> local SQLite/runtime stores
```

GitHub App identity/repository binding, GitHub Actions OIDC, accepted
`BuildRecord`, digest deployment, `TopologyPlan`, `DeploymentPolicy`, routing,
and factual readiness/known-good rollback are implemented. No current path
contains an AI provider, MCP server, `IncidentEvidence v1`, `ActionPlan`,
approval grant, Safe ActionPlane, or pull-request preview environment.

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

Cloud provides GitHub App user authorization, installation authentication,
typed App-wide webhook intake, repository/service ownership, GitHub Actions
OIDC verification, and accepted BuildRecord storage. The generic GitHub push
relay and route-scoped webhook secrets are retired. The historical
`/webhooks/next` transport name remains, but `PollJob` carries only canonical
deployment or node lifecycle jobs; it is not a generic webhook relay.

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
project-scoped and idempotent. The worker processes at most one session and
uses the implemented resumable per-step BootstrapJob checkpoint state.

The worker uses a private control-plane URL for lease traffic and a distinct
Agent-reachable Cloud URL when installing a remote Agent. The operator-run K3s
acceptance validates a protected PEM/OpenSSH private-key file and pins one exact
SSH host-key fingerprint before bootstrap.

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

The sole executable delivery path is GitHub Actions OIDC -> accepted
`BuildRecord` -> immutable OCI digest -> `TopologyPlan` + `DeploymentPolicy` +
routing -> durable `DeploymentJob` + canonical `RolloutIntent` -> Agent `PollJob`
-> `ReconcileRollout` -> `ProductionAdapter` -> Opsi-owned K3s resources ->
factual readiness and known-good rollback. Agent source clone/build, caller-supplied
manifests, direct deployment RPC, service-scoped deployment creation, and the
generic push relay are retired. Opsi does not yet provision DNS or certificates.

New BuildRecord deployments never create active `immutable_image` jobs. A
missing RolloutIntent is a retired historical command and fails closed before
Kubernetes mutation. No-external workloads use an empty exposure snapshot and
never render a hidden Ingress; existing authoritative exposure is preserved on
image redeployment.

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

## 2. Trusted artifact architecture

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

The implemented delivery boundary has five separate trust paths:

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

### 2.3 Delivery and gateway flow

An allowed `BuildRecord` creates or reuses a durable `DeploymentJob`. Agent
validates the allowlisted image repository and full `sha256` digest, pulls the
immutable artifact, and deploys it through an Opsi-rendered Deployment and
ClusterIP Service. Typed `ExposureSpec` produces an Opsi-owned Traefik resource
with hostname/path conflict detection. Readiness, last-known-good digest,
automatic rollback, post-check, and restart reconciliation complete the
deployment transaction.

Agent never receives Git source, Docker build input, arbitrary manifests, or a
caller-selected runtime target for deployment.

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
