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
export OPSI_E2E_SECOND_FACTOR_DIR=/protected/operator/opsi-second-factor
```

The script validates the key without printing or copying it to evidence. It
rejects symlinks, non-regular/unreadable files, group/other permission bits,
empty files, oversized files, and files without a recognized PEM/OpenSSH
private-key marker. Host identity is pinned with `ssh-keyscan` and `ssh-keygen`;
every direct SSH call uses `BatchMode=yes`, `IdentitiesOnly=yes`,
`StrictHostKeyChecking=yes`, a dedicated mode-0600 `known_hosts`, and the
protected key path. A changed or ambiguous fingerprint fails closed.

Prepare the second-factor handoff directory before preflight. This validates a
current-user-owned, non-symlink directory with exact mode 0700; preflight does
not require an expiring code:

```bash
python3 scripts/e2e/second_factor_handoff.py prepare \
  --directory "$OPSI_E2E_SECOND_FACTOR_DIR"
```

When the running harness reaches secret rotation, use a separate trusted local
terminal to stage one factor through a hidden prompt. Staging opens and
validates the controlling `/dev/tty`, disables echo, and fails closed without
reading redirected stdin when no controlling terminal is available. The value
is never a command argument, environment variable, or shell-history entry:

```bash
python3 scripts/e2e/second_factor_handoff.py stage-totp \
  --directory "$OPSI_E2E_SECOND_FACTOR_DIR" --operation rotate
```

The harness consumes and deletes `rotate.json` only at the rotate boundary. A
TOTP handoff is accepted only in its recorded 30-second period and is reused
only for the immediately adjacent reveal while still current. If it expires
between operations, stage a fresh TOTP for `--operation reveal`.

Cloud OTP fallback requires two distinct one-time pairs. Stage the factual
rotate pair when rotation is waiting, then stage a separately requested factual
reveal pair when reveal is waiting:

```bash
python3 scripts/e2e/second_factor_handoff.py stage-otp \
  --directory "$OPSI_E2E_SECOND_FACTOR_DIR" --operation rotate
python3 scripts/e2e/second_factor_handoff.py stage-otp \
  --directory "$OPSI_E2E_SECOND_FACTOR_DIR" --operation reveal
```

Each handoff must be a current-user-owned regular non-symlink file with exact
mode 0600 and at most 512 bytes. The harness waits 60 seconds by default; the
optional `OPSI_E2E_SECOND_FACTOR_TIMEOUT` must be between 1 and 120 seconds.
Malformed, stale, oversized, insecure, or reused input fails closed and is
removed. Request bodies and exact redaction values live only in a private
temporary directory that is removed on exit. Never provide OTP, TOTP, PAT, or
TOTP seed content through chat, and never store or expose the TOTP seed in this
workflow.

Secret create, rotate, and reveal responses are parsed through a bounded JSON
stream before evidence is written. Exact non-empty generated `username` and
`password` values are added to the private mode-0600 redaction registry, both
fields are redacted in response evidence, and later unlabeled occurrences fail
the artifact leak scan. The unredacted response body is never written to an
evidence or temporary file.

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

The barrier uses three separate trust domains. The operator workstation owns
the loopback-only Local API, Local API session, second factor, Agent VPS key,
and a distinct staging SSH key and pinned host identity. The staging host owns
the exact repository checkout, Docker/Compose, Worker lifecycle, barrier
config, marker, and remote state. The Agent VPS remains the existing separate
pinned target. No tunnel, SSHFS, shared mount, local Docker fallback, or Agent
SSH identity reuse is permitted.

Before this procedure, use the separately authorized source-alignment process
to place the exact committed source revision on staging. Do not fetch, pull,
checkout, or upload source through the verifier. The operator checkout and the
staging checkout must have the same full revision; the staging tracked
worktree/index and committed helper blob must be clean and exact.

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=...
export OPSI_E2E_SSH_KEY_PATH=/protected/operator/agent-vps.key
export OPSI_E2E_BOOTSTRAP_WORKER_DIGEST=sha256:<currently-approved-digest>
export OPSI_E2E_RUN_ID="r5-017-$(date -u +%Y%m%dT%H%M%SZ)-$$"
export OPSI_E2E_STAGING_HOST=staging.example
export OPSI_E2E_STAGING_SSH_PORT=22
export OPSI_E2E_STAGING_SSH_USER=opsi-staging
export OPSI_E2E_STAGING_SSH_KEY_PATH=/protected/operator/staging.key
export OPSI_E2E_STAGING_KNOWN_HOSTS_PATH=/protected/operator/staging.known_hosts
export OPSI_E2E_STAGING_HOST_KEY_SHA256=SHA256:<approved-fingerprint>
export OPSI_E2E_STAGING_REPOSITORY_DIRECTORY=/srv/opsi
export OPSI_E2E_STAGING_COMPOSE_DIRECTORY=/srv/opsi/deploy/staging-control-plane
export OPSI_E2E_SOURCE_REVISION="$(git rev-parse HEAD)"
scripts/e2e/verify-k3s.sh --barrier-prepare \
  /protected/state/bootstrap-barrier.json
```

