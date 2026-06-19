# Opsi CLI

CLI is the local command and presentation layer. It stores PAT values in the OS keychain, talks to Agent through the published contract, serves the Local Web UI placeholder, and streams deployment progress.

## Build

```bash
rtk go build ./cmd/opsi
```

## Test

```bash
rtk go test ./...
rtk go test -cover ./...
```

## Run locally

```bash
rtk go run ./cmd/opsi status --config config.example.yaml
rtk go run ./cmd/opsi start --addr 127.0.0.1:9780
rtk go run ./cmd/opsi deploy --config config.example.yaml --project-id dev-project --service-id example-app --service-name example-app --repo-url https://github.com/example/app.git --git-sha abcdef1234567890 --manifest-path k8s/deployment.yaml
```

`opsi deploy` prints newline-delimited JSON progress events from Agent. `--project-id`, `--service-id`, and `--service-name` define the Project-first scope; fields can be omitted when Agent `deployment:` config provides defaults. `--service` remains an alias for `--service-name`.
