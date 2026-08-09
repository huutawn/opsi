# Opsi CLI

Networked mutation commands require an explicit `--config` snapshot. `opsi
start` is the first-run exception: it uses the Hosted Beta Cloud at
`https://opsidev.site` and leaves Agent connectivity unconfigured. Secret and
TOTP responses require `--output-file`; protected output is created as a new
`0600` file and stdout contains only a sanitized receipt.

CLI is the local command and presentation layer. It stores PAT values in the OS keychain, talks to Cloud and Agent through explicit clients, serves the built Local Web UI, streams deployment progress, and can consume telemetry sync chunks.

The installed prerelease archive places `opsi-ui` beside the binary, so
`opsi start` serves the UI without a repository checkout. Browser code calls
only relative `/api/local/...` routes and never receives the PAT or Agent TLS
credentials.

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
OPSI_UI_DIR="$PWD/ui/out" go run ./cmd/opsi start --addr 127.0.0.1:9780
go run ./cmd/opsi sync --config config.example.yaml --project-id dev-project --since-unix 0
```

The deployment UI uses accepted immutable BuildRecords and the canonical
deployment preview/apply/event/rollback APIs; the retired Git/source-build
inputs are not present.

`opsi sync` prints newline-delimited JSON telemetry chunks from Agent. Chunk payloads are base64 in JSON because the underlying contract field is bytes; the payload content is zstd-compressed delta records. When `--since-unix` is omitted, sync resumes from the per-project timestamp in the sync state file. Configure it with `sync_state_path` or `--state-path`; use `--no-state` to disable state reads/writes.

`opsi incident evidence --config <selected-config> --project-id <project> --incident-id <incident> [--json]` reads the bounded factual evidence body from the authenticated, TLS-pinned Agent. It never falls back to an implicit Agent address. The Local API exposes the same body at `/api/local/projects/:project_id/incidents/:incident_id/evidence` with `Cache-Control: no-store`; UI rendering and browser acceptance are deferred to R5-017.

## Repository bootstrap

`opsi init --project-id <project> --service-id <service> --service-key <key>`
detects the local GitHub.com `origin`, matches it case-insensitively to Cloud
inventory, but always claims and binds by the numeric Cloud `repository_id`.
If the repository is not visible, `--installation-id` starts the P09
installation-claim browser flow; Cloud still verifies the GitHub user and
installation through OAuth. The CLI reads its PAT only from the OS keychain.

## Human-approved actions

`opsi action device register|list|revoke` manages Cloud public device identity;
the Ed25519 private key remains in Linux Secret Service or macOS Keychain with
no plaintext fallback. Runtime actions use three separate process invocations:
`opsi action preflight`, `opsi action approve <challenge-id>`, and
`opsi action execute <challenge-id>`. Approval requires an interactive TTY and
the exact displayed phrase. Pending grants remain in the OS secure store and
are removed after a terminal Agent result. No browser approve/execute API,
MCP, AI approval, `--yes`, or automatic approval path is implemented.

The command writes `.opsi/opsi-cd.yaml` with build/deployment intent only and
`.github/workflows/opsi-cd.yaml` as a manual bootstrap status workflow. Neither
file contains Cloud infrastructure identity or secrets. Existing different
content is never overwritten unless both `--force` and `--yes` are present;
`--dry-run` prints a secret-free JSON plan without mutation or file writes.
P10 does not implement Actions OIDC, BuildRecord, image build/push, Agent
deployment, or real CD.
