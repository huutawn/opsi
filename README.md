<div align="center">
  <h1>Opsi</h1>
  <p><strong>Open-source, self-hosted deployment and operations platform for shipping immutable workloads to your own infrastructure.</strong></p>
</div>

Opsi combines a hosted or self-hosted control plane with an agent running on
your infrastructure. The CLI and local web interface keep credentials on the
operator machine while deployments remain policy-driven and auditable.

## Table of contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Interfaces](#interfaces)
- [Features](#features)
- [Status](#status)
- [Documentation](#documentation)
- [License](#license)

## Quick start

Install the latest published CLI prerelease for Linux or macOS (amd64 or
arm64). The installer verifies the release archive with SHA-256 and installs
the CLI plus local web UI to `~/.local/bin` by default.

```sh
curl -fsSL https://raw.githubusercontent.com/huutawn/opsi/main/scripts/install-cli.sh | sh
opsi start
```

The installer selects the latest published beta unless `OPSI_VERSION` pins an
exact release. Open `http://127.0.0.1:9780`; the Local UI uses the Hosted Beta
Cloud at `https://opsidev.site` by default and reports an unconfigured Agent as
not connected. Cloud operations require a PAT stored in the OS
keychain with `opsi login --pat-file PATH`. See the
[CLI installation runbook](docs/runbooks/install_cli.md) for release, PATH,
configuration, and self-hosting details.

## How it works

```text
GitHub Actions OIDC
        |
        v
Opsi Cloud: identity, policy, topology, deployment jobs
        |
        v
Opsi Agent: reconcile immutable OCI images on K3s
        |
        v
Readiness evidence, audit history, and known-good rollback
```

The Cloud control plane decides what may run and assigns durable work. The
Agent owns infrastructure reconciliation and reports factual results. Browser
requests go through the local CLI backend, so Cloud PATs and Agent TLS
credentials are not exposed to browser code.

## Interfaces

| Interface | Purpose |
|---|---|
| CLI | Configure projects, servers, policies, deployments, incidents, and approved actions. |
| Local Web UI | Browser console served by `opsi start` through credential-safe local APIs. |
| Cloud API | Identity, GitHub integration, topology, policy, build records, and deployment coordination. |
| Agent | Reconciles workloads on user-owned infrastructure and reports runtime evidence. |

## Features

- Self-hosted control plane and agents for user-owned infrastructure.
- GitHub App integration and GitHub Actions OIDC admission.
- Immutable OCI digest deployments with topology and deployment policies.
- Opsi-owned K3s reconciliation, readiness checks, and known-good rollback.
- Local CLI and web console with OS-keychain-backed Cloud credentials.
- Incident evidence, audit history, and explicit human-approved operational actions.

## Status

Opsi `v0.1.0-beta.2` is a public beta, not a production-ready release. The
currently supported scope is one VPS running single-node K3s, with Web/API
workloads, deployment, exposure, and known-good rollback.

- Hosted Beta Cloud: `https://opsidev.site`
- Known exclusions: DNS/TLS automation, multi-VPS topology, managed databases,
  and any production SLA.
- Current implementation truth: [docs/current_state.md](docs/current_state.md)
- Capability evidence: [docs/status_matrix.md](docs/status_matrix.md)
- Production roadmap: [docs/opsi_roadmap_v5_production.md](docs/opsi_roadmap_v5_production.md)

## Documentation

- [Architecture](docs/architecture.md)
- [CLI installation](docs/runbooks/install_cli.md)
- [Development control plane](docs/runbooks/dev_control_plane.md)

## License

Opsi is licensed under the [Apache License 2.0](LICENSE). Unless explicitly
stated otherwise, contributions submitted to this repository are licensed
under the same terms.
