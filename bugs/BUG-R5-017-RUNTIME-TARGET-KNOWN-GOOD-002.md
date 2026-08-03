# BUG-R5-017-RUNTIME-TARGET-KNOWN-GOOD-002

## Live reproduction

Retained Run 1 `r5-017-run1-20260802T142745Z` created deployment
`dep-255109f89b9efb64` for node `node-f94fc967441130ae` and Agent
`agent-7bbabeae900e7cd9`. Cloud supplied the historical `sha256:9f02ca...`
known-good while requesting `sha256:7dd66b...`. The target-scoped Agent rejected
the stale reference before mutation, and the deployment remains terminal
`failed` with `ROLLOUT_CONFLICT`.

The retained evidence is immutable and contains no authorization material in
this bug record.

## Root cause

Cloud selected previous known-good by project, environment, runtime, and
service only. It omitted node and Agent identity even though the rollout target
and Agent durable known-good authority include both.

The lookup also accepted any terminal result with a non-empty known-good ID.
A failed pre-mutation job could therefore echo an invalid previous reference,
become the newest matching row, and poison later deployment or exposure
rollouts. Adding only node and Agent filters would leave that failure mode.

Affected call sites:

- in-memory initial immutable deployment creation;
- PostgreSQL initial immutable deployment creation;
- in-memory exposure rollout creation;
- PostgreSQL exposure rollout creation.

## Baseline regression evidence

At revision `45ba2d09547294dae33f82d95704f52d2eed7eb2`:

- `TestMemoryDeploymentKnownGoodDoesNotCrossRuntimeTarget` failed because a new
  node and Agent inherited the old target known-good.
- `TestMemoryFailedPreMutationDoesNotPoisonKnownGood` failed because the next
  same-target deployment inherited the failed job's echoed reference.
- `TestPostgresDeploymentKnownGoodDoesNotCrossRuntimeTarget` reproduced the
  cross-target selection in disposable PostgreSQL 16.
- `TestPostgresFailedPreMutationDoesNotPoisonKnownGood` reproduced failed-row
  poisoning in disposable PostgreSQL 16.

No test repaired deployment state with direct SQL before asserting behavior.

## Required correction

Each storage backend must keep one canonical lookup that requires the exact
project, environment, runtime, service, node, and Agent target; a non-preview
row; an immutable factual terminal result; terminal rollout state exactly
`succeeded` or `rolled_back`; and non-empty known-good ID, hash, and current
digest. Exact-target history must never fall back to runtime-wide history.

Historical deployment rows remain immutable and queryable. Agent conflict
validation, explicit rollback semantics, rollout IDs, intent hashing,
idempotency, leases, WAL behavior, and result validation remain unchanged.

## Delivery boundary

The source correction commit is named
`fix(deploy): scope known-good to exact runtime target`.

The fix is not deployed. Aligned Cloud, Bootstrap Worker, and Agent artifacts
must be republished from the corrected revision before a new live Run 1 with a
new Run ID. R5-017, release readiness, and production readiness remain
unclaimed.