`--barrier-prepare` invokes only the committed remote helper over pinned SSH.
It requires a revision/helper-bound `preflight` receipt, then a `prepare`
receipt proving the single Worker is stopped, before it authenticates the
Local API and sends exactly one bootstrap POST. The factual session ID is
fsynced into protected local state before remote config, marker arm, and
same-digest Worker recreation. Requests and receipts are strict, bounded JSON;
successful SSH requires one receipt, empty stderr, and exit zero. Local API,
second-factor, PAT, secret, Agent-key, and bootstrap-body material never enters
remote stdin, stdout, stderr, state, or receipts.

After `reached`, restart only the Worker through the canonical `barrier-replay`
helper operation and require
`consumed -> completed` evidence:

```bash
scripts/e2e/verify-k3s.sh --barrier-restart \
  /protected/state/bootstrap-barrier.json
scripts/e2e/verify-k3s.sh --resume-bootstrap-session \
  /protected/state/bootstrap-barrier.json
```

The resume path never POSTs a second bootstrap request. Restore the normal
profile only after factual `completed`; restoration uses canonical base
Compose with no pull, `.env` mutation, binding backup, or dependency target.
Preserve `reached`, `consumed`, and `completed` marker evidence:

```bash
scripts/e2e/verify-k3s.sh --barrier-restore \
  /protected/state/bootstrap-barrier.json
```

Before session creation, a failed prepare or bootstrap request invokes the
remote pre-session abort and requires one healthy normal Worker. After factual
session creation, any failure preserves the stopped/barrier Worker and both
state records; it never restores an unbarriered Worker or sends another POST.
Rerunning `--barrier-prepare` with the same protected state continues only when
read-only status proves a safe pre-recreation state. An `armed` or otherwise
ambiguous state stops for inspection. SSH timeout/disconnect is reconciled only
with remote `status`; the mutating phase is accepted only when status proves
its exact completed transition.

Invoke `scripts/validate-staging-control-plane.py` directly, without an
external wrapper, and capture stderr separately. The next live run fails
acceptance if validator stderr is non-empty even when its exit status is zero.
Historical `stat: missing operand` warnings came from an external/ad hoc
wrapper and are not claimed fixed by this source correction.

```bash
python3 scripts/validate-staging-control-plane.py --runtime \
  >validator.stdout 2>validator.stderr
test ! -s validator.stderr
```

The logical Agent Host header may use `curl --resolve`; it proves only the
selected IP/Host route, not public DNS, HTTPS, or Agent TLS. This correction was
tested only with local fake SSH/Docker fixtures; no live staging, Agent VPS,
K3s, PostgreSQL, deployment, or Run 1 action occurred.

## Limits and status

The full scenario has no accepted real-infrastructure artifact until an
operator runs it against the protected Local API/UI and reviews redacted
evidence. Do not change `MANUAL_GATED` without that evidence. R5-011 remains
`PARTIAL`; R5-011.4 remains `MANUAL_GATED`. This runbook does not claim R5-012,
MCP, AI, DNS, TLS, or public endpoint acceptance. The new source revision makes
the previous runtime artifacts revision-unaligned; aligned Cloud, Worker, and
Agent artifacts must be republished before a new Run 1. R5-017 remains pending.
