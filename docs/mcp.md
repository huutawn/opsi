# Opsi MCP-01 through MCP-05: Read-Only Model Context Protocol Surface

## Overview

Opsi Model Context Protocol (MCP) server provides AI agents and external tools with a safe, read-only window into Opsi project topology, source snapshots, deployment preflight evaluations, and 5-layer dependency verifications.

MCP-01 through MCP-05 are strictly **non-operational**. They introduce zero mutations into the Opsi control plane or running environments. The local Dashboard can connect Codex to this surface, but Review, Approve, and Apply remain separate human actions outside MCP.

MCP-02 adds an advisory dependency-proposal boundary. Opsi provides bounded,
exact-source-bound facts and deterministically validates a typed candidate with
the same Cloud-side ADC validation and diff authority used by manual review.
An external MCP-capable AI client performs any reasoning; Opsi does not invoke
or configure an LLM. Proposals are client-side, non-authoritative, and are not
persisted.

MCP-03 adds the same bounded advisory boundary for source patches. An external
client reasons over MCP-01 facts and optional MCP-02 intent, then submits a
typed exact-source patch candidate. Opsi validates provenance, Git blob
preimages, constrained unified diff syntax, and a virtual in-memory apply. A
patch is never written, applied, committed, compiled, or tested by MCP.

---

## Architectural Boundary

