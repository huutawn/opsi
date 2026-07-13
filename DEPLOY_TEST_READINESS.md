# Opsi Cloud Deployment Test Readiness Review

Reviewed: 2026-07-13

## Verdict

| Scope | Verdict | Conditions |
|---|---|---|
| Private control-plane smoke test | READY AFTER CONFIGURATION | Keep the development proxy bound to `127.0.0.1`, fill runtime secrets, build the images, and use an SSH tunnel from the operator machine. |
| Real remote VPS bootstrap test | CONDITIONALLY READY | In addition to the above, provide a target-reachable HTTPS `agent_cloud_url`, a real Agent binary URL and SHA-256, and valid SSH credentials. Pin/verify the target host key before treating the result as security evidence. |
| Internet-facing or production deployment | NOT READY | The committed Compose package is HTTP-only and development-only. Clean-VM evidence, full VPS E2E evidence, pinned K3s delivery, release provenance, backup/restore evidence, and production edge configuration remain required. |

The original archive was not safe to deploy as-is. It contained development TLS private keys and several runtime/authentication mismatches. This reviewed copy removes the private certificate directory and fixes the code/config defects listed below. Docker and the required Go 1.26.4 toolchain were not available in the review environment, so image builds and Go tests still must run on the deployment host.

If any certificate from the removed `agent/certs/` directory has ever been used outside local development, treat it as compromised and issue a new CA/server/client chain rather than reusing it.

## Verification completed in this review

- all 155 Go source files parsed successfully and `gofmt` reported no differences;
- the deployment validator compiled and passed against a temporary non-placeholder configuration;
- shell scripts passed `bash -n`; all JSON and multi-document YAML files parsed;
- the source-package self-test, source-tree hygiene check and final archive hygiene check passed;
- no runtime `.env`, generated config, certificate directory or private-key file is present in the reviewed archive.

These are static/package checks, not a substitute for image build, integration tests or a real VPS bootstrap.

## What Cloud does

Opsi Cloud is the durable identity and control-plane boundary. With PostgreSQL configured it owns:

- users, organizations, projects, memberships and RBAC;
- PAT verification/lifecycle, prelinked OAuth identity mediation and OTP state;
- nodes, Agent registration/rotation/revocation and Agent heartbeats;
- bootstrap sessions, deployment jobs, node lifecycle jobs, leases, retries and dead-letter state;
- GitHub webhook route matching, HMAC verification and sanitized relay envelopes;
- idempotency, rate limits, audit events, support metadata, health and metrics.

Cloud does **not** install or run application workloads itself. Kubernetes/K3s operations and deployment execution belong to the Agent on the target node. Cloud also does not store raw Agent logs/metrics as its primary runtime evidence store.

## What the Bootstrap Worker does

`opsi-bootstrap-worker` is a separate long-running daemon. It processes one bootstrap lease at a time:

1. Poll Cloud and lease the oldest eligible pending/retry session.
2. Receive a short-lived SSH credential, a one-time Agent registration token and a per-lease token.
3. Connect to the target with SSH password or an unencrypted private key.
4. Check Ubuntu, `curl`, `systemd` and passwordless `sudo` when not root.
5. Install/start K3s.
6. Download the Agent and verify its configured SHA-256.
7. Exchange the one-time registration token for an Agent credential.
8. Write `/etc/opsi/agent.yaml`, install a systemd service and start the Agent.
9. Renew the bootstrap lease while running, wait for Agent heartbeat, then report completion or a classified retry/dead-letter failure.

`cloud_url` is only for worker-to-Cloud traffic inside Compose. `agent_cloud_url` is written to the remote Agent and therefore must be reachable from the target VPS.

## Authentication currently implemented

- **First Owner bootstrap:** local-only `opsi-cloud admin bootstrap-owner`; requires PostgreSQL and creates/reuses the first user, organization, project and Owner membership. It can write an initial PAT to a new mode-0600 file and/or prelink an OAuth provider subject.
- **PAT:** bearer token scoped by project or organization membership; only bcrypt hashes are stored; supports expiry, rotation and revocation.
- **Browser OAuth:** generic authorization-code flow. A provider subject must already be linked; callback email alone is not trusted. Pending state/grants are currently in memory.
- **OTP:** PAT-authenticated request and verification endpoints; the user/project and recipient email are derived from the verified PAT identity. Codes are hashed, expire after five minutes, are one-time and rate-limited. Development may use a protected file outbox; real email requires SMTP.
- **Agent:** one-time registration token exchange followed by a scoped bearer credential stored as a hash. Production forces timestamped HMAC request signatures.
- **Bootstrap Worker:** shared internal worker token, validated worker ID and per-lease raw token whose stored representation is hashed.
- **Internal alerts:** dedicated internal token.
- **Agent local API:** bootstrap-generated Agent config now enables Cloud PAT verification; the listener remains loopback-only.

