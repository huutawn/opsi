# Demo Runbook

## Clean Setup

1. Build artifacts:
   ```bash
   make ui-build
   make release
   make smoke-release
   ```
2. Start Cloud in dev mode with debug UI only if needed:
   ```bash
   ./release/opsi-cloud --addr 127.0.0.1:9800 --config cloud.dev.json
   ```
3. Start Agent with local/dev config:
   ```bash
   ./release/opsi-agent --config agent.dev.yaml
   ```
4. Start local UI:
   ```bash
   ./release/opsi start --addr 127.0.0.1:9780 --config cli.dev.yaml
   ```

## Happy Path

1. Create/open a project.
2. Register first server/node.
3. Register a Git-backed service.
4. Queue deployment.
5. Confirm Agent leases job, reports result, Cloud deployment becomes terminal.
6. Open support/metrics and audit screens; verify request IDs and redaction.

## Failure Path

1. Queue a bad deployment revision.
2. Confirm deployment fails terminally or rolls back.
3. Open incident/RCA flow.
4. Verify RCA metadata shows fixture/fallback when no provider is configured.

## Fallbacks

- Cloud unavailable: local Agent/CLI commands still expose status and local runtime paths.
- UI build stale/missing: CLI binary serves embedded `cli/ui/out`; devs may use `opsi start --dev-ui http://localhost:3000`.
- Gemini/API unavailable: fixture RCA is explicit via response metadata; do not claim provider mode.

