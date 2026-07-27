# BUG-R5-013-CLI-GAPS-001

Factual backend/API gaps found while building the manual capability matrix:

- There is no project-scoped member listing or membership/RBAC mutation API.
- There is no separate organization listing API; projects require a known
  organization ID.
- The Agent SecretService contract has no metadata/list RPC; only setup, create,
  reveal, and rotate are available.
- Runtime inventory is exposed through `GET /api/projects/{project_id}/topology/facts`,
  not a standalone runtime route.
- Agent secret create/reveal/rotate RPC requests have no idempotency-key field;
  the CLI protects output and does not invent replay semantics for those calls.
- Agent service mutation and incident resolve RPC requests likewise have no
  idempotency-key field.
- Cloud PAT rotate/revoke handlers accept the idempotency header but do not
  expose replay semantics in the handler contract.

These remain `BACKEND_GAP`; the CLI does not create fake successful commands.
