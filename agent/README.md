# Opsi Agent

Agent is the execution brain that runs on the user's VPS. It provides config loading, structured logging, health/status endpoints, gRPC transport, TLS/mTLS wiring, deployment execution, SQLite deployment records, local telemetry storage, and sync chunks.

## Build

```bash
go build ./cmd/opsi-agent
```

## Test

```bash
go test ./...
go test -cover ./...
```

## Run locally

```bash
go run ./cmd/opsi-agent --config config.example.yaml
go run ./cmd/opsi-agent --config config.example.yaml --check
go run ./cmd/opsi-agent --version
```

Generate local development certificates first if TLS paths are enabled:

```bash
 ../scripts/dev-certs.sh ./certs
```

For local deployment smoke tests without containerd/K3s, keep `deployment.dry_run: true` in `config.example.yaml`. Production single-node K3s deployments use `git`, `nerdctl --namespace k8s.io build`, `kubectl apply`, `kubectl set image`, and `kubectl rollout status/undo`. Docker remains available with `deployment.builder_mode: docker` for compatibility or registry-oriented flows.

## Linux release artifact

From the repository root:

```bash
make agent-release
make verify-agent-release
```

`make agent-release` writes a direct Linux amd64 executable plus deterministic
metadata to:

```text
dist/agent/opsi-agent-linux-amd64
dist/agent/checksums.txt
dist/agent/release.json
```

The release artifact is built with explicit version and full commit metadata.
It has not been published or hosted over HTTPS, and Bootstrap Worker has not
been tested against this artifact on a VPS.

## Systemd Runtime

Production Agent is intended to run as a native systemd service, not as a Docker container. `packaging/systemd/opsi-agent.service` is the canonical unit and a Bootstrap Worker parity test prevents its embedded install asset from diverging. The checksum-addressed layout is:

```text
/opt/opsi/agent/releases/<agent-sha256>/opsi-agent
/opt/opsi/agent/current -> releases/<agent-sha256>
/opt/opsi/agent/previous -> releases/<previous-sha256>  # optional
/etc/opsi/agent.yaml
/var/lib/opsi/
```

`install_agent` only verifies and stages a release. `register_agent` atomically
updates `previous` and `current`, installs the unit/config atomically, restarts
the service, checks the local health endpoint, and restores the previous release
if the new one is unhealthy. This behavior has unit/contract coverage but has
not been proven on a clean target VPS; that evidence belongs to P06.

## Phase 2 Deployment

Agent exposes `opsi.agent.v1.DeploymentService.Deploy` over gRPC. The engine resolves missing CLI request fields from `deployment:` config, requires `project_id` + `service_id` + `service_name`, upserts service metadata in SQLite table `services`, records deployments in SQLite table `deployments` using WAL mode, builds under `/tmp/opsi-builds/{project_id}/{deploy_id}/`, and removes the build directory after success or failure.

Progress phases are `queued`, `cloning`, `building`, `applying`, `watching`, `success`, `rollback`, and `failed`. Progress events include project/service scope. Only deploy-time rollout failures before readiness passes are rollback-safe; those call `kubectl rollout undo deployment/{service_name}` and store `rollback_safe` plus `rollback_reason`.

## Phase 3 Telemetry

Agent migrates SQLite tables `metrics`, `logs`, `incidents`, and `audit_log` alongside Phase 2 `services`/`deployments`, with WAL mode enabled. When `telemetry.enabled` is true, the runtime collector writes node/process fallback metrics every `telemetry.interval` and applies raw retention for metrics/logs. `opsi.agent.v1.TelemetryService.Sync` returns project-scoped zstd chunks with delta-encoded metric/log payloads.
