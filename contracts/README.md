# Opsi Contracts

Shared API contracts live here. This layer must not contain business logic.

`contracts/agent/v1/status.proto` is the public Agent contract source for `StatusService` and `TelemetryService`. Deployment execution is delivered through the Cloud Agent PollJob transport as an immutable `deploymentv1.AgentCommand`; there is no public direct-deploy RPC. `contracts/go` contains a small hand-written Go gRPC binding because `protoc`/`buf` is not required by the current workspace.

`contracts/cloud/v1/webhook_relay.md` documents the historical relay schema and the retained GitHub App/PollJob transport boundary. Generic GitHub push relay execution has been retired; relay tables remain only for restore/read compatibility.

## Build/Test

```bash
 go test ./...
```
