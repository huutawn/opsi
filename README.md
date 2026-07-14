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
  installation authentication, GitHub Actions OIDC, `BuildRecord`, digest
  deployment, `DeploymentPolicy`, and PR previews are not implemented.
- Opsi does not currently render or manage Ingress, Gateway, domains, or TLS.
- P01 code is complete. Its clean control-plane VPS checkpoint is
  `DEFERRED / UNPROVEN` because no clean Ubuntu VPS was available.
- Bootstrap sessions persist a durable per-step checkpoint. New workers build
  `first-server-v2`; unfinished `first-server-v1` checkpoints fail closed and
  require a new bootstrap session. Verified K3s/Agent installation and Agent
  registration replay are idempotent for at-least-once step execution.
- Production readiness and complete real VPS/GitHub evidence remain unproven.
- GitHub user access tokens are used only for the callback's `/user` request and
  are not persisted or returned to the CLI. Pending browser login state remains
  in memory and is lost when Cloud restarts. No real GitHub App login has been
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
tests and development Docker smoke pass. The full Cloud gate is still blocked
by the pre-existing OTP/PAT failure. P06 clean target VPS proof remains
`DEFERRED / UNPROVEN`. P07 GitHub App user authorization code and focused tests
are complete, while live GitHub verification remains `UNPROVEN`; P08
installation authentication and webhooks are next. Production readiness must
not be inferred.
