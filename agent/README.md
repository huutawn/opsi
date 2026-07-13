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

Production Agent is intended to run as a native systemd service, not as a Docker container. A unit template is available at `packaging/systemd/opsi-agent.service`. The template currently describes this versioned layout:

```text
/opt/opsi/agent/releases/<version>/opsi-agent
/opt/opsi/agent/current -> /opt/opsi/agent/releases/<version>
/etc/opsi/agent.yaml
/var/lib/opsi/
```

Rollback is a symlink switch back to a previous release followed by `systemctl restart opsi-agent`.

Bootstrap installation commands do not yet use the same layout. P05 owns the
canonical versioned systemd install layout and upgrade/rollback integration;
P03 does not change Bootstrap Worker or the unit template.

## Phase 2 Deployment

Agent exposes `opsi.agent.v1.DeploymentService.Deploy` over gRPC. The engine resolves missing CLI request fields from `deployment:` config, requires `project_id` + `service_id` + `service_name`, upserts service metadata in SQLite table `services`, records deployments in SQLite table `deployments` using WAL mode, builds under `/tmp/opsi-builds/{project_id}/{deploy_id}/`, and removes the build directory after success or failure.

Progress phases are `queued`, `cloning`, `building`, `applying`, `watching`, `success`, `rollback`, and `failed`. Progress events include project/service scope. Only deploy-time rollout failures before readiness passes are rollback-safe; those call `kubectl rollout undo deployment/{service_name}` and store `rollback_safe` plus `rollback_reason`.

## Phase 3 Telemetry

Agent migrates SQLite tables `metrics`, `logs`, `incidents`, and `audit_log` alongside Phase 2 `services`/`deployments`, with WAL mode enabled. When `telemetry.enabled` is true, the runtime collector writes node/process fallback metrics every `telemetry.interval` and applies raw retention for metrics/logs. `opsi.agent.v1.TelemetryService.Sync` returns project-scoped zstd chunks with delta-encoded metric/log payloads.
