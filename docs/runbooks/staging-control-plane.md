# Staging Control Plane And Cloudflare Full (strict)

Status: R5-002 operator runbook. The repository profile and offline validation
exist, but no live origin, DNS, firewall, or Cloudflare change is performed by
this task. Live evidence remains `UNPROVEN` until R5-003.

## Boundary

`deploy/dev-control-plane` remains the local HTTP development package.
`deploy/staging-control-plane` is a separate production-like profile with its
own Compose project name, runtime files, secrets, named volumes, networks, and
Make targets. Never use the development `up` target for staging.

The staging proxy publishes host ports 80 and 443. Caddy runs as UID/GID 1000
and listens on unprivileged container ports 8080 and 8443. The official Caddy
binary carries the `NET_BIND_SERVICE` file capability; with
`no-new-privileges`, retaining that single capability is required for the
binary to execute even though the configured ports are unprivileged. No
privileged mode is used. PostgreSQL, Cloud, and Bootstrap Worker do not publish
host ports. Cloud and Worker use an isolated backend network plus a separate
egress network; the public proxy cannot expose worker or alert internal routes.

PostgreSQL retains only `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, and
`SETUID` during official image initialization so its entrypoint can prepare the
named data volume and drop to the postgres user. Cloud and Worker drop all
capabilities; Caddy retains only `NET_BIND_SERVICE` for the official binary
constraint described above. Caddy stores its non-persistent runtime state on
tmpfs because manual origin certificates and a disabled admin API require no
persistent Caddy state.

## Preconditions

- R5-001 source-package containment checks pass. Credential rotation may still
  be `OPERATOR_REQUIRED`; do not reuse any credential suspected of disclosure.
- The operator has a staging hostname, a proxied Cloudflare DNS record, access
  to the Cloudflare zone, and a maintenance/rollback window.
- Immutable Cloud and Bootstrap Worker image references are available. Do not
  use `latest`.
- TCP 443 can be opened to the origin during cutover. Preserve a separate,
  tested administrative recovery path before restricting origin traffic.
- PostgreSQL backup/restore and the prior deployment rollback path are known.
- Docker Engine and the Compose plugin are installed on the target host.

## Prepare Runtime Files

Run from the repository root. These commands create only gitignored runtime
paths; they do not create a certificate or private key in source control.

```bash
cd deploy/staging-control-plane
umask 077
mkdir -p secrets
cp .env.example .env
cp config/cloud.example.json config/cloud.json
cp config/bootstrap-worker.example.json config/bootstrap-worker.json
chmod 0600 .env
chmod 0644 config/cloud.json config/bootstrap-worker.json
```

Edit `.env` and both JSON files with a local editor. Set the real HTTPS public
origin, matching GitHub callback, immutable images, SMTP identity, GitHub App
identity, pinned K3s version, artifact URLs, and non-zero SHA-256 values. Do not
put secret values in these files; the staging profile consumes them from
individual files under `secrets/`.

Generate new random values locally instead of pasting them into a shell command:

```bash
openssl rand -hex 32 > secrets/postgres-password
openssl rand -hex 32 > secrets/alerts-internal-token
openssl rand -hex 32 > secrets/bootstrap-worker-token
openssl rand -hex 32 > secrets/bootstrap-secret-key
chmod 0600 secrets/*
```

Use a password-manager CLI with stdin/file output, or an editor, for
`smtp-password`, `github-app-client-secret`, and
`github-app-webhook-secret`. Do not place values in command arguments or paste
them into interactive shell history. Create `secrets/database-url` with the
`POSTGRES_USER`, PostgreSQL password, `POSTGRES_DB`, and the internal host
`postgres:5432`. URL-encode the username, password, and database path when they
contain reserved URL characters. The runtime validator URL-decodes all three
values and requires them to match `.env` plus `secrets/postgres-password`
without printing either value. Keep `sslmode=disable` limited to the isolated
Compose backend. The Cloudflare-facing origin connection is separately
protected by Caddy TLS.

Place the GitHub App private key at
`secrets/github-app-private-key.pem` and the pinned SSH host keys at
`secrets/ssh_known_hosts`. Each file must be non-empty, non-symlinked, and not
group/world writable. The same Bootstrap Worker token file is mounted read-only
into Cloud and Worker; no token is duplicated into JSON.

`bootstrap-worker.json` deliberately sets
`allow_insecure_internal_cloud_url=true` for the exact
`http://cloud:9800` control endpoint. Production Worker configuration otherwise
requires HTTPS. This staging-only exception depends on the Compose `backend`
network remaining `internal: true` and on Cloud/Worker publishing no host ports;
the source validator checks those controls. Do not copy this opt-in to another
deployment merely because it uses the same hostname or port. The installed
Agent always receives the separate HTTPS `agent_cloud_url`.

Cloud, Worker, and Caddy run as UID/GID 1000. Before validation, ensure their
JSON files and mounted secret files are readable by that UID. On a root-managed
host, set ownership explicitly without exposing contents:

```bash
chown 1000:1000 config/cloud.json config/bootstrap-worker.json
chown 1000:1000 secrets/database-url secrets/smtp-password secrets/alerts-internal-token
chown 1000:1000 secrets/bootstrap-worker-token secrets/bootstrap-secret-key
chown 1000:1000 secrets/github-app-client-secret secrets/github-app-private-key.pem
chown 1000:1000 secrets/github-app-webhook-secret secrets/ssh_known_hosts
chmod 0600 secrets/*
```

The PostgreSQL password may remain root-owned because the official entrypoint
reads it before dropping privileges. The runtime validator checks ownership and
permissions without printing file contents.

## Prepare Origin TLS

Choose one of these certificate sources:

1. A publicly trusted certificate covering the exact staging hostname.
2. A Cloudflare Origin CA certificate covering the exact staging hostname or an
   intentionally scoped wildcard. This certificate is for Cloudflare-to-origin
   traffic and is not expected to be browser-trusted on direct access.

Generate the private key on the origin or another controlled system. Never
commit it, send it through chat, or pass it as a command argument. Store the
certificate and key as:

```text
deploy/staging-control-plane/secrets/origin-certificate.pem
deploy/staging-control-plane/secrets/origin-private-key.pem
```

Set the private key to mode `0600`. Inspect metadata without printing key data:

```bash
chown 1000:1000 secrets/origin-certificate.pem secrets/origin-private-key.pem
chmod 0600 secrets/origin-certificate.pem secrets/origin-private-key.pem
openssl x509 -in secrets/origin-certificate.pem -noout -subject -issuer -serial -dates -ext subjectAltName
openssl x509 -in secrets/origin-certificate.pem -noout -checkend 2592000
openssl pkey -in secrets/origin-private-key.pem -noout -check
```

Verify the chain against the CA bundle appropriate to the certificate source:

```bash
openssl verify -CAfile /path/to/approved-origin-ca-chain.pem secrets/origin-certificate.pem
```

Confirm that the SAN covers the exact public hostname and that expiry leaves the
team's required rotation margin. An Origin CA certificate, an expired
certificate, or a hostname mismatch must not be accepted merely because a
direct client can be configured to ignore validation.

## Offline Validation

Source/examples and all mandatory negative cases:

```bash
make staging-control-plane-validate-source
```

After runtime files and secrets exist:

```bash
make staging-control-plane-validate
```

The runtime validator reads values only to validate length, placeholders,
permissions, certificate markers, URL identity, and digest shape. It does not
print secret contents. Fix every error before start.

## Start And Check Origin

Start the staging project only after validation:

```bash
make staging-control-plane-up
docker compose --env-file deploy/staging-control-plane/.env -f deploy/staging-control-plane/compose.yaml ps
```

Cloud applies its PostgreSQL migrations during startup. Confirm all four health
checks are healthy and inspect only redacted service errors. Do not dump the
container environment or secret mounts.

Before changing Cloudflare, test TLS directly against the origin while sending
the public hostname for SNI and HTTP Host. Substitute operator-owned values at
execution time; do not add them to this repository:

```bash
curl --fail --show-error --cacert /path/to/approved-origin-ca-chain.pem --resolve '<staging-host>:443:<origin-address>' https://<staging-host>/health
curl --silent --output /dev/null --write-out '%{http_code}\n' --cacert /path/to/approved-origin-ca-chain.pem --resolve '<staging-host>:443:<origin-address>' https://<staging-host>/internal/bootstrap-worker/lease
```

The health request must succeed with normal certificate validation. Internal,
API-internal, metrics, worker, and alert paths must return 404. Also test the
exact path, trailing slash, query string, normalized path, and percent-encoded
path variants. A publicly trusted origin certificate may use the system CA
bundle instead of `--cacert`; a Cloudflare Origin CA certificate requires its
approved Origin CA chain. Never use `--insecure` as acceptance evidence.

## Cloudflare Cutover To Full (strict)

This section is an operator procedure for R5-003; R5-002 does not execute it.

1. Confirm origin HTTPS preflight, valid hostname/chain/expiry, healthy services,
   backup availability, and a recorded rollback owner.
2. Open origin TCP 443 only as broadly and briefly as required for the initial
test. Port 80 serves the health response only to container loopback and
redirects every external request to HTTPS.
3. In Cloudflare, change the zone SSL/TLS encryption mode from Flexible to Full
   (strict). `Always Use HTTPS` may remain enabled for visitors, but it is not a
   substitute for origin TLS. Full (strict) makes Cloudflare validate the origin
   certificate.
4. Request `https://<staging-host>/health` through the proxied hostname. Confirm
   the request succeeds, the DNS record remains proxied, and Cloudflare reports
   no 525/526 error.
5. Check the configured Cloudflare mode and, where the account exposes it,
   origin TLS protocol/certificate events in Cloudflare logs or analytics. A
   visitor-side padlock alone does not prove Cloudflare-to-origin TLS.
6. Repeat the public deny-path matrix. Confirm no `/internal/*`,
   `/api/internal/*`, or `/metrics` response is proxied to Cloud.

Flexible can cause redirect loops when an origin blindly redirects based only
on the origin-side HTTP scheme. This profile avoids that loop by serving the
Cloudflare origin connection on HTTPS 443; the HTTP listener is only a health
response plus a direct 308 redirect.

## GitHub Callback And Webhook Checks

- Confirm the GitHub App callback URL exactly matches the HTTPS public base
  origin and `/v1/auth/browser/callback` path.
- Perform a browser authorization smoke test. Confirm the callback succeeds and
  no authorization code, client secret, or token appears in application logs.
- Deliver a GitHub App webhook from GitHub's delivery UI. Confirm a 2xx response,
  expected redacted audit metadata, and signature enforcement. A callback test
  does not replace webhook verification, and webhook verification does not prove
  GitHub Actions workload identity.

## Restrict Direct-Origin Access

After proxied HTTPS is stable, restrict origin 443 to Cloudflare's published
IPv4 and IPv6 ranges, or use an equivalent origin restriction such as Cloudflare
Tunnel. Automate allowlist refresh and preserve a tested recovery channel.
Cloudflare Authenticated Origin Pulls can add client-certificate authentication;
it complements, rather than replaces, Full (strict) server-certificate
validation. Do not lock the origin until rollback access is proven.

Re-test direct access after restriction: Cloudflare-proxied requests must work,
while unauthorized direct-origin requests must fail before reaching Caddy.
Firewall, DNS, Cloudflare settings, and Authenticated Origin Pulls are live
operator work and are explicitly outside R5-002.

## Certificate Rotation

Create and validate the replacement certificate/key beside the current files
using mode `0600`, then atomically replace each runtime file. Because the Caddy
admin API is disabled, reload by restarting only the proxy:

```bash
docker compose --env-file deploy/staging-control-plane/.env -f deploy/staging-control-plane/compose.yaml restart reverse-proxy
```

Repeat direct-origin certificate checks, proxied health, deny-path tests, and
GitHub callback/webhook checks. Keep the previous certificate available only in
a protected rollback location for the approved rollback window.

## Stop And Roll Back

If origin TLS is unreachable or Cloudflare returns 525/526:

1. Stop the cutover and capture non-sensitive error evidence.
2. Restore the last known-good proxy/certificate files and restart only the
   proxy, or restore the prior deployment profile if needed.
3. If the previous Cloudflare mode must be restored, make that explicit,
   time-bounded operator action and treat visitor-to-origin plaintext as a
   security regression. Do not record Full (strict) as passed.
4. Keep or restore the prior firewall path until the origin is reachable, then
   re-run preflight before another cutover.

To stop the staging project without deleting named data volumes:

```bash
make staging-control-plane-down
```

Do not use `down -v` during rollback unless the database destruction and restore
procedure has been explicitly approved.

## Evidence And Status

Record command names, timestamps, image digests, certificate subject/SAN/issuer
and expiry, health results, Cloudflare mode, callback/webhook results, and
rollback outcome. Never record private keys, secret values, authorization
headers, callback codes, or tokens.

R5-002 may mark staging source/config validation as implemented. Live origin
TLS, Cloudflare Full (strict), direct-origin restriction, and restart/persistence
evidence remain `UNPROVEN` until R5-003 executes and records them.
