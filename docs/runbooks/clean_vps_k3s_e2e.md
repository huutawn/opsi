# Clean VPS/K3s immutable acceptance

Status: active operator runbook; `MANUAL_GATED`.

This is an explicit local operator workflow. There is no GitHub-hosted workflow
for this acceptance, and the repository does not SSH to a target automatically.
Start the Local backend/UI yourself, then run:

```bash
make verify-e2e-k3s-preflight
make verify-e2e-k3s
```

## Inputs

Required tools are `bash`, `curl`, `python3`, `ssh`, `ssh-keygen`,
`ssh-keyscan`, `timeout`, `go`, `node`, `npm`, and `kubectl`.

Set the Local API/UI and canonical immutable-delivery inputs from operator-owned
secure environment/configuration. Do not put credentials in shell history or
evidence.

Before staging deployment, require one exact reviewed revision across the
runtime set. Resolve Cloud and Bootstrap Worker only from the combined
control-plane manifest. Resolve Agent only from the immutable public prerelease
tag `agent-<full-source-revision>` and its stable anonymous assets:
`opsi-agent-linux-amd64`, `checksums.txt`, and `release.json`. Verify the exact
asset set, lowercase SHA-256, Linux amd64 ELF, `opsi-agent --version`, embedded
full revision, and strict metadata before installing. A publisher workflow run
is artifact evidence only; it does not authorize staging deployment or begin
Run 1/Run 2.

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=...
export OPSI_E2E_SSH_KEY_PATH=/protected/operator/key
export OPSI_E2E_VPS_HOST_KEY_SHA256=SHA256:<operator-pinned-fingerprint>
export OPSI_E2E_BUILD_RECORD_ID=...
export OPSI_E2E_BAD_BUILD_RECORD_ID=...
export OPSI_E2E_ENVIRONMENT_ID=...
export OPSI_E2E_SERVICE_KEY=...
export OPSI_E2E_REPLICAS=1
export OPSI_E2E_CONTAINER_PORT=8080
export OPSI_E2E_CPU_REQUEST=100m
export OPSI_E2E_MEMORY_REQUEST=128Mi
export OPSI_E2E_CPU_LIMIT=500m
export OPSI_E2E_MEMORY_LIMIT=512Mi
export OPSI_E2E_TOTP_CODE=...
```

The script validates the key without printing or copying it to evidence. It
rejects symlinks, non-regular/unreadable files, group/other permission bits,
empty files, oversized files, and files without a recognized PEM/OpenSSH
private-key marker. Host identity is pinned with `ssh-keyscan` and `ssh-keygen`;
every direct SSH call uses `BatchMode=yes`, `IdentitiesOnly=yes`,
`StrictHostKeyChecking=yes`, a dedicated mode-0600 `known_hosts`, and the
protected key path. A changed or ambiguous fingerprint fails closed.

## Local API/UI and bootstrap

The operator opens the Local UI served by `opsi start` and confirms the same
project/session state through the Local API. The bootstrap request uses
`auth_method: private_key`; a bounded mode-0600 temporary request file is
deleted immediately after `POST /api/local/projects/<project>/nodes/bootstrap`.
The request body is never copied into evidence. The Local backend maps it to a
Cloud bootstrap session; Worker/Agent bootstrap must complete and readiness must
be `ready` before deployment.

## Acceptance sequence

1. Bootstrap completes through the Local API and the target Agent reports
   healthy readiness.
2. The accepted BuildRecord is submitted to the project-scoped deployment
   endpoint. The resulting DeploymentJob/RolloutIntent is routed by the exact
   TopologyPlan and DeploymentPolicy.
3. Agent `PollJob` consumes the job through `ReconcileRollout`, deploys the
   immutable OCI digest, and Opsi-owned K3s resources become healthy. Evidence
   includes desired/current digest, known-good identity/hash, readiness hash,
   resource identities, and exact runtime/node/Agent identity.
4. The known-bad BuildRecord must follow `failed -> rolling_back -> rolled_back`.
   `failed`, `rollback_failed`, `succeeded`, or `cancelled` are immediate
   harness failures while waiting. Desired digest B must remain recorded while
   current and previous digest equal A and A known-good evidence is unchanged.
5. K3s readiness is checked again after rollback. Every ready application
   `imageID` must match digest A. The harness then verifies factual incident
   list/detail/resolve and the `incident.resolve` audit, selecting only an
   incident for the same service created at or after the broken-B timestamp.
6. Redacted evidence and cleanup guidance are written; PEM markers and secret
   values are rejected.

`make verify-e2e-k3s-selfcheck` replaces `ssh-keyscan` with a temporary PATH
stub and tests correct, wrong, zero-match, and duplicate-match fingerprints
without network access. Local fixtures cover rollout, rollback, incident
selection, and redaction. No Git clone, source SHA, Docker build, arbitrary
manifest, caller-selected authority, service-scoped deployment, or generic
webhook relay participates in this path.

## Deterministic Bootstrap Worker crash proof

R5-017D1 closes the orchestration gap with one fail-closed source procedure.
The operator context owns the Local API and protected bootstrap key; the
staging control-plane host owns Compose, `.env`, the run-specific config, and
the private marker directory. Host, key, fingerprint, project, and immutable
digest values come only from operator-owned environment or secure config.

The release must already be published/deployed or otherwise present at the
approved immutable digest. Normal same-image release remains a no-op. The
barrier path uses explicit `--force-recreate-same-image`; it never changes
`.env`, pulls the image, or creates a binding backup.

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=...
export OPSI_E2E_SSH_KEY_PATH=/protected/operator/key
export OPSI_E2E_BOOTSTRAP_WORKER_DIGEST=sha256:<currently-approved-digest>
export OPSI_E2E_RUN_ID="r5-017d1-$(date -u +%Y%m%dT%H%M%SZ)-$$"
export OPSI_E2E_STAGING_COMPOSE_DIRECTORY=/home/operator/opsi/deploy/staging-control-plane
scripts/e2e/verify-k3s.sh --barrier-prepare \
  /protected/state/bootstrap-barrier.json
```

