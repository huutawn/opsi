# Opsi

Opsi is a local-first operations control-plane prototype. Current implementation
truth lives in `docs/current_state.md`; target requirements live in
`docs/opsi_srs.md`; capability evidence lives in `docs/status_matrix.md`; the
single canonical active roadmap is `docs/opsi_roadmap_v4.md`. Trusted artifact
delivery is defined by
`docs/architecture_decisions/ADR-004-trusted-artifact-cd.md`.

## Current M0 boundary

- Cloud has no AI runtime or AI provider integration.
- Agent has no AI analyzer, fallback RCA, or RCA-backed execution.
- Active incidents support factual list/get/resolve only.
- `IncidentEvidence v1`, Safe ActionPlane, and the CLI-side MCP bridge are not
  implemented.
- Agent currently supports Git-source clone/build deployment and rejects
  image-source deployment before runtime execution.
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
  `.opsi/opsi-cd.yaml` plus a manual bootstrap-only workflow. GitHub Actions OIDC,
  `BuildRecord`, digest deployment, `DeploymentPolicy`, and PR previews are not
  implemented.
- Opsi does not currently render or manage Ingress, Gateway, domains, or TLS.
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
  P08 in-memory replay layer remains enabled. No real GitHub App flow has been
  tested yet.

The roadmap target is user-owned AI through a future local CLI MCP bridge. AI
will read redacted evidence and propose typed actions; deterministic Agent policy
and a separate human approval channel remain authoritative. MCP will not expose
execute or approve tools.

The production delivery target is separate from that AI boundary:

```text
GitHub Actions build/test
-> OCI registry
-> image@sha256:<digest>
-> Opsi Cloud BuildRecord and DeploymentPolicy
-> DeploymentJob
-> Agent deploys the immutable digest
```

Git commit SHA remains provenance and source identity, not the runtime artifact.
The existing Git clone/build path remains a legacy/manual development path
during migration; the trusted OCI artifact flow is target architecture, not a
current capability.

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
The command path exists, but no committed real-infrastructure pass artifact
currently proves the complete scenario. See
`docs/runbooks/clean_vps_k3s_e2e.md`.

## Roadmap order

P01 development control-plane code is complete, while checkpoint `CP-VPS-1`
remains `DEFERRED / UNPROVEN`. P02 documentation is retained. P03 Agent
executable and deterministic Linux release artifact code is complete. P04
durable BootstrapJob checkpoint/resume behavior is implemented and its focused
unit, race, PostgreSQL, and migration-upgrade tests pass. P05 hardened pinned
K3s installation, SSH host-key verification, checksum-addressed Agent releases,
canonical systemd activation/rollback, and registration replay. Focused/race
tests and development Docker smoke pass. P06 clean target VPS proof remains
`DEFERRED / UNPROVEN`. P07 GitHub App user authorization and P08 installation
authentication/webhook code are complete, while live GitHub verification
remains `UNPROVEN`. P09 durable installation/repository inventory, secure
installation claim, single-project repository ownership, and monorepo service
bindings are implemented with local/PostgreSQL evidence; live P09 GitHub proof
remains `UNPROVEN`. P10 repository bootstrap code and local tests are complete;
its real GitHub App checkpoint remains `UNPROVEN`. P11 remains blocked until
that checkpoint is run or explicitly recorded as deferred.
Production readiness must not be inferred.
