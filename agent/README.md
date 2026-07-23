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

`deployment.dry_run` controls only managed-service catalog application. It does
not bypass immutable `ProductionAdapter` deployment. Production deployments
consume only the immutable `AgentCommand` delivered by Cloud and reconcile
Opsi-owned K3s resources by digest.

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

## Immutable Deployment

The public Agent API has no direct deployment RPC. Cloud resolves the accepted
BuildRecord, topology, policy, routing, and durable job into an immutable
`AgentCommand`; the Agent polls that command, pulls its digest, reconciles
Opsi-owned K3s resources, reports readiness, and participates in rollback or
rollout reconciliation. Historical SQLite deployment columns remain readable
for restore compatibility but are not executable input paths.

The poll transport may retain the historical `/webhooks/next` route name, but
it carries only canonical deployment or node lifecycle jobs. It is not a
generic webhook relay, and the Agent has no Git/source or arbitrary-manifest
deployment path.

## Phase 3 Telemetry

Agent migrates SQLite tables `metrics`, `logs`, `incidents`, and `audit_log` alongside Phase 2 `services`/`deployments`, with WAL mode enabled. When `telemetry.enabled` is true, the runtime collector writes node/process fallback metrics every `telemetry.interval` and applies raw retention for metrics/logs. `opsi.agent.v1.TelemetryService.Sync` returns project-scoped zstd chunks with delta-encoded metric/log payloads.
