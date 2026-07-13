# Development Control Plane

This runbook starts the supported development control plane: PostgreSQL, Opsi
Cloud, one Bootstrap Worker, and a Caddy reverse proxy. It also defines the
V3-013 clean-VM verification path. It is not a production deployment.

## 1. Prerequisites

- Ubuntu 24.04 LTS x86_64 or Fedora 44 x86_64 on a newly provisioned VM.
- At least 2 vCPU, 4 GiB RAM, 20 GiB disk, and outbound internet access.
- `git`, `make`, `curl`, `openssl`, `python3`, Docker Engine, and the Docker
  Compose plugin.
- A user allowed to run Docker commands.

Run all commands from the repository root.

For V3-013, the VM must not be a workstation, a container on a workstation, a
VM that previously ran Opsi, or a simulated CI environment without a real
Docker daemon. Do not record a public hostname, public IP, or cloud credential.

Before cloning the repository, confirm the VM has no prior Opsi resources:

```bash
docker ps -a --format '{{.Names}}' |
  grep -E '^(opsi-|opsi_dev|opsi-dev)' &&
  exit 1 || true

docker volume ls --format '{{.Name}}' |
  grep -E '^(opsi-|opsi_dev|opsi-dev)' &&
  exit 1 || true

docker image ls --format '{{.Repository}}' |
  grep -E '^(opsi-|opsi_dev|opsi-dev)' &&
  exit 1 || true
```

## 2. Clean-VM verification

Clone and check out the exact candidate commit, then run:

```bash
make verify-dev-control-plane-preflight
make verify-dev-control-plane-clean-vm
```

Preflight is read-only: it checks the supported VM baseline, clean Git state,
V3-012 files, Docker/Compose availability, absence of old Opsi resources and an
available loopback port. It does not build images or start containers.

The clean-VM target creates protected runtime configuration from the committed
examples, builds and starts exactly four services, creates the first Owner and
project, verifies the initial PAT through Caddy, restarts each service
independently, and performs a full Compose down/up without deleting volumes.
It writes `docs/evidence/v3-013-clean-vm.md` atomically only after every check
and the exact-value secret scan pass.

No target VPS bootstrap job is executed in V3-013. The verifier does not install
packages, provision a VM, modify firewall or SSH settings, or use Terraform or
Ansible. If a run fails, do not turn partial output into PASS evidence; reset to
a proven clean baseline before rerunning after any packaging repair.

## 3. Manual local setup

The remaining steps are for local development and are not clean-VM evidence.

### Create runtime files

```bash
cp deploy/dev-control-plane/.env.example \
   deploy/dev-control-plane/.env

cp deploy/dev-control-plane/config/cloud.example.json \
   deploy/dev-control-plane/config/cloud.json

cp deploy/dev-control-plane/config/bootstrap-worker.example.json \
   deploy/dev-control-plane/config/bootstrap-worker.json

mkdir -p deploy/dev-control-plane/secrets
chmod 0600 \
  deploy/dev-control-plane/.env \
  deploy/dev-control-plane/config/cloud.json \
  deploy/dev-control-plane/config/bootstrap-worker.json
chmod 0700 deploy/dev-control-plane/secrets
```

The Cloud image runs as UID/GID 1000. If the host operator does not use UID
1000, prepare the secret directory before running the first-owner command:

```bash
sudo chown 1000:1000 deploy/dev-control-plane/secrets
sudo chmod 0700 deploy/dev-control-plane/secrets
```

### Replace placeholders

Replace every `REPLACE_WITH_`, `EXAMPLE_SECRET`, and `CHANGE_ME` value in the
three runtime files. Use independently generated random values except that:

- the PostgreSQL password in `.env` and `cloud.json` must match;
- `bootstrap_worker_token` must match in both JSON files;
- `public_base_url` must match the reverse-proxy address used by the CLI/browser;
- `bootstrap-worker.json.cloud_url` is the internal Docker URL used by the
  worker (`http://cloud:9800` in this package);
- `bootstrap-worker.json.agent_cloud_url` must be reachable from every target
  VPS because it is written into the installed Agent configuration. A Docker
  service name and `127.0.0.1` are invalid for a remote target.

The Agent URL and SHA-256 are operator-supplied development release inputs.
This package does not claim that Agent release signing is implemented.

