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

Commit `7623266fdbebd96087ab525f6e38b6066570c259` fixed cross-target
inheritance and failed-job poisoning. Review before runtime republishing found
that exact-target history validation was still incomplete: memory could accept
a rollout-shaped row without rollout mode, canonical intent, or a valid
snapshot, while PostgreSQL could accept legacy JSON or normalized columns that
disagreed with the terminal result.

At revision `7623266fdbebd96087ab525f6e38b6066570c259`, test-first regressions
proved that memory selected an exact-target legacy row and disposable
PostgreSQL 16 selected an equivalent legacy row, a terminal-result/column
mismatch, and a canonical rollout intent whose Agent target differed from the
normalized row. The regressions failed on the historical selector behavior;
no test repaired state with direct SQL.

## Required correction

Each storage backend must keep one canonical lookup that requires the exact
project, environment, runtime, service, node, and Agent target; a non-preview
row; current job, snapshot, rollout-intent, and result schemas; rollout mode; a
canonical intent matching the normalized target and snapshot; an immutable
factual terminal result; terminal rollout state exactly `succeeded` or
`rolled_back`; matching normalized state, hashes, digests, and known-good
fields; and non-empty known-good ID, hash, and current digest. Exact-target
history must never fall back to runtime-wide history. Legacy, malformed, or
internally inconsistent history fails closed in memory and PostgreSQL through
the same Go candidate predicate.

Historical deployment rows remain immutable and queryable. Agent conflict
validation, explicit rollback semantics, rollout IDs, intent hashing,
idempotency, leases, WAL behavior, and result validation remain unchanged.

## Delivery boundary

The target history-validation correction commit is named
`fix(deploy): reject malformed known-good history`. It follows commit
`7623266fdbebd96087ab525f6e38b6066570c259`, which fixed cross-target
inheritance and failed-job poisoning.

The fix is not deployed. Aligned Cloud, Bootstrap Worker, and Agent artifacts
must be republished from the corrected revision before a new live Run 1 with a
new Run ID. R5-017, release readiness, and production readiness remain
unclaimed. No deployment record was modified, retried, rewritten, or migrated;
the previous failed Run 1 remains immutable.
