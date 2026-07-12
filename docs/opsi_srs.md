# Opsi Software Requirements Specification

| Metadata | Value |
|---|---|
| Title | Opsi Software Requirements Specification |
| Version | 5.0 |
| Status | Active target contract; current implementation is tracked separately in `docs/status_matrix.md` |
| Last updated | 2026-07-12 |
| Supersedes | SRS v4.0 current direction; SRS v3.2 remains archived |
| Canonical roadmap | `docs/opsi_roadmap_v3/00_MASTER_ROADMAP.md` and `docs/opsi_roadmap_v3/12_EXECUTION_BACKLOG.md` |

This SRS defines the intended Production MVP. It is not evidence that a
requirement is implemented. Only `docs/current_state.md` and
`docs/status_matrix.md`, backed by code, tests, commands, and real-infrastructure
artifacts, describe implementation status.

## 1. Scope layers

### 1.1 Current implementation at M0

- Cloud has no AI runtime, provider integration, prompt, fixture response, or
  `/v1/ai/*` route.
- Agent has no AI analyzer, fallback RCA, provider/model metadata, RCA-backed
  approval path, or incident-owned Kubernetes mutation executor.
- The active incident surface is list, get, and resolve, with project/role
  authorization and resolve audit.
- Agent retains an internal bounded sanitized incident context builder. Opsi has
  no public `IncidentEvidence v1` API.
- Opsi has no Safe ActionPlane, no CLI MCP bridge, and no managed gateway.
- User-provided deployment manifests may define their own Service, Ingress,
  Gateway, TLS, or lifecycle resources; Opsi does not render or own them.
- Historical incident columns `rca_result` and `mitigation_actions_json` are
  storage-only compatibility data. Active runtime code does not read, expose,
  authorize, or execute them.
- Clean VPS/K3s automation checks factual incident list/get/resolve and resolve
  audit. No committed real-infrastructure pass artifact proves the full path.

### 1.2 Production MVP target

Production MVP follows roadmap v3: repeatable control-plane deployment, clean
VPS bootstrap, exact Git SHA delivery, Opsi-rendered Deployment/Service/Traefik
exposure, deterministic incident evidence, a typed Safe ActionPlane, a
user-owned CLI-side MCP bridge, protected end-to-end proof, and production
hardening/recovery gates.

### 1.3 Post-v1 and future scope

HA and multi-node operation, additional managed databases, provider-specific
integrations, multi-cloud provisioning, generic Helm/manifests, conversational
product chat, and autonomous multi-step workflows are post-v1. They must not be
used as Production MVP acceptance evidence.

## 2. Product definition and non-goals

Opsi is a local-first operations platform for small teams. It is not a generic
SSH shell, Kubernetes dashboard, database console, Terraform replacement, AI
provider, or autonomous SRE.

Production MVP explicitly forbids:

- free-form shell;
- arbitrary `kubectl` or arbitrary Kubernetes apply/patch;
- arbitrary SQL DDL or DML;
- mutation of Kubernetes `kube-system`;
- K3s uninstall;
- host filesystem deletion;
- credential or secret export;
- firewall or OS package mutation requested by AI;
- database data mutation requested by AI;
- autonomous approval;
- autonomous destructive remediation.

These operations are forbidden or deferred, not R4 actions that become valid
after approval.

## 3. Ownership and boundaries

### SYS-OWN-01 — Cloud ownership

Cloud owns:

- organization, project, membership, and RBAC metadata;
- bootstrap jobs and Agent registration;
- deployment job relay and sanitized result metadata;
- audit/control-plane metadata;
- OTP/authentication;
- durable PostgreSQL state for the control-plane slice.

Cloud must not own:

- an LLM provider, model key, prompt, or AI conversation;
- raw runtime logs or raw metric streams;
- Kubernetes execution;
- application secret values, kubeconfig, or device private keys;
- RCA generation;
- `ActionPlan` execution.

### SYS-OWN-02 — CLI/local backend ownership

The CLI/local backend owns:

- local user session and Browser-to-Agent mediation;
- OS-keychain PAT storage;
- project selection and local policy presentation;
- the future MCP server and local AI integration boundary;
- the separate human approval interaction and future device signing.

The Browser must not receive a long-lived PAT, Agent credential, device private
key, or approval grant.

### SYS-OWN-03 — Agent ownership

Agent owns:

- runtime facts and telemetry collection;
- redacted evidence construction;
- deployment and service runtime execution;
- secret runtime;
- deterministic preflight and future policy enforcement;
- future typed allowlisted execution;
- post-check and runtime audit.

Agent must not own an LLM provider, conversational agent, prompt orchestration,
or AI approval decision.

