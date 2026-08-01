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

Set the Local API/UI and canonical immutable-delivery inputs. Values are
operator secrets or project metadata; do not put them in shell history or
evidence.

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST='54.254.183.11'
export OPSI_E2E_VPS_SSH_USER='ubuntu'
export OPSI_E2E_SSH_KEY_PATH="$HOME/key/ta.pem"
export OPSI_E2E_VPS_HOST_KEY_SHA256='SHA256:URwAq88NRpjLdVWdQzrZYnc7KZ++JHhRPSIwVFg2tLY'
export OPSI_E2E_BUILD_RECORD_ID=accepted-record-id
export OPSI_E2E_BAD_BUILD_RECORD_ID=known-bad-record-id
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

The disposable R5-017 Agent VPS values are fixed for this proof. Before using
the key, verify the local file without printing it:

```bash
test -f "$HOME/key/ta.pem"
chmod 600 "$HOME/key/ta.pem"
```

The key path is expanded to an absolute path and checked before it is read. The
script rejects symlinks, non-regular/unreadable files, group/other permission
bits, empty files, files over 1 MiB, and files without a recognized
PEM/OpenSSH private-key marker. The key contents are never placed in an argv value,
environment variable, log, evidence artifact, or cleanup instruction.

The script obtains bounded candidate keys with `ssh-keyscan`, computes each
SHA-256 fingerprint with `ssh-keygen`, and requires exactly one match for
`OPSI_E2E_VPS_HOST_KEY_SHA256`. Only that matching line is written to a
mode-`0600` temporary `known_hosts` file. Every direct SSH call uses
`BatchMode=yes`, `IdentitiesOnly=yes`, `StrictHostKeyChecking=yes`, that
dedicated file, and `-i "$OPSI_E2E_SSH_KEY_PATH"`; a changed or ambiguous host
key fails closed.

## Local API/UI and bootstrap

The operator opens the Local UI served by `opsi start` and confirms the same
project/session state through the Local API. The bootstrap request uses
`auth_method: private_key`. A bounded JSON-generation process reads the
validated key into a mode-`0600` temporary request file, submits
`POST /api/local/projects/<project>/nodes/bootstrap`, and deletes the request
file immediately after the call. The request is never copied into evidence.

The Local backend maps this to the Cloud bootstrap session. Worker/Agent
bootstrap must complete and the Local readiness endpoint must report `ready`
before deployment. The transport route may retain the historical
`/webhooks/next` name, but Agent `PollJob` carries only canonical deployment or
node lifecycle jobs; it is not a generic webhook relay.

## Acceptance sequence

1. Bootstrap completes through the Local API and the target Agent reports
   healthy readiness.
2. The accepted `OPSI_E2E_BUILD_RECORD_ID` is submitted to the project-scoped
   deployment endpoint. The resulting durable DeploymentJob/RolloutIntent is
   routed by the exact TopologyPlan and DeploymentPolicy.
3. Agent `PollJob` consumes the job through `ReconcileRollout` and its
   `ProductionAdapter`, deploys the immutable OCI digest, and Opsi-owned K3s
   resources become healthy. Evidence includes desired/current digest,
   known-good identity/hash, readiness hash, resource identities, and exact
   runtime/node/Agent identity.
4. The known-bad `OPSI_E2E_BAD_BUILD_RECORD_ID` must follow `failed ->
   rolling_back -> rolled_back`. `failed`, `rollback_failed`, `succeeded`, or
   `cancelled` are immediate harness failures while waiting. The final record
   must keep desired digest B while current and previous digest equal A and the
   A known-good identity/hash remain unchanged.
5. K3s readiness is checked again after rollback. Every ready application
   container imageID must match digest A, so the harness cannot print PASS while
   the target workload is unhealthy. The script then verifies factual incident
   list/detail/resolve and the `incident.resolve` audit. It records a Unix
   timestamp immediately before creating broken deployment B and resolves only
   an incident for the same service with `created_at_unix` at or after that
   boundary; an older incident cannot satisfy the acceptance.
6. The script writes redacted evidence and cleanup guidance. It rejects any
   artifact containing a PEM private-key marker or sensitive value.

`make verify-e2e-k3s-selfcheck` replaces `ssh-keyscan` through a temporary PATH
stub and tests correct, wrong, zero-match, and duplicate-match fingerprints
without network access. Local JSON fixtures cover succeeded A, rolled-back B,
failed, rollback-failed, cancelled, incorrect rollback metadata, rejection of a
stale same-service incident, and selection of a fresh controlled incident.

No Git clone, source SHA, Docker build, arbitrary manifest, caller-selected
authority, service-scoped deployment, or generic webhook relay participates in
this path.

## Deterministic Bootstrap Worker crash proof

R5-017A supplies a staging-only file marker for the exact boundary
`install_k3s` remote success -> checkpoint write. It is dormant in the normal
staging Compose file and is not reachable when Worker configuration has
`production: true`. The explicit E2E override uses a run-specific config with
`production: false`, mounts only the private marker directory, and must never be
included in normal staging startup.

Confirm the Cloud health boundary before creating or leasing a session:

```bash
curl --fail --silent --show-error https://opsidev.site/health
```

First pin the VPS host identity. Do not use `StrictHostKeyChecking=no`:

