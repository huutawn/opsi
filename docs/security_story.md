# Security Story

Opsi is local-first: Agent owns runtime execution, secrets, telemetry, incidents, and deploy state. Cloud owns identity, membership, bootstrap/agent registration, deployment intent relay, OTP, alerts, and AI proxy boundaries.

Secrets:

- PATs stored in Cloud are bcrypt hashes.
- CLI PAT storage is OS keychain only.
- Raw logs, metrics streams, kubeconfig, app secrets, private keys, and provider API keys must not be sent to Cloud AI.
- Secret reveal requires Owner plus OTP/TOTP.

RBAC:

- Owner/Admin lifecycle actions are enforced in Cloud.
- Developer may deploy/manage services.
- Viewer is read-only.
- Agent mutation RPCs enforce role when auth is enabled.

AI:

- Incident payloads must use `opsi.incident_context.v1`.
- Cloud rejects secret-like/raw-log fields before analysis.
- Fixture/fallback RCA is labeled in response metadata.

Audit:

- Sensitive actions append audit records.
- Postgres production mode uses append-only audit trigger.
- Audit metadata is redacted and scoped by project.

