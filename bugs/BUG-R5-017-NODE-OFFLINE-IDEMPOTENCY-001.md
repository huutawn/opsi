# BUG-R5-017-NODE-OFFLINE-IDEMPOTENCY-001

## Live reproduction

`POST /api/projects/{project_id}/nodes/{node_id}/offline` accepted an exact
replay with the same payload and idempotency key, but advanced `updated_at` and
appended a second success audit. The first request produced
`aud-894bba993e969cec`; the replay produced `aud-e3a3219b47f1cb1c`.
Existing audit history remains append-only and was not changed by this source
correction.

## Root cause

The handler validated that `Idempotency-Key` existed but discarded its value.
`registry.API.MarkNodeOffline` accepted no actor, key, request identity, or
replay result. Both registry implementations rewrote node and related state on
every call, and the handler appended `NODE_MARKED_OFFLINE` after every success
outside the PostgreSQL state transaction.

## Expected replay semantics

- Scope keys as `node-offline:v1:<project_id>` and bind each key to one node ID.
- Return the stored factual node on exact replay without updates or another
  audit.
- Reject reuse for another node with HTTP 409 and `IDEMPOTENCY_CONFLICT`.
- Treat a new key against the exact retired state as a no-op while durably
  binding the new key.
- Commit PostgreSQL state changes, Agent revocation, runtime/project changes,
  idempotency binding, and the single success audit in one transaction.
- Use the authenticated principal as the audit actor and retain bounded request
  correlation in redacted audit metadata.

## Regression coverage

- In-memory exact replay, timestamp stability, target conflict, invalid keys,
  new-key state no-op, audit identity, and concurrent replay.
- HTTP missing/invalid keys, authenticated actor, exact response replay,
  target-bound conflict, concurrent replay, and one success audit per factual
  retirement.
- Disposable PostgreSQL 16 exact replay after service reconstruction,
  timestamp stability, target conflict, new-key state no-op, durable binding,
  concurrent replay, and one transactional success audit.

## Fixed revision

This source correction is the commit named
`fix(registry): make node retirement replay idempotent`.
Runtime publication and live R5-017 acceptance remain pending.