```bash
tmp_candidates="$(mktemp)"
tmp_known_hosts="$(mktemp)"
chmod 600 "$tmp_candidates" "$tmp_known_hosts"
ssh-keyscan -T 5 -t ed25519 -p 22 54.254.183.11 >"$tmp_candidates"
fingerprint="$(ssh-keygen -lf "$tmp_candidates" -E sha256 | awk '$2 ~ /^SHA256:/ {print $2; exit}')"
test "$fingerprint" = 'SHA256:URwAq88NRpjLdVWdQzrZYnc7KZ++JHhRPSIwVFg2tLY'
awk '$2 == "ssh-ed25519" {print; count++} END {exit count == 1 ? 0 : 1}' "$tmp_candidates" >"$tmp_known_hosts"
chmod 600 "$tmp_known_hosts"
export OPSI_E2E_SSH_KNOWN_HOSTS="$tmp_known_hosts"
```

If the fingerprint differs, stop before SSH or VPS mutation. All later SSH
commands use `BatchMode=yes`, `IdentitiesOnly=yes`,
`StrictHostKeyChecking=yes`, `UserKnownHostsFile="$OPSI_E2E_SSH_KNOWN_HOSTS"`,
and the protected key path.

Prepare an E2E-only Worker config from
`deploy/staging-control-plane/config/bootstrap-worker.e2e.example.json`, set its
real session/run IDs, create the private state directory, and arm the marker:

```bash
install -d -m 700 deploy/staging-control-plane/barrier-state
cp deploy/staging-control-plane/config/bootstrap-worker.e2e.example.json \
  deploy/staging-control-plane/config/bootstrap-worker.e2e.json
export OPSI_E2E_RUN_ID="r5-017a-$(date -u +%Y%m%dT%H%M%SZ)-$$"
scripts/e2e/bootstrap-worker-barrier.sh arm \
  --state-dir "$PWD/deploy/staging-control-plane/barrier-state" \
  --session-id "$OPSI_E2E_BOOTSTRAP_SESSION_ID" \
  --run-id "$OPSI_E2E_RUN_ID"
sudo chown -R 1000:1000 deploy/staging-control-plane/barrier-state
```

Start or recreate only the Bootstrap Worker with the explicit override:

```bash
docker compose --env-file deploy/staging-control-plane/.env \
  -f deploy/staging-control-plane/compose.yaml \
  -f deploy/staging-control-plane/compose.e2e-bootstrap-barrier.yaml \
  up -d --no-deps bootstrap-worker
```

After the marker reports `reached`, record the evidence before restarting:

```bash
sudo scripts/e2e/bootstrap-worker-barrier.sh status \
  --state-dir "$PWD/deploy/staging-control-plane/barrier-state" \
  --session-id "$OPSI_E2E_BOOTSTRAP_SESSION_ID" \
  --run-id "$OPSI_E2E_RUN_ID"
ssh -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
  -o "UserKnownHostsFile=$OPSI_E2E_SSH_KNOWN_HOSTS" -i "$HOME/key/ta.pem" \
  ubuntu@54.254.183.11 'sudo k3s --version'
```

The marker proves the Worker observed successful `install_k3s`; the read-only
K3s version check proves the target mutation. The Cloud/local session record
must still show `checkpoint.next_step_index == 1` and
`last_completed_step == "preflight"`; no `install_k3s` checkpoint or later step
may exist. Do not use a fixed sleep as evidence; poll the marker and the factual
session record.

Restart only the Worker, preserving PostgreSQL, Cloud, and the Agent VPS:

```bash
docker compose --env-file deploy/staging-control-plane/.env \
  -f deploy/staging-control-plane/compose.yaml \
  -f deploy/staging-control-plane/compose.e2e-bootstrap-barrier.yaml \
  up -d --no-deps --force-recreate bootstrap-worker
```

The new Worker must see the `reached` marker, bypass the barrier once, execute
`install_k3s` again, then write its checkpoint. Confirm the replay from Worker
logs and the factual session events, followed by `install_agent`,
`register_agent`, and `ready`. A replay failure must leave the checkpoint at
`preflight`; retry only after the Worker is restarted again. Finally disarm and
remove the run-specific state after collecting redacted evidence:

```bash
sudo scripts/e2e/bootstrap-worker-barrier.sh disarm \
  --state-dir "$PWD/deploy/staging-control-plane/barrier-state" \
  --session-id "$OPSI_E2E_BOOTSTRAP_SESSION_ID" \
  --run-id "$OPSI_E2E_RUN_ID"
```

The logical Host header `r5-017-agent.opsidev.site` does not require public DNS
or Agent TLS proof in this task. When the later harness needs the route, use
`curl --resolve 'r5-017-agent.opsidev.site:80:54.254.183.11' ...`; this keeps the
HTTP Host header while directing traffic to the disposable VPS. Do not claim
public DNS, HTTPS, or Agent TLS acceptance from this barrier proof.

## Limits and status

The full scenario has no accepted real-infrastructure artifact until an
operator runs it against the protected Local API/UI and reviews the redacted
evidence. Do not change `MANUAL_GATED` without that evidence. R5-011 remains
`PARTIAL`; R5-011.4 remains `MANUAL_GATED`. This runbook does not claim R5-012,
MCP, AI, DNS, TLS, or public endpoint acceptance.
