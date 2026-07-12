# Opsi Hard Rules

This is hot context. Keep it short. Full requirements live in `docs/opsi_srs.md`.

## Architecture

- Monorepo domains: `agent/`, `cli/`, `cloud/`, `contracts/`.
- No internal imports between `agent/`, `cli/`, and `cloud/`.
- Shared types/schemas/generated clients belong in `contracts/`; no business logic there.
- Product model is project-first: `User -> Project -> Node -> Service -> Pod`.
- Every operational action after login must be scoped by `project_id`.

## Ownership

- Cloud owns identity, project membership, PAT verify, OTP, webhook relay, and notification. Cloud does not own AI runtime or AI providers.
- Cloud must not store raw logs, raw metrics, K3s secrets, app secrets, kubeconfig, Docker layers, or long-lived operational payloads.
- Agent owns deployment, K3s operations, secrets, telemetry, incidents, audit, local sync buffer.
- CLI owns presentation, local commands, Agent proxy/bridge, and OS keychain PAT storage only.

## Security

- CLI-Agent privileged traffic must use TLS 1.3/mTLS/cert pinning in production.
- PATs stored by Cloud must be bcrypt hashes; CLI PATs must use OS keychain.
- Never log PAT, OTP, TOTP secret, private key, kubeconfig, app secret, or AI provider API key.
- Secret reveal requires Owner role plus OTP/TOTP.
- Audit sensitive actions append-only at app level.
- OTP TTL is 5 minutes; OTP requests max 5 per 15 minutes per user.

## Local-First AI

- Roadmap v3 assigns future user-owned AI integration to a CLI-side bridge; that MCP bridge is not implemented yet.
- Cloud must not host AI runtime, provider integrations, prompts, or fixture RCA behavior.
- The active incident contract exposes only list, get and resolve. Agent contains no HTTP AI analyzer, fallback RCA, provider/model metadata, AI network call, incident-owned mutation executor, analyze RPC or approve RPC. Historical RCA/mitigation columns are storage-only compatibility data.
- Never send raw logs, raw metrics streams, full manifests with secrets, env secrets, or user source code to AI.
- Future AI suggestions remain advisory and cannot authorize runtime actions.
- Future AI-origin mutations require human approval outside the AI/MCP channel;
  MCP must expose neither execute nor approval-grant tools.
- Agent deterministic policy, risk classification, preflight, typed executor,
  post-check and audit remain authoritative.
- Opsi does not currently render or manage Ingress, Gateway, domains or TLS;
  managed Traefik exposure is later roadmap work.

## Reliability

- Infra operations must be idempotent.
- Deploy rollout timeout target is 10 minutes with deploy-time rollback on failure.
- Agent SQLite stores must use WAL.
- Offline mode must keep local collection/storage running.
- Long-running operations should stream progress.

## Testing

- Business logic should have focused unit tests; protocol/security paths need negative tests.
- Do not depend on real Cloud when local fake/server is practical.
- Run tests inside module dirs:
  - `agent/`: ` go test ./...`
  - `cli/`: ` go test ./...`
  - `cloud/`: ` go test ./...`
  - `contracts/go/`: ` go test ./...`

## Source And Release Hygiene

- Local runtime configs, environment files, private keys, runtime certificate directories, databases, logs, and generated output must not be tracked.
- Source archives must use the Git-aware candidate set owned by `scripts/source-package.sh`; never archive the working-directory `.` directly.
- Archive and release checks must reject local config paths, private-key markers, runtime state, generated output, traversal paths, and escaping symlinks.
- Release output must be recreated from an empty `release/` directory before approved binaries, docs, and sanitized example configs are copied.
- Deleting current-tree credentials does not clean Git history; rotation and any approved history rewrite are separate owner actions.
## Development Stage

* The codebase is currently in active development.
* Backward compatibility is not required unless explicitly stated.
* It is acceptable to delete, rewrite, rename, move, or restructure existing code when doing so creates a cleaner production-ready architecture.
* Do not preserve bad abstractions, mock-only flows, duplicated logic, monolithic UI files, or CLI-first behavior just because they already exist.
* Prefer clean replacement over patching around broken design.
* If an existing implementation conflicts with these hard rules or roadmap v3, replace it.
* Migrations may be destructive during development unless a task explicitly requires preserving existing user data.
* Generated files, prototypes, placeholder screens, fake success states, and temporary scaffolding may be removed freely.
* Keep changes intentional: large rewrites must still preserve the intended product direction, contracts, and security rules.

DO NOT USE RTK IN ANY THINGS
## Repair Enforcement

- For current repair work, also load `.agents/rules_enforced.md` and the relevant
  roadmap v3 phase document.
- Architecture drift fixes outrank new feature work until P0 gates pass.
