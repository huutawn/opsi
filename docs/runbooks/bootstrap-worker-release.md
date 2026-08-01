# Bootstrap Worker release and barrier procedure

Status: source procedure only. Publishing, deployment, SSH, and live acceptance
remain operator-gated; this document does not claim a live pass.

## Context and normal release

The publish operator runs the reviewed GitHub workflow from `developer` and
records its immutable `ghcr.io/huutawn/opsi-bootstrap-worker@sha256:<digest>`
manifest. The staging operator runs the helper from the canonical checkout on
the staging control-plane host. The Local API/session request runs from the
operator context; host, key, and fingerprint values come only from secure
operator-owned environment/configuration.

Normal same-image deploy is idempotent: when the running RepoDigest and
persisted `OPSI_BOOTSTRAP_WORKER_IMAGE` both equal the expected digest, the
helper checks Worker health, RepoDigest, and Cloud health, then exits with
`result=same-image-no-op`. It does not pull, edit `.env`, create a backup, or
recreate any container.

A normal new-image deploy uses the same helper and only changes the Worker
binding. It is the canonical path; do not run `docker compose up` directly in
an acceptance run.

## Explicit same-image barrier recreation

The barrier path is the only reason to restart an unchanged image. It requires
all of the following before mutation:

- `deploy` plus `--force-recreate-same-image` and target digest equal to
  `--expected-current-digest`;
- exactly one `bootstrap-worker` container, whose immutable RepoDigest and
  persisted `.env` binding already match the expected digest;
- exactly `compose.e2e-bootstrap-barrier.yaml` from the canonical staging
  compose directory, with no traversal, symlink, or outside path;
- private, regular `config/bootstrap-worker.e2e.json` with no placeholders,
  `production: false`, and the exact run/session barrier target;
- private marker `barrier-state/install_k3s-<session/run hash>.json` in state
  `armed` for that same session and run.

The helper does not pull or edit `.env` in this mode. It records the old
container ID, runs exactly:

```text
up -d --no-deps --force-recreate bootstrap-worker
```

with the canonical base and barrier override, then requires a different
container ID, healthy Worker, the same actual RepoDigest, and passing Cloud
health. It prints `result=same-image-barrier-recreated`. Production barrier
configuration, wrong target digests, invalid state, and all precondition
mismatches fail before recreate.

## Canonical ordering

Run the following procedure on the staging control-plane host. The Local API
is operator-owned; expose it to that host only through an operator-approved
private tunnel or equivalent secure local context. The protected state file
contains run/session/container metadata only, never PEM, PAT, OTP, or request
body data.

Set only operator-owned values (no credentials in shell history):

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=...
export OPSI_E2E_SSH_KEY_PATH=/protected/operator/key
export OPSI_E2E_BOOTSTRAP_WORKER_DIGEST=sha256:<currently-approved-digest>
export OPSI_E2E_RUN_ID="r5-017d1-$(date -u +%Y%m%dT%H%M%SZ)-$$"
export OPSI_E2E_STAGING_COMPOSE_DIRECTORY=/home/ubuntu/opsi/deploy/staging-control-plane
```

1. `--barrier-prepare /protected/state/bootstrap-barrier.json` proves one
   Worker exists, stops only that Worker, and verifies the singleton is
   stopped before creating a session.
2. The same command POSTs the canonical Local API bootstrap route, takes the
   factual response `id` as `session_id`, and writes protected state. It never
   guesses an ID or creates a second Worker.
3. It creates private run-specific config from the normal Worker config, then
   arms the exact marker. It verifies `armed` and matching session/run before
   invoking the release helper.
4. The helper performs the explicit same-image barrier recreation and proves
   container replacement, health, immutable RepoDigest, and Cloud health.

```bash
scripts/e2e/verify-k3s.sh --barrier-prepare \
  /protected/state/bootstrap-barrier.json
```

The procedure restores the normal Worker profile if session creation, config,
arm, or barrier recreation fails. Restoration targets only `bootstrap-worker`;
PostgreSQL, Cloud, and reverse proxy are never recreated. A restoration failure
is reported separately and returns nonzero. `reached`, `consumed`, and
`completed` markers are never deleted or reset by failure handling.

## Replay, resume, and cleanup

Poll the factual marker without fixed sleeps:

```bash
scripts/e2e/bootstrap-worker-barrier.sh status \
  --state-dir "$OPSI_E2E_STAGING_COMPOSE_DIRECTORY/barrier-state" \
  --session-id <factual-session-id> --run-id "$OPSI_E2E_RUN_ID"
```

After `reached`, restart only the Worker through the dedicated canonical
`barrier-replay` operation. It must replace the container and leave the marker
to progress `consumed -> completed`:

```bash
scripts/e2e/verify-k3s.sh --barrier-restart \
  /protected/state/bootstrap-barrier.json
```

Continue the full E2E with the existing session; this path does not POST a
second bootstrap session:

```bash
scripts/e2e/verify-k3s.sh --resume-bootstrap-session \
  /protected/state/bootstrap-barrier.json
```

After evidence shows `consumed` and `completed`, restore the normal profile with
the dedicated `barrier-restore` operation (base Compose only; no pull, `.env`
edit, or binding backup), then explicitly disarm the marker. Do not disarm `reached`, `consumed`, or
`completed` before evidence collection:

```bash
scripts/e2e/verify-k3s.sh --barrier-restore \
  /protected/state/bootstrap-barrier.json
scripts/e2e/bootstrap-worker-barrier.sh disarm \
  --state-dir "$OPSI_E2E_STAGING_COMPOSE_DIRECTORY/barrier-state" \
  --session-id <factual-session-id> --run-id "$OPSI_E2E_RUN_ID"
rm -f -- "$OPSI_E2E_STAGING_COMPOSE_DIRECTORY/config/bootstrap-worker.e2e.json"
```

The logical Agent Host header may be used with `curl --resolve`; that proves
only the selected IP/Host routing. It does not prove public Agent DNS, HTTPS,
or TLS. No publish, image push, staging deploy, VPS reset, or live E2E is
performed by the source tests.

## Rollback

Rollback remains an explicit normal release operation. Set
`--expected-current-digest` to the digest factually running at rollback start,
use `rollback --to <immutable-reference>`, and let the helper verify health,
RepoDigest, and Cloud health. Barrier overrides and the force flag are deploy
only.
