# R5-011.3 Local Control-Plane Acceptance

This runbook records the local-only evidence for the durable exposure/rollout
lifecycle. It does not prove a live Agent endpoint, DNS, certificate
provisioning, or production readiness.

## Canonical path

```text
CLI or Browser
  -> Local API (PAT stays in OS keychain)
  -> Cloud project-scoped exposure lifecycle
  -> existing DeploymentJob lease and AgentCommand
  -> Agent deploy.Engine ReconcileRollout/ReconcilePending
  -> SQLite/Kubernetes runtime truth
  -> bounded sanitized progress/result/events in Cloud
  -> same status/history in CLI, Local API, and UI
```

Cloud never calls Kubernetes. The append-only R5-011 migration extends the
R5-010 deployment tables; it does not create a second queue or deployment
engine. Agent reports contain hashes, bounded resource identities, readiness
evidence hashes, typed failures, digests, timestamps, and attempts only.

## Disposable PostgreSQL

Start a disposable PostgreSQL instance and run the focused durability gate:

```text
OPSI_TEST_DATABASE_URL=<disposable-loopback-postgres-dsn> make verify-postgres
```

The gate covers migration upgrade, restart persistence, exact replay,
concurrent one-winner apply, lease/result durability, and the pre-existing
R5-008/R5-009/R5-010 PostgreSQL regressions. Use a fresh disposable database
when fixed legacy fixture IDs are present.

## Lifecycle evidence

- Success: preview/diff/apply creates one durable job; the Agent command keeps
  the immutable project/environment/runtime/service, digest, workload hash,
  exposure hash, policy/routing authority, and known-good expectation. Events
  observe `prepared -> applying -> waiting -> succeeded`; the terminal result
  records the factual desired digest and readiness evidence.
- Automatic rollback: known-good A exists, desired B fails readiness, and the
  Agent reports `failed -> rolling_back -> rolled_back`. Cloud retains desired
  B while factual current/known-good metadata points to A; no duplicate job or
  resource is created.
- Explicit rollback: a succeeded rollout pins the expected current known-good
  B and restores the previous A through the same lease/command path. Replay is
  idempotent and a changed key/payload is rejected.
- Terminal failures: missing known-good ends in typed `NO_KNOWN_GOOD`; rollback
  apply/readiness failure ends in typed `rollback_failed` metadata.

## Negative and durability matrix

Focused tests cover target busy, hostname/path conflict, foreign ownership,
wrong project/service/runtime/Agent, intent/spec/exposure hash mismatch, stale
lease/progress/result, illegal transition, terminal overwrite, idempotency
conflict, concurrent apply, unauthorized/cross-project lookup, oversized or
unknown JSON, and PAT/lease/raw-manifest/secret marker leakage. PostgreSQL,
Local backend, and Agent/store restart fixtures read the same rollout/job/event
IDs and hashes after restart.

## UI boundary

UI source-state tests prove Browser requests are limited to `/api/local/...`,
including preview/apply/status/history/rollback. `npm run lint`, `npm run build`,
and the deployment source tests pass. A disposable headless Chrome fixture ran
the interactive preview -> apply -> succeeded -> explicit rollback ->
rolled_back flow, verified desired/current digests, and found empty browser
storage. Playwright is not installed; the interaction fixture uses Chrome DevTools
Protocol.

## Scope boundary

No VPS or SSH was used. No staging deployment, public endpoint, DNS, Cloudflare,
certificate, live Agent release, R5-012 workflow, MCP, or AI approval was
started. R5-011.4 remains the operator task for live Agent/public endpoint
acceptance.
