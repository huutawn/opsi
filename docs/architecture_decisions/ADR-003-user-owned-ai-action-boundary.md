# ADR-003: User-owned CLI AI and Deterministic Agent Action Boundary

## Status

Accepted.

## Date

2026-07-12.

## Context

Earlier Opsi direction placed sanitized AI analysis behind a Cloud proxy and
coupled Agent incident handling to stored RCA/recommended actions. That design
made Cloud an AI runtime, gave incident storage an execution role, and blurred
the distinction between AI suggestion, authorization, and deterministic runtime
policy.

Phase 1 removed the Cloud AI runtime, Agent analyzer/fallback/provider metadata,
RCA-backed approval/execution, incident analyze/approve contracts, UI/CLI
surfaces, and Nginx-specific mitigation residue. Current incidents expose
factual list/get/resolve only. Opsi now needs an architecture that can support
optional user-owned AI without giving AI credentials or execution authority.

## Decision

1. AI is user-owned and connects through a future vendor-neutral CLI-side
   `opsi mcp serve` boundary.
2. External AI clients read only bounded redacted evidence and may propose a
   versioned typed `ActionPlan`.
3. AI clients must not connect directly to Agent or receive PATs, Agent tokens,
   device private keys, approval grants, or secret values.
4. Agent is the authoritative deterministic policy enforcement point. It owns
   risk classification, preflight, current-state validation, locks, typed
   allowlisted execution, post-check, rollback result, and runtime audit.
5. Human approval occurs outside the AI/MCP channel through a trusted Local UI
   or interactive CLI. The approval grant is signed by a registered local device
   and is never returned to MCP.
6. MCP exposes no execute tool and no approve/approval-grant tool. It may expose
   read, preflight, approval-challenge request, and status tools.
7. Historical `rca_result` and `mitigation_actions_json` data is storage-only and
   never an evidence, action, or authorization source.
8. `IncidentEvidence v1`, Safe ActionPlane, and CLI MCP are future roadmap
   components and are not implemented at M0.

## Consequences

- Cloud no longer needs model SDKs, provider keys, prompts, fallback responses,
  or AI request payloads.
- Agent incident code stays factual and deterministic. Evidence and action
  modules are introduced only in their ordered roadmap phases.
- Opsi can support multiple MCP clients without vendor-specific runtime code.
- Manual Opsi workflows remain usable when no AI client is configured.
- Approval and execution require more explicit contracts and device/security
  work, but AI cannot self-authorize or bypass Agent policy.
- Documentation and status must keep current implementation separate from the
  Production MVP target.

## Security invariants

- AI request channel != human approval channel.
- External AI clients must not establish a direct authenticated Agent control
  connection.
- ApprovalGrant must not appear in MCP output or AI conversation state.
- Logs, commit messages, events, image labels, and application output are
  untrusted and cannot modify tool policy, risk, or approval requirements.
- Agent executes only typed allowlisted operations with internally constructed
  arguments, bounded execution, post-check, and audit.
- R4 operations are forbidden, including free-form shell, arbitrary `kubectl`,
  arbitrary SQL, `kube-system` mutation, K3s uninstall, host deletion,
  credential export, and autonomous destructive remediation.

## Rejected alternatives

- **Cloud-hosted LLM proxy:** rejected because Cloud must not own provider keys,
  prompts, AI runtime, or runtime evidence payloads.
- **Agent-embedded LLM:** rejected because Agent must remain deterministic and
  operate without model/provider dependencies.
- **Direct AI-to-Agent connection:** rejected because it would expose Agent
  credentials and collapse the local mediation/approval boundary.
- **MCP execute or approve tool:** rejected because the AI channel must not
  authorize or directly initiate mutation.
- **Free-form shell or kubectl:** rejected because text filtering cannot provide
  bounded targets, stable risk, or reliable post-check semantics.
- **Historical RCA as execution authority:** rejected because legacy AI output is
  untrusted, unversioned storage and is not bound to current state or approval.

## Migration

V3-001 through V3-007 remove the old boundary. V3-008 aligns active
documentation and supersedes conflicting ADR metadata. Phase 5 introduces
`IncidentEvidence v1`; Phase 6 introduces deterministic action/approval
contracts and typed executors; Phase 7 introduces the CLI MCP bridge. Legacy RCA
columns remain storage-only until a later explicit database migration proves
upgrade compatibility and removal safety.
