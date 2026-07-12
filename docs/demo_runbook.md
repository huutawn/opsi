# Demo Runbook

Status: active factual demo guide; not a production-readiness artifact.

## Clean setup

1. Build artifacts with `make ui-build`, `make release`, and
   `make smoke-release`.
2. Start Cloud in development mode only with an explicitly sanitized local
   configuration.
3. Start Agent with local development configuration.
4. Start the Local UI through `opsi start`.

Local configuration files and credentials are operator-created runtime inputs;
they are not tracked source artifacts.

## Happy path

1. Create or open a project.
2. Register the first server/node.
3. Register a Git-backed service.
4. Queue a deployment.
5. Confirm Agent leases the job and reports a terminal runtime result.
6. Inspect telemetry/log summaries and redacted audit metadata.

## Failure and incident path

1. Queue a controlled failing deployment revision.
2. Confirm failure or deploy-time rollback behavior from factual deployment
   state.
3. List incidents and open incident detail.
4. Resolve the incident and verify resolve audit.

Do not demonstrate or claim AI analysis, fallback RCA, recommended mitigation,
incident action approval, managed gateway behavior, MCP, or Safe ActionPlane.
Those capabilities are not implemented at M0.

## Honest fallbacks

- If Cloud is unavailable, only workflows that do not require fresh Cloud
  identity/OTP/relay may continue locally.
- If the UI build is missing, `opsi start` returns an honest unavailable response
  or may proxy an explicitly configured development UI.
- If Agent is unavailable, runtime operations fail unavailable; there is no shell
  or SSH execution fallback.

For real infrastructure use `docs/runbooks/clean_vps_k3s_e2e.md`. Its status is
`MANUAL_GATED` because no committed full-pass artifact exists.
