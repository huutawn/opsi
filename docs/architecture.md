# Opsi Architecture

| Metadata | Value |
|---|---|
| Version | 5.0 |
| Status | Active architecture map |
| Last updated | 2026-07-12 |
| Requirements | `docs/opsi_srs.md` |
| Implementation truth | `docs/current_state.md`, `docs/status_matrix.md` |

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

Agent
  -> K3s/containerd
  -> local SQLite/runtime stores
```

No current path contains an AI provider, MCP server, `IncidentEvidence v1`,
`ActionPlan`, approval grant, or Safe ActionPlane.

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

Bootstrap Worker is a long-running, single-concurrency Cloud-side worker. It
polls `POST /internal/bootstrap/sessions/lease`; the registry atomically claims
the oldest pending session and stores only a hash of the one-time lease token.
Worker status, progress, and finish calls require both worker identity and the
raw lease token. Lease heartbeat, renewal, recovery, retry, and dead-letter
semantics are not implemented until V3-010.

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
TLS certificate. The removed `deployment.ingress_enabled` key fails fast.

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

## 2. Target architecture — not implemented at M0

```text
Codex / Claude Code / Antigravity / compatible MCP client
  -> opsi mcp serve
  -> CLI local backend
       -> Agent read-only evidence APIs
       -> Agent typed action/preflight APIs

Human Local UI or trusted interactive CLI
  -> separate approval interaction
  -> signed ApprovalGrant
  -> Agent typed executor
  -> post-check and audit
```

Vendor names are examples of MCP clients. Opsi must provide one vendor-neutral
local bridge, not provider-specific runtime architecture.

### 2.1 Target evidence flow

Agent will build `IncidentEvidence v1` from bounded, redacted runtime facts.
Evidence includes deployment diff, health/metric/event timelines, log
fingerprints/excerpts, topology impact, action/deployment history, evidence hash,
and prompt-injection tags. Cloud does not receive the evidence body.

### 2.2 Target action flow

```text
AI proposes typed ActionPlan
  -> Agent deterministic ActionPreflight
  -> approval challenge
  -> human reviews outside MCP
  -> registered local device signs ApprovalGrant
  -> Agent verifies grant, state, policy, lock, and preconditions
  -> allowlisted typed executor
  -> post-check, rollback result, audit
```

The MCP surface may read, preflight, and request an approval challenge. It has no
execute tool and no approval-grant tool.

### 2.3 Target delivery and gateway flow

Phase 4 will narrow delivery to an exact Git SHA and Opsi-rendered Deployment,
ClusterIP Service, and Traefik Ingress from typed `ExposureSpec`, with conflict
checks, readiness, and rollback. This target is not current behavior.

## 3. Trust boundaries

| Boundary | Trusted data | Untrusted data | Secret policy | Mutation policy |
|---|---|---|---|---|
| Browser <-> CLI | Short local session, explicit user input after validation | Browser state, request bodies, rendered runtime text | No long-lived PAT, Agent token, device key, or reusable grant in Browser | Mutations require local session, CSRF/origin controls, project scope, RBAC, and idempotency |
| AI client <-> MCP | Versioned tool schemas and bounded typed responses | AI output, prompts, copied logs, commit/application text | No PAT, Agent token, device private key, secret value, or ApprovalGrant | Read/preflight/challenge request only; no execute or approve tool |
| CLI <-> Cloud | Authenticated metadata and versioned envelopes | Provider callbacks, webhook metadata, user-supplied identifiers | PAT stays in OS keychain; Cloud stores hashes; no runtime secrets | Metadata/control-plane mutations only; Cloud does not execute K3s operations |
| CLI <-> Agent | Versioned gRPC types, authenticated project/actor context | User/AI reason text and evidence references | TLS/mTLS/pinning target; no credential forwarding to AI | Current typed APIs only; future actions require deterministic preflight and valid grant |
| Cloud <-> Agent | Signed registration, heartbeat, lease, and result envelopes | Remote errors and external webhook-derived metadata | Agent credential/HMAC protected; no raw runtime evidence in Cloud | Agent performs runtime work; Cloud records sanitized result metadata |
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

## 5. Data ownership

- Agent local stores own deployments, service runtime state, telemetry,
  incidents, runtime audit, and bounded evidence inputs.
- Cloud Postgres owns users, PAT hashes, organizations/projects/memberships,
  nodes/agents, bootstrap/deployment job metadata, OTP state, idempotency, and
  control-plane audit.
- CLI owns only local session state, OS-keychain credentials, configuration, and
  future device/MCP state necessary for the local trust boundary.

## 6. Architecture update rule

Update this document whenever protocol, authentication, data ownership,
cross-domain flow, public contract, AI boundary, approval channel, or managed
gateway ownership changes. Update `docs/status_matrix.md` separately with actual
implementation evidence.
