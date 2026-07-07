# Opsi Contracts

Shared API contracts live here. This layer must not contain business logic.

`contracts/agent/v1/status.proto` is the public Agent contract source for `StatusService`, `DeploymentService`, and `TelemetryService`. `contracts/go` contains a small hand-written Go gRPC binding because `protoc`/`buf` is not required by the current workspace. Later phases may replace the hand-written binding with generated code without changing the RPC shape.

`contracts/cloud/v1/webhook_relay.md` defines the Phase 2 Cloud webhook relay contract for GitHub webhook ingestion and Agent long-poll delivery. `cloud/` now has a local in-memory implementation of that contract for dev/test validation.

## Build/Test

```bash
 go test ./...
```
