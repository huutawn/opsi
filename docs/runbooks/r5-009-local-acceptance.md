# R5-009 Local Acceptance

Status: `DONE / LOCAL_FUNCTIONAL_ACCEPTANCE_PASS`

Date: 2026-07-19

Starting revision: `9ca5c42adce7cc48f3245961205bcbf5d67a8922`

## Scope and safety

This acceptance used a disposable PostgreSQL container, a loopback Cloud
process, the built CLI, the loopback Local API/UI, and headless Chrome. It did
not contact, SSH to, reboot, reset, bootstrap, or mutate the Agent VPS at
`52.77.226.123`. It created no workload, `DeploymentJob`, Agent command, DNS,
route, certificate, port mapping, MCP capability, or AI authority.

The temporary PAT was generated into a mode-0600 file by the existing admin
bootstrap path and loaded through stdin into OS Secret Service; it was never
placed in argv, browser storage, Cloud responses, or committed evidence.
Acceptance cleanup cleared the fixture PAT and stopped PostgreSQL, so that token
is no longer usable. Linux Secret Service resolved the pre-existing Opsi
`default-pat` item rather than an isolated collection, so the prior local CLI
PAT was replaced and could not be recovered from its Cloud hash. The operator
must run `opsi login` again for the real environment after this acceptance.

## Phase 0 findings

- Factual runtime state already exists in project-owned environment, runtime,
  node, and Agent rows. Heartbeat inventory provides node status, CPU cores,
  memory MiB, K3s state, Agent status/capabilities, and server-recorded
  `last_seen_at` timestamps.
- The registry did not provide a separate allocatable/reserved capacity record.
  R5-009 therefore treats positive heartbeat CPU/memory as
  `agent_observed`, adds bounded server-configured reserved headroom, and treats
  missing values as `unknown`. Optional capacity declarations are separate
  audited `operator_declared` revisions and never masquerade as Agent facts.
- GitHub OIDC admission decides whether a workflow may submit a BuildRecord.
  DeploymentPolicy decides whether an already accepted BuildRecord may route to
  the TopologyPlan runtime. ADR-005 prohibits either authority from replacing
  or OR-combining with the other.

## Fixture

The disposable fixture contained:

- services `api` and `worker` bound to repository ID `1304594095`;
- accepted R5-008-style BuildRecords for both services;
- a fresh observed-capacity runtime with one deploy Agent;
- an unknown-capacity runtime with one deploy Agent;
- a stale runtime, a zero-Agent runtime, and a two-Agent runtime;
- a foreign-project runtime;
- an audited unknown-capacity policy override.

The fixture project and credentials were disposable local identities and were
not the live staging project or Agent node.

## Positive evidence

The real CLI called the real loopback Cloud process backed by PostgreSQL:

1. `topology plan -> validate -> diff -> apply -> get` passed for both services.
2. Policy create/apply/list/get passed for exact api and worker BuildRecords.
3. Exact topology replay returned the same plan with `reused=true`.
4. Routing selected runtime `rt-fresh-r5009`, node `node-fresh-r5009`, and
   Agent `agent-fresh-r5009` for both services.
5. CLI and Local API returned identical topology plan/state hashes and policy
   policy/state hashes.
6. The built Local UI loaded through the loopback server in headless Chrome,
   selected the existing repository/service/BuildRecord/environment/runtime,
   produced deterministic validation and diff hashes, accepted explicit
   `APPLY`, and rendered topology/policy revision and audit results.

The browser-applied results were topology revision 4 with plan hash
`d1d399e8f90a410084ec4a47e0eef50d86e3c4a7a321641a8cb16fc71c1e6cc6`
and policy revision 4 with policy hash
`88db7fa6e3312f15ac5a3ff0c67874d0884194fc04c8a35dd6ae9b83aaf332e7`.
Subsequent CLI and Local API reads returned identical hashes and state hashes.

## Durability and concurrency

- Two simultaneous topology applies with the same expected revision/state hash
  produced one success and one typed `TOPOLOGY_STATE_CONFLICT`; the head moved
  by exactly one revision.
- Reusing a topology idempotency key with a different valid payload returned
  typed `IDEMPOTENCY_CONFLICT` HTTP 409.
- Before and after restarting the disposable PostgreSQL container, the fixture
  retained the same counts for topology revisions, policy revisions, policy
  heads, and idempotency records. Cloud health recovered without rebuilding
  state.
- Direct update/delete attempts against immutable topology/policy revision
  tables are rejected by migration triggers in the PostgreSQL integration test.

## Negative matrix

| Case | Result |
|---|---|
| stale node/Agent heartbeat | `TOPOLOGY_HEARTBEAT_STALE` |
| unknown capacity without policy | `TOPOLOGY_CAPACITY_UNKNOWN` |
| unknown capacity with scoped active policy | valid, explicit override shown |
| requested resources exceed headroom-adjusted capacity | `TOPOLOGY_CAPACITY_EXCEEDED` |
| foreign-project runtime ID | `TOPOLOGY_RUNTIME_NOT_FOUND`, no foreign state |
| zero deploy Agent | `TOPOLOGY_AGENT_MISSING` |
| two eligible deploy Agents | `TOPOLOGY_AGENT_AMBIGUOUS` |
| multiple exact policies for the same topology runtime | `ROUTING_POLICY_AMBIGUOUS` |
| exact policies for disjoint runtimes | topology runtime selects one policy deterministically |
| wrong repository/service/ref/environment/OCI/config/plan | routing fails closed |
| disabled matching policy | `ROUTING_POLICY_MISMATCH` |
| unknown JSON field or wildcard/expression policy value | request rejected |
| rationale asks to override validation | ignored; validation still fails |

## Verification commands

The final gate includes:

```text
contracts/go: go test ./...
cloud: go test ./...
cloud: go vet ./...
cloud: focused -race topology/deploymentpolicy/webhookrelay tests
cloud: PostgreSQL placement restart/concurrency/routing integration test
cli: go test ./...
cli: go vet ./...
cli/ui: npm run lint
cli/ui: topology and BuildRecord source-state tests
cli/ui: npm run build
repository: git diff --check, source hygiene, and secret-marker scan
```

R5-008 BuildRecord and OIDC suites are included in the full Cloud regression
run. R5-010 has not started.
