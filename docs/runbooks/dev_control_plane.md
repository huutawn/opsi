# Development Control Plane

This runbook starts the supported development control plane: PostgreSQL, Opsi
Cloud, one Bootstrap Worker, and a Caddy reverse proxy. It is not a production
deployment or clean-VM evidence.

## 1. Prerequisites

- Docker Engine with the Docker Compose plugin.
- A user allowed to run Docker commands.
- `curl`, `make`, and `rg` on the host.

Run all commands from the repository root.

## 2. Create runtime files

```bash
cp deploy/dev-control-plane/.env.example \
   deploy/dev-control-plane/.env

cp deploy/dev-control-plane/config/cloud.example.json \
   deploy/dev-control-plane/config/cloud.json

cp deploy/dev-control-plane/config/bootstrap-worker.example.json \
   deploy/dev-control-plane/config/bootstrap-worker.json

mkdir -p deploy/dev-control-plane/secrets
chmod 0700 deploy/dev-control-plane/secrets
```

The Cloud image runs as UID/GID 1000. If the host operator does not use UID
1000, prepare the secret directory before running the first-owner command:

```bash
sudo chown 1000:1000 deploy/dev-control-plane/secrets
sudo chmod 0700 deploy/dev-control-plane/secrets
```

## 3. Replace placeholders

Replace every `REPLACE_WITH_`, `EXAMPLE_SECRET`, and `CHANGE_ME` value in the
three runtime files. Use independently generated random values except that:

- the PostgreSQL password in `.env` and `cloud.json` must match;
- `bootstrap_worker_token` must match in both JSON files;
- `public_base_url` must match the reverse-proxy bind address and port.

The Agent URL and SHA-256 are operator-supplied development release inputs.
This package does not claim that Agent release signing is implemented.

## 4. Validate and build

```bash
make dev-control-plane-validate
make dev-control-plane-build
```

Validation reports only the path of a file containing a placeholder; it does
not print configuration or secret values.

## 5. Start and inspect health

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
after PostgreSQL becomes healthy.

## 6. Bootstrap the first Owner

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

## 7. Restart a service

```bash
docker compose \
  --env-file deploy/dev-control-plane/.env \
  -f deploy/dev-control-plane/compose.yaml \
  restart cloud
```

Replace `cloud` with `postgres`, `bootstrap-worker`, or `reverse-proxy` for a
basic local restart. Independent restart evidence belongs to V3-013.

## 8. Stop services

```bash
make dev-control-plane-down
```

This keeps the PostgreSQL and Cloud named volumes. Do not add `-v` unless data
removal is intentional and separately approved.

## 9. Troubleshooting

- Run `make dev-control-plane-validate` first for missing files or placeholders.
- Use `docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml ps` to inspect health.
- Use the same Compose prefix with `logs cloud`, `logs bootstrap-worker`, or `logs reverse-proxy`; do not paste secrets or full configuration into logs or reports.
- Use `docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml exec postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'` only for database diagnostics, never to bootstrap the Owner.
