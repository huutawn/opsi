# Opsi Mandatory Production-Ready Rules

Load this file for every implementation, review, refactor, or planning task until all P0 production gates are green.

## Prime rule

Do not claim production-ready unless `docs/production_ready/09_PRODUCTION_ACCEPTANCE_GATES.md` has all P0 gates green with code, tests, docs, and manual/e2e evidence.

## Mandatory boundaries

1. Browser production workflows must call `/api/local/...`; no direct Cloud runtime calls.
2. Browser must never receive or store long-lived PATs, private keys, kubeconfig, app secrets, OTP seeds, or raw runtime credentials.
3. Cloud must not own runtime execution and must not store raw logs, raw metrics, app secret values, kubeconfig, Docker layers, source code, or long-lived runtime payloads.
4. Agent owns deployment execution, K3s operations, runtime secrets, telemetry, incidents, local audit, and runtime sync buffer.
5. CLI local backend owns OS keychain PAT access and local session facade.

## Mandatory implementation rules

1. No fake success. Return typed unsupported/blocked errors when capability is missing.
2. No metadata-only runtime operations in production. Drain/remove/deploy/rotate/mitigate must execute through Agent or be blocked.
3. All mutating operations require RBAC, project scope, audit, request ID, and idempotency key where retryable.
4. Deployment jobs must carry versioned `DeploymentIntent` and Agent must reject unsafe/unknown intent.
5. Unsupported image-source deploy must be rejected before queueing unless implemented end-to-end.
6. Long-running operations must be restart-safe or explicitly recoverable.
7. Production Cloud must not use in-memory store for relay, audit, bootstrap, idempotency, or registry source of truth.
8. Secret values must never be passed through process command args.
9. Deployment source paths must be validated against path traversal and symlink escape.
10. AI output is advisory; Agent executes only typed allowlist actions after explicit user approval and audit.

## Mandatory documentation rules

1. `docs/opsi_srs.md` is the canonical current SRS v4 production-ready contract.
2. Legacy SRS/plans must be archived and labeled historical.
3. `docs/current_state.md` must describe implemented behavior only.
4. `docs/status_matrix.md` must use only: `DONE`, `PARTIAL`, `CONTRACT_ONLY`, `DOC_ONLY`, `NOT_STARTED`, `FAILED_OR_REGRESSED`, `BLOCKED`, `UNPROVEN`, `MANUAL_GATED`.
5. Any boundary change requires ADR.
6. A feature cannot be marked `DONE` without code, tests, config, verification command, and docs evidence.

## Mandatory review rejection checklist

Reject the change if any answer is yes:

- Does browser code call Cloud directly for production runtime workflow?
- Does Cloud receive/store raw logs, raw metrics, secrets, kubeconfig, source code, or Docker layers?
- Does UI show success without backend execution?
- Can a user queue an unsupported deployment?
- Can path traversal escape repo checkout during deploy?
- Can a secret appear in command args, logs, errors, audit, support summary, or AI payload?
- Can a user access another org/project resource?
- Can Cloud restart lose a deployment/bootstrap job?
- Did behavior change without updating current state/status matrix?
- Is production mode allowed with debug UI, dev OTP echo, weak secrets, unsigned Agent requests, or in-memory store?

## Required files for production work

Read these before changing production behavior:

- `.agents/rules.md`
- `.agents/production_ready_rules.md`
- `docs/production_ready/00_MASTER_PRODUCTION_READY_PLAN.md`
- `docs/production_ready/09_PRODUCTION_ACCEPTANCE_GATES.md`
- relevant plan under `docs/production_ready/`
- `docs/current_state.md`
- `docs/status_matrix.md`

## Required gates before completion

At minimum run or explain why you cannot run:

```bash
make clean
make verify
make test
make build
rg -n "placeholder|mock|fixture|debug-only|not implemented|unsupported|TODO|FIXME" docs .agents agent cli cloud contracts
rg -n "CloudRegistryClient|NEXT_PUBLIC_CLOUD|cloudURL|localhost:9800|127\.0\.0\.1:9800" cli/ui
rg -n "--from-literal|private key|kubeconfig|TOTP secret|OTP code|Gemini API key|OpenAI API key|DATABASE_URL" agent cli cloud contracts docs .agents
```

Every allowed match must be explained in the final change summary.
