# Opsi Agent

Agent is the execution brain that runs on the user's VPS. It provides config loading, structured logging, health/status endpoints, gRPC transport, TLS/mTLS wiring, deployment execution, SQLite deployment records, local telemetry storage, and sync chunks.

## Build

```bash
rtk go build ./cmd/opsi-agent
```

## Test

```bash
rtk go test ./...
rtk go test -cover ./...
```

## Run locally

```bash
rtk go run ./cmd/opsi-agent --config config.example.yaml
```

Generate local development certificates first if TLS paths are enabled:

```bash
rtk ../scripts/dev-certs.sh ./certs
```

For local deployment smoke tests without Docker/K3s, keep `deployment.dry_run: true` in `config.example.yaml`. Real deployments use `git`, `docker buildx` or `docker build`, `docker push` when a registry is configured, and `kubectl rollout status/undo`.

## Phase 2 Deployment

Agent exposes `opsi.agent.v1.DeploymentService.Deploy` over gRPC. The engine resolves missing CLI request fields from `deployment:` config, requires `project_id` + `service_id` + `service_name`, upserts service metadata in SQLite table `services`, records deployments in SQLite table `deployments` using WAL mode, builds under `/tmp/opsi-builds/{project_id}/{deploy_id}/`, and removes the build directory after success or failure.

Progress phases are `queued`, `cloning`, `building`, `applying`, `watching`, `success`, `rollback`, and `failed`. Progress events include project/service scope. Rollout failures call `kubectl rollout undo deployment/{service_name}` and store `rolled_back` or `failed_after_rollback`.

## Phase 3 Telemetry

Agent migrates SQLite tables `metrics`, `logs`, `incidents`, and `audit_log` alongside Phase 2 `services`/`deployments`, with WAL mode enabled. When `telemetry.enabled` is true, the runtime collector writes node/process fallback metrics every `telemetry.interval` and applies raw retention for metrics/logs. `opsi.agent.v1.TelemetryService.Sync` returns project-scoped zstd chunks with delta-encoded metric/log payloads.