The MCP server runs at the **local Opsi Edge boundary** (the CLI on the operator's machine):

```
+-------------------------------------------------------------------------+
| Local Operator Machine                                                  |
|                                                                         |
|  +--------------------+                     Stdio                             |
|  |   MCP Client       | -------------------------------------+          |
|  | (AI Assistant/IDE) |                                      |          |
|  +--------------------+                                      v          |
|                                                     +-----------------+ |
|                                                     |    opsi mcp     | |
|                                                     |  (MCP Server)   | |
|                                                     +--------+--------+ |
|                                                              |          |
|                 +--------------------------------------------+          |
|                 |                                            |          |
|                 v                                            v          |
|    +--------------------------+                 +---------------------+ |
|    | Local Git Object Store   |                 | Local Session/PAT   | |
|    | (Exact commit reading)   |                 | (Keychain store)    | |
|    +--------------------------+                 +----------+----------+ |
+------------------------------------------------------------|------------+
                                                             | HTTPS Bearer PAT
                                                             v
                                                  +---------------------+
                                                  |     Opsi Cloud      |
                                                  |   (Control Plane)   |
                                                  +---------------------+
```

### Security & Isolation Principles
- **Local Boundary**: MCP clients never receive raw Cloud PATs, Agent TLS certificates, kubeconfigs, or database passwords.
- **Zero Domain Mutations**: MCP exposes 0 mutation tools. There are no tools to create, update, delete, apply, deploy, build, rotate secrets, acknowledge preflight warnings, or trigger verifications.
- **Exact Source Provenance**: Source file listing, reading, and searching require an explicit or resolved immutable Git commit SHA. If the exact commit is unavailable, the server returns `SOURCE_SNAPSHOT_UNAVAILABLE` rather than falling back to uncommitted local working trees.
- **Strict Secret Redaction**: All source reads, search snippets, and evidence outputs pass through regex scanners that redact URI credentials (`postgres://user:[REDACTED]@host`), tokens, and private keys.
- **Path Traversal Protection**: Any path containing `..`, absolute paths, null bytes, or escaping `ApplicationRoot` is strictly rejected with `SOURCE_PATH_INVALID`.

---

## Transport Mode

### Stdio
Standard JSON-RPC 2.0 over standard input and standard output (newline-delimited JSON). Diagnostic logs are written exclusively to `stderr` to maintain protocol stream integrity. Both `opsi mcp` and `opsi mcp serve` share the exact same authority and start the stdio server.

```bash
opsi mcp
# or
opsi mcp serve
```

---

## Available Read-Only Tools (24)

| Tool Name | Scope / Purpose | Key Safeguards & Bounds |
| :--- | :--- | :--- |
| `project_review_context` | One bounded composition of current project, application, topology, deployment, and configuration facts for an external review agent. | Reuses existing authorities; always `action: NONE`; does not store an AI review state. |
| `deployment_readiness_context` | Single bounded snapshot of current source, configuration/dependency realization, BuildRecord, placement, canonical preflight, deployment, and verification facts for one application/environment. | Always `action: NONE`; derives facts on each read; no persisted AI workflow, reasoning, acknowledgement, or operational action. A PASS still requires the human to use the canonical deployment review surface. |
| `project_context` | High-level project summary, counts, topology revision, deployment state. | Redacted facts, zero credentials. |
| `topology` | Applied topology plan (runtimes, nodes, assignments, state hashes). | Canonical applied plan only. |
| `applications_list` | List applications with source bindings and placement runtimes. | Bounded list (max 100). |
| `application_get` | Detailed configuration, safe env keys, public route, build record. | Secret values excluded (key names only). |
| `application_dependencies` | ADC-01 dependency contracts and realization facts. | Passwords/DSNs omitted. |
| `managed_resources_list` | List managed databases, caches, queues. | Bounded list (max 100). |
| `managed_resource_get` | Detailed resource metadata, safe endpoints, binding count. | Zero credentials. |
| `build_records_list` | List immutable BuildRecords with workload SHA and artifact digest. | Cursor pagination, bounded limit. |
| `build_record_get` | Detailed immutable BuildRecord metadata and provenance. | Read-only. |
| `deployments_list` | List deployment jobs with rollout state and outcomes. | Outcome != runtime health. |
| `deployment_get` | Detailed single deployment job state and timeline. | Read-only. |
| `deployment_preflight` | Evaluate ADC-04 deployment preflight checks (PASS/WARN/BLOCK). | Zero-mutation evaluation. Cannot acknowledge warnings. |
| `source_risk_report` | ADC-05 source risk analysis report with findings. | Heuristic findings with `[REDACTED]` evidence. |
| `dependency_verification_latest` | Latest ADC-05 5-layer dependency verification run result. | Read-only 5-layer outcome facts. |
| `dependency_verification_history` | Historical verification runs for a deployment job. | Read-only. |
| `source_files_list` | List files inside `ApplicationRoot` at an exact commit SHA. | Bounded (max 200 files), ignores `.git/`, `.env*`. |
| `source_file_read` | Read content of a file in `ApplicationRoot` at an exact commit SHA. | Max 256 KiB, binary detection, credential redaction, path traversal rejection. |
| `source_search` | Deterministic literal text search at an exact commit SHA. | Max 50 matches, credential redaction in snippets, bounded scan. |
| `dependency_analysis_context` | Bounded facts for external dependency analysis for one application/environment. | Current immutable BuildRecord commit, `ApplicationRoot`, configuration/dependency/topology hashes, compatible targets, bounded risk/verification facts; no source dump or secrets. |
| `validate_dependency_proposal` | Validate an external typed dependency proposal. | Always `action: NONE`; detects stale provenance; reuses canonical ADC validation/diff endpoints; never applies, persists, builds, deploys, or realizes. |
| `validate_service_configuration_proposal` | Validate and semantically diff a complete ServiceConfiguration proposal, including non-secret variables, bindings, dependencies, resources, and public routes. | Revision/state-hash stale check; canonical Cloud preview/validation/diff; returns generated variable names but not generated values; never persists or applies. |
| `validate_source_patch_proposal` | Validate an external typed exact-source patch proposal. | Always `action: NONE`; max 8 files, 32 hunks, 128 KiB, and 1000 changed lines; exact blob preimage and virtual apply only; never writes, applies, persists, builds, or tests. |

### MCP-02 proposal lifecycle

```
Opsi factual context -> external AI reasoning -> typed proposal -> Opsi deterministic validation
```

The proposal provenance contains the exact source commit, `ApplicationRoot`, and
`analysis_inputs_hash`. The hash changes when relevant source, configuration,
dependency contract, compatible target, or topology/route inputs change. An old
proposal returns `STALE`; Opsi never rebases or retargets it. Results are one of
`VALID`, `VALID_WITH_WARNINGS`, `INVALID`, `STALE`, or `NO_CHANGE_PROPOSED`,
with explicit target resolution (`RESOLVED`, `TARGET_AMBIGUOUS`, or
`TARGET_NOT_FOUND`) and no apply action.

Evidence is limited to 20 redacted references with excerpts capped at 512 bytes.
The only confidence values are `HIGH`, `MEDIUM`, and `LOW`; confidence is an AI
client assertion, not an Opsi probability score. Source text, names, excerpts,
and proposal fields are treated as data and cannot add capabilities.

MCP does not provide ServiceConfiguration apply, dependency apply, resource-binding realization,
managed-resource creation, build, deployment, warning acknowledgement,
verification trigger, source patching, shell execution, arbitrary HTTP, secret
access, or an approval action.

### MCP-03 source patch lifecycle

```
MCP-01 source facts + optional MCP-02 intent -> external AI reasoning -> typed patch -> deterministic virtual validation
```

Each patch binds to the current exact `BuildRecord`, source commit,
`ApplicationRoot`, current analysis-inputs hash, and canonical Git blob ID for
every modified file. If a referenced MCP-02 proposal is relevant, its proposal
hash and analysis hash are bound too. Changed source, blobs, or relevant
configuration causes `STALE`; unrelated project state does not.

Only existing UTF-8 text files inside `ApplicationRoot` may be modified. The
validator rejects traversal, `.git`, cross-application paths, symlink/mode,
rename, create/delete, binary, generated/vendor output, malformed diffs,
oversized patches, and credential-like added literals. Hunk context must match
the immutable preimage at its declared line exactly—there is no fuzzy rebase.
Added lines, rationale, evidence, and previews are secret-scanned/redacted.

Results are `VALID`, `VALID_WITH_WARNINGS`, `INVALID`, `STALE`, or
`NO_SOURCE_CHANGE_PROPOSED`; all include `action: NONE`. Validity is structural
only: `PATCH_HAS_NOT_BEEN_COMPILED_OR_EXECUTED` remains explicit. A patch that
uses an un-applied MCP-02 mapping returns
`DEPENDS_ON_UNAPPLIED_DEPENDENCY_PROPOSAL`; a configuration-only alternative is
reported rather than hidden. Source and proposal prompt-injection text is data
only and cannot expand MCP capabilities.

After a successful assistant turn, a human developer may explicitly choose
**Apply to local worktree** in the Dashboard. That Local API action is outside
MCP and only accepts the exact patch retained by the attested turn. It requires
the reviewed commit to still be `HEAD`, every target Git blob and worktree file
to match the original preimage, and every target to be clean, regular and
inside the repository. Opsi writes atomically with a local `.git/opsi/source-patch`
recovery journal. It never builds, tests, stages, commits, pushes, or opens a
pull request. Cloud ProposalReview accepts configuration changes only; source
patches are not persisted in Cloud.

### Local AI Assistant

`opsi start` exposes an AI Assistant destination backed by a local provider
bridge. The current provider is Codex CLI; the bridge uses a provider interface
so another local agent can replace it without adding another Opsi review or
configuration implementation. Codex runs in an isolated empty workspace and
receives project facts through an `opsi mcp` child configured for the selected
project. The bridge prompt prohibits shell, filesystem, web, and non-Opsi tools.
Codex owns its thread history; Opsi keeps only a bounded in-memory turn
projection. Native shell, execution, browser, web, plugin, app, computer, and
image tools are disabled for assistant turns.

Agent configuration output is structured and must first pass
`validate_service_configuration_proposal`. The browser rechecks the current
revision/state hash and Cloud validation/diff before it creates the authoritative
`service_configuration` ProposalReview. A human then performs three distinct
actions: Create review, Approve, and Apply. Restarting the local CLI loses the UI
projection but not Cloud ProposalReview records; listing historical agent
threads in the Dashboard is not implemented.

---

## MCP-05 operator readiness

`deployment_readiness_context` is the one MCP-05 addition. It is a compact
read of one application and target environment, not an AI workflow or a second
deployment state machine. It reuses the same factual authorities exposed by
the rest of Opsi:

| Readiness field | Canonical source | Meaning |
| :--- | :--- | :--- |
| `source` | immutable BuildRecord provenance and source binding | An exact commit is either bound to the selected record or unavailable; MCP never uses a mutable branch or working tree. |
| `dependencies` | ServiceConfiguration and ResourceBinding realization facts | The configuration contract and required managed-resource binding state. Final deployability remains canonical preflight. |
| `build` | BuildRecord | `CURRENT` means a succeeded immutable BuildRecord; `REQUIRED`, `FAILED`, and `NOT_ACCEPTED` cannot be used for deployment. |
| `placement` | applied Topology | The currently applied runtime assignment, if one exists. |
| `preflight` | ADC-04 `deployments/preflight` | Fresh `PASS`, `PASS_WITH_WARNINGS`, or `BLOCKED` result for the current BuildRecord and environment. Warnings remain unacknowledged in this read. |
| `deployment` | DeploymentJob | Durable rollout facts only; `COMPLETED` is not a verification claim. |
| `verification` | latest ADC-05 runs | `VERIFIED`, `PARTIALLY_VERIFIED`, `FAILED`, `STALE`, or `NOT_RUN`; stale never becomes current success. |

The context always returns `action: "NONE"`. It intentionally does not return
`READY_TO_DEPLOY`: a PASS means that the human may open the existing Deployment
Review, which creates the exact current review and, where applicable, provides
the explicit warning acknowledgement. A `PASS_WITH_WARNINGS` result never
contains an acknowledgement, and a `BLOCKED` result is passed through with its
canonical checks and remediation codes.

An external AI may use these facts to explain a workflow such as dependency
review, source change required, build required, placement required, preflight
blocked, warning acknowledgement required, deployment in progress, or
verification failure. Opsi stores none of that interpretation. A source patch
review remains a stopping point: the human can copy it and make a repository
commit outside Opsi; no MCP or Cloud source-write authority exists.

---

## Standard Error Codes

- `AUTH_REQUIRED`: Local session is unauthenticated (no PAT in keychain). Run `opsi login` outside MCP.
- `FORBIDDEN`: The authenticated session lacks permission for the requested project or resource.
- `NOT_FOUND`: The requested application, resource, build record, or deployment was not found.
- `AMBIGUOUS_PROJECT`: Multiple projects are available; the tool caller must provide `project_id`.
- `SOURCE_SNAPSHOT_UNAVAILABLE`: The exact Git commit SHA is missing or unavailable in the local repository.
- `SOURCE_PATH_INVALID`: The requested file path is invalid, absolute, contains `..`, or escapes `ApplicationRoot`.
- `SOURCE_BINARY_UNSUPPORTED`: The requested file is binary and cannot be returned as text.
- `INVALID_ARGUMENT`: A required parameter was missing or malformed.
- `PATCH_MALFORMED`: The constrained unified diff is malformed or requests an unsupported patch operation.
- `PATCH_PREIMAGE_MISMATCH`: An exact hunk or declared base blob no longer matches immutable source; the result is `STALE`.
- `SECRET_LITERAL_INTRODUCED`: Rationale, evidence, or an added line contains a credential-like literal.
- `PATCH_TARGET_GENERATED`: The patch targets generated, vendor, or build output.
- `AUTHORITY_UNAVAILABLE`: The underlying Cloud or local authority returned an unexpected error.
