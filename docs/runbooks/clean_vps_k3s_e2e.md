# Clean VPS/K3s E2E Proof

Status: active runbook; `MANUAL_GATED`.

Canonical commands:

```bash
make verify-e2e-k3s-preflight
make verify-e2e-k3s
```

Supported target: a clean Ubuntu 22.04/24.04 VPS reachable by SSH password
authentication. The path uses the CLI local backend, Cloud metadata/job
envelopes, Bootstrap Worker, Agent, and real K3s. It does not use Agent dry-run
or fake runtime adapters.

Required tools: `bash`, `curl`, `python3`, `ssh`, `sshpass`, `go`, `node`, `npm`,
and `kubectl`.

Required environment:

```bash
export OPSI_E2E_LOCAL_URL=http://127.0.0.1:9780
export OPSI_E2E_PROJECT_ID=...
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=root
export OPSI_E2E_VPS_SSH_PASSWORD=...
export OPSI_E2E_SERVICE_REPO=https://github.com/.../opsi.git
export OPSI_E2E_SERVICE_SHA=...
export OPSI_E2E_TOTP_CODE=...
```

## Manual CLI And Local UI Flow

The manual CLI and Local UI create the same Cloud bootstrap session through the
same versioned request shape. The CLI keeps the SSH credential in a protected
file (or `/dev/stdin`); secret values must not be passed as flags, logged, or
stored in the evidence directory:

```bash
opsi --config "$OPSI_E2E_CLI_CONFIG" server bootstrap \
  --project-id "$OPSI_E2E_PROJECT_ID" \
  --role first_server \
  --public-host "$OPSI_E2E_VPS_HOST" \
  --ssh-username "$OPSI_E2E_VPS_SSH_USER" \
  --auth-method private_key \
  --credential-file "$OPSI_E2E_SSH_KEY_PATH"
opsi --config "$OPSI_E2E_CLI_CONFIG" node status \
  --project-id "$OPSI_E2E_PROJECT_ID"
opsi --config "$OPSI_E2E_CLI_CONFIG" node events \
  --project-id "$OPSI_E2E_PROJECT_ID" --session-id "$OPSI_E2E_SESSION_ID"
```

The Local UI submits only to the CLI local backend at
`/api/local/projects/<project>/bootstrap-sessions`; the backend maps that
request to Cloud's `/api/projects/<project>/bootstrap-sessions` endpoint. The
UI shows the durable checkpoint, attempt count, redacted failure, and event
stream. Browser code must not call Cloud directly.

If a PAT is provisioned through the first-owner runbook, store it without
putting its value in argv:

```bash
opsi login --pat-file /secure/path/initial-owner.pat
```

Optional controlled failure revision:

```bash
export OPSI_E2E_BAD_SERVICE_SHA=...
```

## Active scenario

The current script covers local session authentication, Add Server bootstrap,
K3s/Agent readiness, Git deployment, runtime rollout checks, secret
create/rotate/reveal gates, telemetry/log queries, deployment audit, and a
factual incident lifecycle:

1. list incidents;
2. get incident detail;
3. resolve the incident;
4. verify `incident.resolve` audit.

The incident proof contains no AI analysis, root-cause result, recommended
action, mitigation approval, action hash, or fallback provider behavior.

## Current limitations

R5-004 exercised the bootstrap subset on 2026-07-17 through the final CLI,
Cloud bootstrap session, Bootstrap Worker strict SSH, pinned K3s installer, and
checksum-addressed Agent artifact. The same session/node reached completed and
healthy state, Local API/UI read the same timeline, and K3s plus Agent recovered
after a controlled VPS reboot. A live Worker restart between destructive steps
was not attempted because there is no safe production fault-injection hook for
the completed healthy node.

Opsi currently has no public `IncidentEvidence v1`, Safe ActionPlane, CLI MCP
bridge, or managed gateway. This runbook does not claim those target flows.

The command path exists, but no committed real-infrastructure pass artifact
currently proves the complete scenario. Do not change `MANUAL_GATED` to `DONE`
until a maintainer runs the protected workflow, reviews the redacted output, and
commits or otherwise records the accepted artifact.

For R5-004C, run the direct-Agent gate before any target reset: the product CLI
must be able to read the PAT from its OS keychain, resolve the Cloud-provided
`{"nodes":[...]}` metadata, save the TLS pin atomically, and return direct
TLS-pinned Agent status. If the OS keychain cannot complete the login/read
operation, stop `PARTIAL`; do not reset the Agent VPS or attempt the Worker
restart gate.

Artifacts are written under `.tmp/e2e-k3s/<run-id>/`. Raw kubeconfig, SSH
password, PAT, OTP/TOTP, app secret values, and raw logs must not be stored.

Not currently proven: worker-node join/HA, Opsi-managed gateway/TLS, future
evidence/action/MCP flow, release checksum distribution, full DR, or production
acceptance.