`--barrier-prepare` proves exactly one Worker, stops it, and verifies it is
stopped before POSTing the canonical Local API bootstrap route. The factual
response `id` becomes the session ID; a protected state file stores only
run/session/container metadata. The command creates a private config from the
normal Worker config, arms the matching `armed` marker, and calls the canonical
release helper. Any session, config, arm, or recreate failure restores only the
normal Worker profile. Restoration failure is reported separately and is
nonzero; PostgreSQL, Cloud, reverse proxy, and Agent VPS are never recreated by
restoration.

Poll factual state, without a fixed sleep:

```bash
scripts/e2e/bootstrap-worker-barrier.sh status \
  --state-dir "$OPSI_E2E_STAGING_COMPOSE_DIRECTORY/barrier-state" \
  --session-id <factual-session-id> --run-id "$OPSI_E2E_RUN_ID"
```

After `reached`, restart only the Worker through the canonical `barrier-replay`
helper operation and require
`consumed -> completed` evidence:

```bash
scripts/e2e/verify-k3s.sh --barrier-restart \
  /protected/state/bootstrap-barrier.json
scripts/e2e/verify-k3s.sh --resume-bootstrap-session \
  /protected/state/bootstrap-barrier.json
```

The resume path uses the existing factual session and never POSTs a second
bootstrap request. Review checkpoint/session events and read-only K3s evidence
before cleanup. Restore the normal profile with `barrier-restore` (no barrier
override, pull, `.env` mutation, or binding backup), then explicitly disarm;
never delete or reset `reached`, `consumed`, or `completed` as retry logic:

```bash
scripts/e2e/verify-k3s.sh --barrier-restore \
  /protected/state/bootstrap-barrier.json
scripts/e2e/bootstrap-worker-barrier.sh disarm \
  --state-dir "$OPSI_E2E_STAGING_COMPOSE_DIRECTORY/barrier-state" \
  --session-id <factual-session-id> --run-id "$OPSI_E2E_RUN_ID"
```

The logical Agent Host header may use `curl --resolve`; it proves only the
selected IP/Host route, not public DNS, HTTPS, or Agent TLS. This source
procedure performs no publish, deploy, SSH, VPS reset, or live E2E mutation.

## Limits and status

The full scenario has no accepted real-infrastructure artifact until an
operator runs it against the protected Local API/UI and reviews redacted
evidence. Do not change `MANUAL_GATED` without that evidence. R5-011 remains
`PARTIAL`; R5-011.4 remains `MANUAL_GATED`. This runbook does not claim R5-012,
MCP, AI, DNS, TLS, or public endpoint acceptance.
