# Opsi MCP-01 + MCP-02: Read-Only Model Context Protocol Surface

## Overview

Opsi Model Context Protocol (MCP) server provides AI agents and external tools with a safe, read-only window into Opsi project topology, source snapshots, deployment preflight evaluations, and 5-layer dependency verifications.

MCP-01 and MCP-02 are strictly **non-operational**. They introduce zero mutations into the Opsi control plane or running environments.

MCP-02 adds an advisory dependency-proposal boundary. Opsi provides bounded,
exact-source-bound facts and deterministically validates a typed candidate with
the same Cloud-side ADC validation and diff authority used by manual review.
An external MCP-capable AI client performs any reasoning; Opsi does not invoke
or configure an LLM. Proposals are client-side, non-authoritative, and are not
persisted.

---

## Architectural Boundary

The MCP server runs at the **local Opsi Edge boundary** (the CLI on the operator's machine):

```
+-------------------------------------------------------------------------+
| Local Operator Machine                                                  |
|                                                                         |
|  +--------------------+         Stdio / Loopback HTTP                   |
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
- **Zero Domain Mutations**: MCP-01 exposes 0 mutation tools. There are no tools to create, update, delete, apply, deploy, build, rotate secrets, acknowledge preflight warnings, or trigger verifications.
- **Exact Source Provenance**: Source file listing, reading, and searching require an explicit or resolved immutable Git commit SHA. If the exact commit is unavailable, the server returns `SOURCE_SNAPSHOT_UNAVAILABLE` rather than falling back to uncommitted local working trees.
- **Strict Secret Redaction**: All source reads, search snippets, and evidence outputs pass through regex scanners that redact URI credentials (`postgres://user:[REDACTED]@host`), tokens, and private keys.
- **Path Traversal Protection**: Any path containing `..`, absolute paths, null bytes, or escaping `ApplicationRoot` is strictly rejected with `SOURCE_PATH_INVALID`.

---

## Transport Modes

### 1. Stdio (Default)
Standard JSON-RPC 2.0 over standard input and standard output (newline-delimited JSON). Diagnostic logs are written exclusively to `stderr` to maintain protocol stream integrity.

```bash
opsi mcp
# or
opsi mcp serve
```

### 2. Loopback HTTP (Optional)
HTTP POST endpoint running exclusively on local loopback (`127.0.0.1` or `localhost`).

```bash
opsi mcp serve --addr 127.0.0.1:9781
```

---

## Available Read-Only Tools (20)

| Tool Name | Scope / Purpose | Key Safeguards & Bounds |
| :--- | :--- | :--- |
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

MCP-02 does not provide dependency apply, resource-binding realization,
managed-resource creation, build, deployment, warning acknowledgement,
verification trigger, source patching, shell execution, arbitrary HTTP, secret
access, or a model-provider integration.

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
- `AUTHORITY_UNAVAILABLE`: The underlying Cloud or local authority returned an unexpected error.