## 4. Local-first request paths

Current production-oriented paths are:

```text
Browser -> CLI local backend -> Cloud metadata APIs
Browser -> CLI local backend -> Agent gRPC runtime APIs
Cloud -> bootstrap/deployment job relay -> Agent cloud runner
Agent -> K3s/containerd and local SQLite/runtime stores
```

Core Browser workflows must use `/api/local/...`. Cloud may coordinate metadata
and job envelopes but must not become the runtime execution plane.

## 5. AI and MCP target requirements

### AI-01 — Target flow

The Production MVP target flow is:

```text
User AI agent
-> opsi mcp serve
-> CLI local backend
-> read-only redacted evidence
-> typed ActionPlan proposal
-> Agent deterministic preflight
-> approval challenge
-> separate human approval
-> signed ApprovalGrant
-> Agent typed executor
-> post-check and audit
```

At M0 the MCP bridge and ActionPlane are `NOT_IMPLEMENTED`.

### AI-02 — Credential and connection isolation

- External AI clients must not receive PATs, Agent tokens, device private keys,
  approval grants, secret values, or unrestricted runtime payloads.
- AI must not connect directly to Agent or establish an authenticated Agent
  control channel.
- Opsi must not require provider-specific runtime adapters. Codex, Claude Code,
  Antigravity, and other names are examples of compatible MCP clients only.

### AI-03 — MCP surface

- MCP read tools return bounded structured data from explicitly selected
  projects.
- MCP may request preflight and creation of an approval challenge.
- MCP must not expose an execute tool.
- MCP must not expose an approve or approval-grant tool.
- ApprovalGrant must not be returned in MCP output or an AI conversation.
- Opsi must remain usable without an AI client or MCP enabled.

### AI-04 — Untrusted input policy

AI output, logs, commit messages, image labels, Kubernetes events, and
application output are untrusted input. Evidence text must be tagged, redacted,
bounded, stripped of control sequences, and kept separate from tool policy and
approval instructions. Prompt-injection content must not choose an action type,
change risk, bypass approval, or alter typed executor arguments.

### AI-05 — Approval

Every AI-originated mutation in v1 requires a human approval interaction outside
the AI/MCP channel. Agent deterministic policy is authoritative; AI reasoning is
advisory only.

## 6. Incident requirements

### INC-01 — Current factual incident lifecycle

Current incident behavior includes deterministic detection, factual incident
records, list/get/resolve, project/role authorization, resolve audit, MTTR, and
an internal bounded sanitized context builder. The public response must not
expose legacy RCA/provider/recommended-action fields.

### INC-02 — IncidentEvidence v1 target

Phase 5 must define and expose a versioned `IncidentEvidence v1` containing:

- incident identity, severity, timestamps, and affected resources;
- deployment revision and desired-vs-observed diff;
- health transition timeline and metric summary;
- Kubernetes event summaries;
- redacted log fingerprints and bounded excerpts;
- dependency/topology impact;
- action and deployment timeline;
- evidence hash and sanitization metadata;
- prompt-injection tagging.

The bundle contains facts, not an authoritative root-cause assertion or command.
It is `NOT_IMPLEMENTED` at M0.

### INC-03 — Legacy storage

`rca_result` and `mitigation_actions_json` may remain temporarily for upgrade
compatibility. They must be ignored by active reads and execution, must not be
reinterpreted as `IncidentEvidence` or `ActionPlan`, and require a later explicit
migration before column removal.

## 7. Safe action requirements

### ACTION-01 — Versioned contracts

Phase 6 must define:

- `ActionPlan v1`;
- `ActionPreflight v1`;
- `ApprovalChallenge v1`;
- `ApprovalGrant v1`;
- `ActionResult v1`.

All are `NOT_IMPLEMENTED` at M0.

### ACTION-02 — Deterministic risk model

| Class | Meaning | Production MVP policy |
|---|---|---|
| R0 | Read-only | Redacted inspection; no `ActionPlan` required |
| R1 | Reversible service-local mutation | Human approval for AI origin |
| R2 | Availability/configuration impact | Human approval, preflight, lock, rollback plan |
| R3 | Data/node/network/security impact | Mostly deferred; Owner step-up where later allowed |
| R4 | Unbounded/destructive | Forbidden |

Risk classification must be deterministic, explainable, and Agent-owned. An LLM
must not classify or override risk.

### ACTION-03 — Planned Production MVP catalog

The planned catalog is:

```text
deployment.deploy
deployment.rollback
workload.restart
workload.scale
gateway.reconcile
incident.resolve
```

This catalog is not implemented as a unified ActionPlane at M0. Existing direct
deployment and incident-resolve behavior must not be described as proof of the
future contracts, approval grants, policy engine, or MCP integration.

