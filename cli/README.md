# Opsi CLI

CLI is the local command and presentation layer. It stores PAT values in the OS keychain, talks to Cloud and Agent through explicit clients, serves the built Local Web UI, streams deployment progress, and can consume telemetry sync chunks.

## Build

```bash
go build ./cmd/opsi
```

## Test

```bash
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go test -cover ./...
```

## Run locally

```bash
go run ./cmd/opsi status --config config.example.yaml
go run ./cmd/opsi start --addr 127.0.0.1:9780
go run ./cmd/opsi deploy --config config.example.yaml --project-id dev-project --service-id example-app --service-name example-app --repo-url https://github.com/example/app.git --git-sha abcdef1234567890 --manifest-path k8s/deployment.yaml
go run ./cmd/opsi sync --config config.example.yaml --project-id dev-project --since-unix 0
```

`opsi deploy` prints newline-delimited JSON progress events from Agent. `--project-id`, `--service-id`, and `--service-name` define the Project-first scope; fields can be omitted when Agent `deployment:` config provides defaults. `--service` remains an alias for `--service-name`.

`opsi sync` prints newline-delimited JSON telemetry chunks from Agent. Chunk payloads are base64 in JSON because the underlying contract field is bytes; the payload content is zstd-compressed delta records. When `--since-unix` is omitted, sync resumes from the per-project timestamp in the sync state file. Configure it with `sync_state_path` or `--state-path`; use `--no-state` to disable state reads/writes.

## Repository bootstrap

`opsi init --project-id <project> --service-id <service> --service-key <key>`
detects the local GitHub.com `origin`, matches it case-insensitively to Cloud
inventory, but always claims and binds by the numeric Cloud `repository_id`.
If the repository is not visible, `--installation-id` starts the P09
installation-claim browser flow; Cloud still verifies the GitHub user and
installation through OAuth. The CLI reads its PAT only from the OS keychain.

The command writes `.opsi/opsi-cd.yaml` with build/deployment intent only and
`.github/workflows/opsi-cd.yaml` as a manual bootstrap status workflow. Neither
file contains Cloud infrastructure identity or secrets. Existing different
content is never overwritten unless both `--force` and `--yes` are present;
`--dry-run` prints a secret-free JSON plan without mutation or file writes.
P10 does not implement Actions OIDC, BuildRecord, image build/push, Agent
deployment, or real CD.