The Bootstrap Worker accepts SSH password or unencrypted private-key
credentials. In production mode, configure `ssh_known_hosts_path`; the default
development mode does not verify the target host key.

### Validate and build

```bash
make dev-control-plane-validate
make dev-control-plane-build
```

Validation parses both JSON files, checks restrictive file modes, cross-checks
the PostgreSQL password and Bootstrap Worker token, validates the Agent binary
URL/SHA-256, and rejects exposing this HTTP-only development package on a
non-loopback bind address. It does not print secret values.

### Start and inspect health

```bash
make dev-control-plane-up

docker compose \
  --env-file deploy/dev-control-plane/.env \
  -f deploy/dev-control-plane/compose.yaml \
  ps

curl --fail \
  "http://127.0.0.1:8080/health"
```

If the bind address or port changed, use the values from `.env` for the health
request. Cloud performs its existing PostgreSQL schema migration during startup
after PostgreSQL becomes healthy. `/health` also returns `503` if PostgreSQL is
no longer reachable.

The committed Caddy policy intentionally does not expose `/internal/*`,
`/api/internal/*`, or `/metrics`. The Bootstrap Worker and observability stack
must use the internal `cloud:9800` service address.

### Access from a cloud VM

This Compose package is HTTP-only and binds to `127.0.0.1` by default. For an
initial control-plane test, keep that bind and use an SSH tunnel:

```bash
ssh -L 8080:127.0.0.1:8080 user@control-plane-vm
```

Do not change `OPSI_DEV_BIND_ADDRESS` to `0.0.0.0` on an Internet-facing VM
without replacing the development Caddy configuration with a real hostname,
automatic HTTPS, and firewall rules. A remote Agent bootstrap additionally
requires a routable HTTPS `agent_cloud_url`; an SSH tunnel used only by the
operator is not reachable by the Agent.

### Bootstrap the first Owner

Ensure `deploy/dev-control-plane/secrets` is writable by UID/GID 1000, then run:

```bash
docker compose \
  --env-file deploy/dev-control-plane/.env \
  -f deploy/dev-control-plane/compose.yaml \
  run --rm cloud \
  admin bootstrap-owner \
  --config /etc/opsi/cloud.json \
  --email owner@example.invalid \
  --org-name "Opsi Dev" \
  --project-name "Default Project" \
  --pat-output-file /run/opsi-secrets/initial-owner.pat
```

The command never prints the raw PAT. The file is created with mode `0600` and
is ignored by Git. When host UID 1000 is not used, restore ownership without
displaying the PAT:

```bash
sudo chown "$(id -u):$(id -g)" \
  deploy/dev-control-plane/secrets \
  deploy/dev-control-plane/secrets/initial-owner.pat
chmod 0700 deploy/dev-control-plane/secrets
chmod 0600 deploy/dev-control-plane/secrets/initial-owner.pat
```

An exact rerun reuses the durable first-owner state and does not issue another
PAT. A conflicting owner tuple fails closed. To preserve the initial PAT, do
not delete or overwrite `initial-owner.pat`; use the normal PAT lifecycle for
later rotation.

### Restart a service

```bash
docker compose \
  --env-file deploy/dev-control-plane/.env \
  -f deploy/dev-control-plane/compose.yaml \
  restart cloud
```

Replace `cloud` with `postgres`, `bootstrap-worker`, or `reverse-proxy` for a
basic local restart. Independent restart evidence belongs to V3-013.

### Stop services

```bash
make dev-control-plane-down
```

This keeps the PostgreSQL and Cloud named volumes. Do not add `-v` unless data
removal is intentional and separately approved.

## 4. Troubleshooting

- Run `make dev-control-plane-validate` first for missing files or placeholders.
- Use `docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml ps` to inspect health.
- Use the same Compose prefix with `logs cloud`, `logs bootstrap-worker`, or `logs reverse-proxy`; do not paste secrets or full configuration into logs or reports.
- Use `docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml exec postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'` only for database diagnostics, never to bootstrap the Owner.
- After V3-013, runtime files remain gitignored and protected for inspection.
  Stop the stack before deleting them. Never use `docker compose down -v` when
  persistence evidence or data retention matters.
