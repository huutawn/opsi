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
export OPSI_E2E_VPS_HOST=...
export OPSI_E2E_VPS_SSH_USER=root
export OPSI_E2E_SSH_KEY_PATH=/protected/path/to/private-key.pem
export OPSI_E2E_VPS_HOST_KEY_SHA256=SHA256:...
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
3. Agent `PollJob` consumes the job through `ProductionAdapter`/
   `ReconcileRollout`, deploys the immutable OCI digest, and Opsi-owned K3s
   resources become healthy. Evidence includes the BuildRecord, digest, job,
   runtime/node/Agent identity, readiness, events, and audit.
4. The known-bad `OPSI_E2E_BAD_BUILD_RECORD_ID` must fail deployment readiness
   and produce factual failure/incident evidence. The script verifies factual
   incident list/detail/resolve and the `incident.resolve` audit.
5. The script writes redacted evidence and cleanup guidance. It rejects any
   artifact containing a PEM private-key marker or sensitive value.

No Git clone, source SHA, Docker build, arbitrary manifest, caller-selected
authority, service-scoped deployment, or generic webhook relay participates in
this path.

## Limits and status

The full scenario has no accepted real-infrastructure artifact until an
operator runs it against the protected Local API/UI and reviews the redacted
evidence. Do not change `MANUAL_GATED` without that evidence. R5-011 remains
`PARTIAL`; R5-011.4 remains `MANUAL_GATED`. This runbook does not claim R5-012,
MCP, AI, DNS, TLS, or public endpoint acceptance.
