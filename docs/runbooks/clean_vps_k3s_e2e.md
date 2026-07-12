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

Opsi currently has no public `IncidentEvidence v1`, Safe ActionPlane, CLI MCP
bridge, or managed gateway. This runbook does not claim those target flows.

The command path exists, but no committed real-infrastructure pass artifact
currently proves the complete scenario. Do not change `MANUAL_GATED` to `DONE`
until a maintainer runs the protected workflow, reviews the redacted output, and
commits or otherwise records the accepted artifact.

Artifacts are written under `.tmp/e2e-k3s/<run-id>/`. Raw kubeconfig, SSH
password, PAT, OTP/TOTP, app secret values, and raw logs must not be stored.

Not currently proven: worker-node join/HA, Opsi-managed gateway/TLS, future
evidence/action/MCP flow, release checksum distribution, full DR, or production
acceptance.