There is no password login and no public self-sign-up flow.

## Fixes applied in this review

1. Added `agent_cloud_url` so a remote Agent no longer receives the unreachable Docker hostname `cloud`.
2. Implemented end-to-end SSH private-key bootstrap instead of exposing a UI option that Cloud rejected.
3. Made `/health` return `503` when PostgreSQL is unavailable.
4. Blocked `/internal/*`, `/api/internal/*` and `/metrics` at the development Caddy edge.
5. Added server timeouts, read-only container filesystems, dropped capabilities and `no-new-privileges` where applicable.
6. Added strict worker URL and Agent SHA-256 validation; production requires HTTPS and a readable known-hosts file.
7. Required PAT authentication for OTP, bound OTP identity to the PAT, and used the verified email rather than a user ID as the SMTP recipient.
8. Required per-route GitHub webhook secrets and verified `X-Hub-Signature-256` before queueing a deployment signal.
9. Enabled Cloud PAT verification in bootstrap-generated Agent configuration.
10. Added cross-file deployment validation for secrets, PostgreSQL credentials, worker token, file modes, URLs, SHA-256 and loopback-only binding.
11. Removed `agent/certs/`, which contained development CA/server/client private keys.

## Required configuration before testing

Create protected runtime files from the examples and set mode `0600`. At minimum, replace:

- PostgreSQL password in `.env` and the password in `OPSI_CLOUD_DATABASE_URL`;
- `OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN` in `.env` and the matching Worker token;
- `OPSI_CLOUD_BOOTSTRAP_SECRET_KEY` and `OPSI_CLOUD_ALERTS_INTERNAL_TOKEN`;
- `agent_install_url` and its exact 64-character SHA-256;
- `agent_cloud_url` with a URL reachable from the target VPS for a real bootstrap test.

Compose injects Cloud scalar configuration from `.env`; `cloud.json` retains
the route list. SMTP and generic OAuth values may remain empty for a private
development smoke test.

For browser login, also configure the OAuth provider and prelink the first Owner subject. For emailed OTP, configure SMTP. For each GitHub route, configure a unique webhook secret of at least 32 bytes and set the same value in GitHub.

## Minimal private control-plane smoke test

```bash
cp deploy/dev-control-plane/.env.example deploy/dev-control-plane/.env
cp deploy/dev-control-plane/config/cloud.example.json deploy/dev-control-plane/config/cloud.json
cp deploy/dev-control-plane/config/bootstrap-worker.example.json deploy/dev-control-plane/config/bootstrap-worker.json
mkdir -p deploy/dev-control-plane/secrets
chmod 0600 deploy/dev-control-plane/.env deploy/dev-control-plane/config/*.json
chmod 0700 deploy/dev-control-plane/secrets

# Edit the three runtime files, then:
make dev-control-plane-validate
make dev-control-plane-build
make dev-control-plane-up
curl --fail http://127.0.0.1:8080/health
```

On a remote control-plane VM, keep the loopback bind and use:

```bash
ssh -L 8080:127.0.0.1:8080 user@control-plane-vm
```

Then create the first Owner using the command in `docs/runbooks/dev_control_plane.md`.

## Remaining blockers and caveats

- The development Compose/Caddy package has no public HTTPS hostname or certificate setup. Do not bind it to `0.0.0.0` on an Internet-facing VM.
- K3s is still installed through an unpinned `https://get.k3s.io` script. Pinning the installer/version and recording provenance is required before production.
- Development mode permits insecure SSH host-key handling. A real security test should mount and configure a verified `known_hosts` file.
- The Agent binary must be published by the operator; this repository does not provide a ready download endpoint or release-signing proof.
- OAuth state and one-time browser grants are in memory, so a restart invalidates an in-progress login and horizontal scaling is not yet coordinated.
- Container image tags are not digest-pinned, and no SBOM/signature/provenance evidence was produced here.
- No Docker build, Compose start, PostgreSQL migration, Go test, real SSH bootstrap or clean-VPS E2E was executed in this review environment.
