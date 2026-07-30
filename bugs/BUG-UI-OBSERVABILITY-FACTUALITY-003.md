# BUG-UI-OBSERVABILITY-FACTUALITY-003

## Defects

At the FE-04 starting revision:

1. Missing restart and recent-error counters are rendered with a healthy badge because absent values are coerced to zero.
2. Incident handoff displays nonexistent `opsi incident preflight`, `opsi incident approve`, and `opsi incident execute` commands.
3. Health correlation accepts `service.name` as a telemetry identity without proving it is the canonical service key.
4. Incident evidence validation accepts any nonempty content hash and does not bound or structurally validate nested evidence collections.

## Required factual behavior

- Missing counters are `Unknown`; only a factually reported zero is healthy.
- Telemetry joins only on authoritative service identity. Unmatched telemetry is shown as `Unresolved identity` and is never guessed onto a service.
- Incident handoff uses the factual `opsi action` preflight, approval, and execution flow with explicit challenge/device placeholders.
- Evidence requires bounded nested structures, valid timestamps and coverage entries, and a lowercase 64-character SHA-256 value. Malformed evidence fails closed without partially rendering trusted-looking content.

## Status

`FIXED / FE-04`

Missing counters now render `Unknown`, telemetry joins require the authoritative service ID, malformed evidence fails closed, and incident handoff uses only the real `opsi action` CLI flow. Focused regressions and Playwright fixture acceptance pass.
