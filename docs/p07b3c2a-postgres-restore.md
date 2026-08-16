# P07B3C2A PostgreSQL logical restore

P07B3C2A restores a succeeded Opsi `postgres_logical` custom archive into a
different, Ready PostgreSQL Resource created by the normal Resource and
Topology authorities. Restore is deliberately logical: it never adopts a
retained PVC, reuses a source PV, overwrites a running database, changes an
Application binding, or accepts a user-supplied archive.

The Cloud authority persists a read-only restore review and a durable Restore
record. The Agent verifies the immutable object size, SHA-256, Backup object
identity, PostgreSQL 18.6 `pg_restore --list`, target database identity, and
the factual pristine baseline before invoking:

```text
pg_restore --single-transaction --no-owner --no-privileges
```

The target management credential is read from the owned PostgreSQL Secret by
the pod command. It is not stored in Restore, ManagedResourceSpec, browser
responses, or audit events. Success is recorded only after authenticated SQL
verification; a failed transaction must prove the target remains pristine or
the authority reports `RESTORE_TARGET_STATE_UNKNOWN` and requires recreation.
