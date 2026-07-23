# Opsi

Opsi is a local-first operations control-plane prototype. Current implementation
truth lives in `docs/current_state.md`; target requirements live in
`docs/opsi_srs.md`; capability evidence lives in `docs/status_matrix.md`; the
single canonical active roadmap is `docs/opsi_roadmap_v5_production.md`. Trusted artifact
delivery is defined by
`docs/architecture_decisions/ADR-004-trusted-artifact-cd.md`.

## Current boundary

- Cloud has no AI runtime or AI provider integration.
- Agent has no AI analyzer, fallback RCA, or RCA-backed execution.
- Active incidents support factual list/get/resolve only.
- `IncidentEvidence v1`, Safe ActionPlane, and the CLI-side MCP bridge are not
  implemented.
- GitHub App user authorization is implemented with fixed GitHub endpoints,
  PKCE S256, one-time state, and a prelinked numeric GitHub user ID. GitHub App
  installation authentication now loads an RSA private key from a read-only
  file, signs RS256 App JWTs, and caches installation tokens in memory. The
  separate `/v1/webhooks/github-app` endpoint verifies the App-wide secret and
  atomically persists typed installation/repository events and delivery IDs in
  PostgreSQL. Numeric repository IDs are claimed by one Opsi project and may
  bind multiple service keys within that project. `opsi init` now matches a
  safe local GitHub origin against Cloud metadata, claims the numeric repository
  ID, creates the P09 service binding, and atomically writes a secret-free
  `.opsi/opsi-cd.yaml` plus a manual bootstrap-only workflow. GitHub Actions
  OIDC admission, accepted `BuildRecord` storage, immutable digest deployment,
  `TopologyPlan`, `DeploymentPolicy`, routing, and Opsi-owned K3s reconciliation
  are implemented. PR-preview acceptance remains future R5-012 work.
- Opsi renders its owned Deployment, ClusterIP Service, and Traefik exposure
  resources. DNS, certificate provisioning, and public endpoint acceptance are
  not complete.
- P01 code is complete. Its clean control-plane VPS checkpoint is
  `DEFERRED / UNPROVEN` because no clean Ubuntu VPS was available.
- Bootstrap sessions persist a durable per-step checkpoint. New workers build
  `first-server-v2`; unfinished `first-server-v1` checkpoints fail closed and
  require a new bootstrap session. Verified K3s/Agent installation and Agent
  registration replay are idempotent for at-least-once step execution.
- Production readiness and complete real VPS/GitHub evidence remain unproven.
- GitHub user access tokens are used only during login or installation-claim
  callbacks and are not persisted or returned to the CLI. Installation claims
  compare the numeric GitHub `/user` identity with the prelinked Opsi identity,
  then require the requested installation to appear in `/user/installations`.
  Organization visibility proves token access for this MVP, not GitHub
  organization-owner status. Pending OAuth state and local grants remain in
  memory and are lost when Cloud restarts. Installation tokens remain in
  memory, while webhook delivery deduplication is durable in PostgreSQL and the
  P08 in-memory replay layer remains enabled. The remaining GitHub App live
  negatives are tracked as operator-required evidence in the status matrix.

The roadmap target is user-owned AI through a future local CLI MCP bridge. AI
will read redacted evidence and propose typed actions; deterministic Agent policy
and a separate human approval channel remain authoritative. MCP will not expose
execute or approve tools.

The production delivery path is separate from that AI boundary:

```text
GitHub Actions OIDC
-> accepted BuildRecord
-> immutable OCI digest
-> TopologyPlan + DeploymentPolicy + routing
-> durable DeploymentJob/RolloutIntent
-> Agent PollJob
-> ProductionAdapter/ReconcileRollout
-> Opsi-owned K3s resources
-> factual readiness/known-good rollback
```

Git commit SHA remains provenance and source identity, not the runtime artifact.
The Agent Git clone/build and arbitrary manifest execution paths are retired;
there is one executable delivery path from accepted BuildRecord to immutable
runtime state.

## Build and test

Supported toolchain:

- Go `1.26.4` with `GOTOOLCHAIN=local`;
- Node `24.16.0`;
- npm `11.17.0`;
- UI dependencies restored from `cli/ui/package-lock.json`.

Required clean-checkout commands:

```bash
make verify
make test
make build
make clean
make package-source
```

`make verify` checks toolchain, source hygiene, Go vet/tests for each module, and
the UI restore/build/lint path. No optional command wrapper is required.

Module test commands:

```bash
cd contracts/go && GOTOOLCHAIN=local go test ./...
cd agent && GOTOOLCHAIN=local go test ./...
cd cli && GOTOOLCHAIN=local go test ./cmd/... ./internal/...
cd cloud && GOTOOLCHAIN=local go test ./...
```

## Clean VPS/K3s proof

```bash
make verify-e2e-k3s-preflight
make verify-e2e-k3s
```

The active incident segment checks list, detail, resolve, and resolve audit only.
This is an operator-run local workflow requiring `OPSI_E2E_SSH_KEY_PATH` and an
exact `OPSI_E2E_VPS_HOST_KEY_SHA256`; no GitHub-hosted K3s workflow exists. The
command path exists, but no committed real-infrastructure pass artifact
currently proves the complete scenario. See
`docs/runbooks/clean_vps_k3s_e2e.md`.

## Roadmap order

The implemented checkpoint includes GitHub App identity/repository binding,
GitHub Actions OIDC, accepted BuildRecords, topology/policy routing, immutable
DeploymentJobs, Agent polling, and factual K3s readiness/rollback. R5-011 stays
`PARTIAL`; its live R5-011.4 endpoint gate is `MANUAL_GATED`. R5-012, MCP, AI,
DNS, TLS, and public endpoint acceptance are not done. Production readiness
must not be inferred.
