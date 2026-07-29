# ADR-005: Manual Placement and DeploymentPolicy Boundary

- Status: Accepted for R5-009
- Date: 2026-07-19
- Scope: Cloud control plane, CLI, Local API, and Local UI

## Context

R5-008 established the trusted GitHub Actions OIDC and `BuildRecord` admission
boundary. R5-009 adds a manual control-plane path for selecting a runtime and
preflighting artifact routing. These decisions must not create a second meaning
for workload admission or make a manual policy capable of weakening the OIDC
security configuration.

## Decision

`BuildRecord` admission and deployment routing are separate authorities:

1. GitHub OIDC issuer, JWKS, audience, signature, claim binding, active
   repository claim, service binding, and the existing static
   `githuboidc.WorkloadPolicy` remain authoritative for accepting a
   `BuildRecord`.
2. `DeploymentPolicy v1` is evaluated only after an accepted `BuildRecord`
   exists. It exact-matches repository, service, workflow/job workflow, event,
   Git ref, environment, runtime allowlist, platform, OCI identity, config
   hash, and build-plan hash. It cannot override OIDC issuer, JWKS, audience,
   cryptographic verification, or the R5-008 admission checks.
3. `TopologyPlan v1` records the desired manual service-to-runtime placement.
   Exposure is intent metadata only; applying a plan creates no DNS, route,
   certificate, port mapping, `DeploymentJob`, or Agent command.
4. Routing is a deterministic preflight result. It requires exactly one active
   matching policy, one matching topology assignment, a project-owned active
   single-node K3s runtime, fresh server-time heartbeats, healthy node state,
   capacity, and exactly one eligible deploy Agent. Zero or multiple eligible
   Agents fail closed with typed errors.
5. Runtime capacity is authoritative only when observed from Agent/node facts or
   stored as a separate audited `operator_declared` record. Unknown capacity
   fails closed unless the active matching DeploymentPolicy explicitly grants
   `allow_unknown_capacity`; that exception is scoped to its environment,
   service keys, and runtime IDs. Rationale text never changes authorization or
   validation.
6. Each R5-009 runtime represents one K3s node. A runtime with multiple nodes is
   rejected as unsupported instead of selecting one by iteration order.

## Persistence and audit

Topology and policy revisions are immutable append-only records referenced by a
mutable head. Disable/supersede is represented as a new revision, never a
physical delete. PostgreSQL mutation idempotency records bind an operation,
project, key, and payload hash. Exact replay returns the original result with
`reused=true`; a different payload under the same key returns typed `409`.
Expected revision and state hash provide optimistic concurrency. Audit actor
identity comes from the authenticated principal, not request JSON.

## Consequences

- CLI and Local UI call the same Cloud application contracts through the Local
  API proxy; browsers never call Cloud or Agent directly and never store PATs.
- A policy may be active while its target runtime is unhealthy, but routing and
  topology validation still fail closed until factual health/capacity conditions
  pass.
- R5-009 deliberately stops before `DeploymentJob`, digest deployment,
  exposure reconciliation, Agent mutation, MCP, or AI features.

## Rejected alternatives

- Combining static OIDC policy and database policy with `OR`, which would allow
  a new policy to bypass R5-008 admission.
- Arbitrary expressions, regexes, scripts, Rego/CEL, shell, or AI-generated
  authority in policy fields.
- Choosing the first Agent or using map/insertion order when health is
  ambiguous.
