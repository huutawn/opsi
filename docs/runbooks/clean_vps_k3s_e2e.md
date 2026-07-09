# Clean VPS/K3s E2E Proof

Canonical commands:

```bash
make verify-e2e-k3s-preflight
make verify-e2e-k3s
```

Supported target: a clean Ubuntu 22.04/24.04 VPS reachable by SSH password auth. The path uses the CLI local backend, Cloud metadata/job envelopes, bootstrap worker, Agent, and real K3s. It does not use fake adapters or Agent dry-run.

Required tools: `bash`, `curl`, `python3`, `ssh`, `sshpass`, `go`, `node`, `npm`, `kubectl`.

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
export OPSI_E2E_APPROVE_MITIGATION=YES
```

Optional incident trigger:

```bash
export OPSI_E2E_BAD_SERVICE_SHA=...
```

Covered steps: local session auth, Add Server bootstrap, K3s install verification, Agent heartbeat/readiness, Git service deploy, K3s rollout/runtime verification, Agent-backed secret create/rotate/reveal gate, Agent-backed telemetry/log fetch, incident list/analyze, explicit approved allowlisted mitigation, audit fetch, sanitized artifacts, cleanup instructions.

Not covered until a maintainer runs the full command against real infra: worker-node join, HA/server removal, DR, release checksum distribution proof, provider OAuth setup.

Artifacts: `.tmp/e2e-k3s/<run-id>/`. Files are redacted; raw kubeconfig, SSH password, PAT, OTP/TOTP, app secret values, and raw logs are not stored as artifacts.
