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
```

Generate local development certificates first if TLS paths are enabled:

```bash
 ../scripts/dev-certs.sh ./certs
```

For local deployment smoke tests without containerd/K3s, keep `deployment.dry_run: true` in `config.example.yaml`. Production single-node K3s deployments use `git`, `nerdctl --namespace k8s.io build`, `kubectl apply`, `kubectl set image`, and `kubectl rollout status/undo`. Docker remains available with `deployment.builder_mode: docker` for compatibility or registry-oriented flows.

## Systemd Runtime

Production Agent is intended to run as a native systemd service, not as a Docker container. A unit template is available at `packaging/systemd/opsi-agent.service`. Expected layout:

```text
/opt/opsi/agent/releases/<version>/opsi-agent
/opt/opsi/agent/current -> /opt/opsi/agent/releases/<version>
/etc/opsi/agent.yaml
/var/lib/opsi/
```

Rollback is a symlink switch back to a previous release followed by `systemctl restart opsi-agent`.

## Phase 2 Deployment

Agent exposes `opsi.agent.v1.DeploymentService.Deploy` over gRPC. The engine resolves missing CLI request fields from `deployment:` config, requires `project_id` + `service_id` + `service_name`, upserts service metadata in SQLite table `services`, records deployments in SQLite table `deployments` using WAL mode, builds under `/tmp/opsi-builds/{project_id}/{deploy_id}/`, and removes the build directory after success or failure.

Progress phases are `queued`, `cloning`, `building`, `applying`, `watching`, `success`, `rollback`, and `failed`. Progress events include project/service scope. Only deploy-time rollout failures before readiness passes are rollback-safe; those call `kubectl rollout undo deployment/{service_name}` and store `rollback_safe` plus `rollback_reason`.

## Phase 3 Telemetry

Agent migrates SQLite tables `metrics`, `logs`, `incidents`, and `audit_log` alongside Phase 2 `services`/`deployments`, with WAL mode enabled. When `telemetry.enabled` is true, the runtime collector writes node/process fallback metrics every `telemetry.interval` and applies raw retention for metrics/logs. `opsi.agent.v1.TelemetryService.Sync` returns project-scoped zstd chunks with delta-encoded metric/log payloads.
