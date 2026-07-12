# Opsi

Opsi is a local-first operations control-plane prototype. Current implementation
truth lives in `docs/current_state.md`; target requirements live in
`docs/opsi_srs.md`; capability evidence lives in `docs/status_matrix.md`.

## Current M0 boundary

- Cloud has no AI runtime or AI provider integration.
- Agent has no AI analyzer, fallback RCA, or RCA-backed execution.
- Active incidents support factual list/get/resolve only.
- `IncidentEvidence v1`, Safe ActionPlane, and the CLI-side MCP bridge are not
  implemented.
- Opsi does not currently render or manage Ingress, Gateway, domains, or TLS.
- Production readiness and a complete clean VPS/K3s pass remain unproven.

The roadmap target is user-owned AI through a future local CLI MCP bridge. AI
will read redacted evidence and propose typed actions; deterministic Agent policy
and a separate human approval channel remain authoritative. MCP will not expose
execute or approve tools.

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

M0 ends with V3-008 documentation truth. After review, Phase 2 starts at V3-009:
convert Bootstrap Worker from `RunOnce(session_id)` to a poll/lease daemon.
IncidentEvidence, Safe ActionPlane, CLI MCP, and production gates remain later
ordered work.
