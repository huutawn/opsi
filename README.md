# Opsi

Local-first control plane prototype. Current implementation state lives in `docs/current_state.md`.

## Build And Test

Supported toolchain:

- Go `1.26.4`
- Node `24.16.0`
- npm `11.17.0`
- `GOTOOLCHAIN=local` in normal verification, so Go will not download a toolchain implicitly
- Node/npm for `cli/ui`; UI dependencies are restored from `cli/ui/package-lock.json`
- `` is optional for Make targets. If installed, Make auto-detects it and wraps commands; if absent, raw `go`, `npm`, `tar`, and shell commands run directly. To force raw commands: `make = verify`.

Required clean-checkout commands:

```bash
make verify
make test
make build
make clean
make package-source
```

`make verify` is canonical. It checks the pinned toolchain, source hygiene, Go vet/tests for `agent/`, `cli/`, `cloud/`, and `contracts/go/`, then runs `npm ci`, `npm run build`, and `npm run lint` in `cli/ui`.

CI runs the same clean path from `.github/workflows/ci.yml`:

```bash
make clean
make verify
make build
make package-source
```

Module test commands:

```bash
cd contracts/go && GOTOOLCHAIN=local go test ./...
cd agent && GOTOOLCHAIN=local go test ./...
cd cli && GOTOOLCHAIN=local go test ./cmd/... ./internal/...
cd cloud && GOTOOLCHAIN=local go test ./...
```

Offline behavior:

- Go verification uses `GOTOOLCHAIN=local`; dependencies must already be in the module cache or vendored by the environment.
- UI verification runs `npm ci`; it is reproducible from the lockfile but needs registry/cache access when packages are not already cached.
- `make package-source` excludes binaries, release artifacts, databases, UI output, caches, and coverage files, then validates the archive.