### ACTION-04 — Execution invariants

Agent must accept only typed allowlisted executor inputs; verify project, actor,
role, origin, nonce, expiry, action hash, current-state hash, and grant signature;
lock the target; repeat preconditions; construct command arguments internally;
apply time/output bounds; run post-check; report rollback state; and audit denied,
stale, replayed, started, succeeded, failed, and rolled-back outcomes.

## 8. Deployment and gateway requirements

### DEPLOY-01 — Current deployment truth

Git-based deployment exists. User-provided manifests may define their own
runtime resources. Opsi does not currently generate or manage Ingress, Gateway
API routes, domains, or TLS certificates. The removed `IngressEnabled` option
must fail fast if found in old config. A public endpoint value must not be
interpreted as proof of an Opsi-managed gateway.

### GATEWAY-01 — Phase 4 target

Phase 4 must support exact Git SHA delivery, an Opsi-rendered Deployment,
ClusterIP Service, and Traefik Ingress from a typed `ExposureSpec`, including
hostname/path conflict checks, readiness, and rollback. This is `NOT_IMPLEMENTED`
at M0.

## 9. Security and data requirements

- Cloud stores PATs as bcrypt hashes; CLI stores the usable PAT in OS keychain.
- Secret reveal requires Owner plus OTP/TOTP and must return no-store responses.
- Cloud must not persist raw logs, raw metrics, app secrets, kubeconfig, source
  code, or long-lived runtime payloads.
- Audit metadata must be project-scoped and redacted; Cloud PostgreSQL audit is
  append-only at the application/database boundary where implemented.
- Blocking/remote operations must use bounded timeouts, cancellation, retries,
  idempotency, and deterministic resource cleanup.
- Source/release packages must reject local config, environment credentials,
  private keys, runtime certificates, databases, logs, and generated output.

## 10. Verification and status rules

Allowed status values are `DONE`, `PARTIAL`, `CONTRACT_ONLY`, `DOC_ONLY`,
`NOT_STARTED`, `FAILED_OR_REGRESSED`, `BLOCKED`, `UNPROVEN`, and
`MANUAL_GATED`.

`DONE` requires code, tests, configuration where applicable, an executable
verification command, and truthful documentation. Real-infrastructure work
remains `MANUAL_GATED` until a redacted pass artifact is committed and reviewed.
Production readiness remains unproven until roadmap v3 release/DR acceptance
gates pass.

## 11. Requirement migration from SRS v4

Stable IDs retain their semantics where possible. Old AI/RCA requirements are
superseded rather than silently reused.

| Old requirement | Disposition | Replacement requirement | Reason |
|---|---|---|---|
| FR1 local-first boundary | Retained | SYS-OWN-01 through SYS-OWN-03; Section 4 | Same local-first semantics with corrected ownership |
| FR2 identity/project registry | Retained | SYS-OWN-01; Section 9 | Cloud identity and metadata ownership remains valid |
| FR4 deployment management | Retained and narrowed | DEPLOY-01; GATEWAY-01 | Current Git deploy is separate from planned managed gateway |
| FR6 secrets/OTP/TOTP | Retained | Section 9 | Same security semantics |
| FR7 telemetry/logs | Retained | INC-01; INC-02; Section 9 | Facts remain local and become evidence later |
| FR8.1 incident detection | Retained | INC-01 | Deterministic factual incident lifecycle remains active |
| FR8.2 incident context sanitization | Retained and narrowed | AI-04; INC-02 | Internal sanitizer exists; public evidence API is future |
| FR8.3 Cloud AI proxy | `SUPERSEDED` | AI-01 through AI-03 | Cloud AI runtime was deleted; target is user-owned CLI-side MCP |
| FR8.4 RCA output contract | `SUPERSEDED` | INC-02; ACTION-01 | Facts and typed actions replace provider RCA authority |
| FR8.5 mitigation actions | `SUPERSEDED` | ACTION-01 through ACTION-04 | Old incident-coupled execution was removed |
| FR8.6 conversational AI | Deferred post-v1 | Section 1.3 | Not part of Production MVP |
| FR10 Local Web UI | Retained | SYS-OWN-02; Section 4 | CLI local backend remains trusted Browser boundary |

## 12. Milestone order

M0 ends with truthful deletion and documentation. Phase 2 begins with V3-009,
which converts the Bootstrap Worker from `RunOnce(session_id)` to a poll/lease
daemon. IncidentEvidence is Phase 5, Safe ActionPlane is Phase 6, CLI MCP is
Phase 7, and production acceptance follows the later hardening/release phases.
