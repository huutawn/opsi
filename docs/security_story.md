# Opsi Security Story

Status: active boundary summary, last updated 2026-07-15. Detailed requirements
are in `docs/opsi_srs.md`; implementation status is in
`docs/status_matrix.md`; trusted artifact target architecture is in
`docs/architecture_decisions/ADR-004-trusted-artifact-cd.md`.

## Current trust model

Opsi is local-first. Agent owns runtime execution, secrets, telemetry, factual
incidents, and local audit. CLI local backend owns the Browser mediation boundary
and OS-keychain PAT access. Cloud owns identity, membership, registration,
bootstrap/deployment envelopes, OTP, and durable control-plane metadata.

Cloud has no AI runtime, model/provider integration, prompt path, or RCA fallback.
Agent has no AI analyzer or RCA-backed executor. Historical RCA/mitigation data
is storage-only and is never execution authority.

Cloud currently has generic browser OAuth mediation and a webhook route with
per-route SHA-256 HMAC verification. These are not evidence of GitHub App user
authorization, App installation authentication, installation/repository event
ownership, or GitHub Actions OIDC. Agent currently supports Git-source
clone/build deployment and rejects image-source deployment.

## Credentials and secrets

- Cloud stores PATs as bcrypt hashes; the CLI stores the usable PAT in the OS
  keychain. The Browser must not receive a long-lived PAT.
- Secret reveal requires Owner plus OTP/TOTP and a trusted local path.
- PATs, OTP/TOTP material, Agent tokens, device private keys, kubeconfig,
  application secrets, and approval grants must not appear in logs, audit,
  Cloud runtime metadata, MCP output, or AI context.
- Secret values are supplied to Kubernetes through stdin/API data, not process
  command arguments.
- The local `opsi-cloud admin bootstrap-owner` command accepts no raw PAT flag.
  It generates the initial PAT with CSPRNG, stores only the existing bcrypt hash
  format, and writes plaintext once to a non-existing operator-selected file
  created with mode `0600`. Exact repeats never issue or reconstruct the PAT.

## Credential incident status

R5-001 identified that the former canonical `package-source` target archived the
working-directory `.` directly. Its exclusion list did not cover runtime
environment files, secret directories, or private-key PEM material. The current
source-package path instead uses Git tracked plus untracked/non-ignored
candidates, validates paths and private-key markers before publishing the
artifact, and validates the completed archive again.

Packaging containment does not revoke credentials already present in an older
archive. The incident remains `OPERATOR_REQUIRED` until the credential classes
in `docs/runbooks/credential-incident.md` are rotated or revoked, verified, and
the repository owner records a Git history cleanup decision. No external
credential rotation or history rewrite is performed by R5-001.

## Authorization and audit

- Every operation is project-scoped. Owner/Administrator lifecycle actions,
  Developer deployment/service actions, and Viewer read-only access are enforced
  at the owning boundary.
- Sensitive actions and denials write redacted audit records. The Postgres Cloud
  path uses append-only protections for control-plane audit.
- First-owner provisioning takes a PostgreSQL transaction-scoped advisory lock,
  writes a durable singleton marker, and atomically persists identity,
  memberships, linkage, project defaults, and redacted local-admin audit.
  Conflicting bootstrap identities and OAuth subjects fail closed.
- Retryable mutations require request identity/idempotency; authorization must
  not be inferred from user-supplied role text alone when auth is enabled.

## Trusted artifact delivery target

This section is target architecture and is not implemented at the current
snapshot.

- GitHub App user authorization uses App Client ID/Secret, state, and PKCE to
  bind an Opsi identity to a GitHub numeric user ID.
- GitHub App installation authorization uses App ID/private key and short-lived
  installation tokens for mapped installation/repository metadata or configured
  status/check operations.
- GitHub webhooks require a valid per-App signature plus validated event and
  delivery identity. Webhook verification does not prove build identity.
- GitHub Actions authenticates to the `BuildRecord` boundary with a short-lived
  OIDC JWT. Cloud validates JWKS/signature, issuer, audience, expiry,
  not-before, repository/owner IDs, ref, SHA, event, run ID/attempt, workflow,
  and `job_workflow_ref`.
- Cloud binds every security-relevant `BuildRecord` body value to the verified
  OIDC claims, configured repository/service mapping, workflow/event/ref policy,
  and registry repository allowlist. JSON body values alone are untrusted.
- OIDC replay protection and repository/run/attempt idempotency fail closed.
- The authoritative production runtime artifact is the immutable
  `registry/repository@sha256:<digest>`. Mutable tags, including `latest`, are
  prohibited as production deployment identity.
- GitHub runner registry push authority and Agent registry pull authority are
  separate least-privilege credentials. Neither is a GitHub OAuth credential;
  an installation token is not a long-lived Agent pull credential.
- Same-repository pull requests may build. Preview deployment requires policy,
  isolation, no production credentials, and TTL cleanup. Fork pull requests
  fail closed by default and untrusted fork code receives no write token or
  production secret.
- `DeploymentPolicy` is configured in advance by an authorized user. An allowed
  trusted branch deployment does not require human approval per run. Trusted CD
  is not an AI action, AI cannot approve it, and automatic rollback remains
  inside the already authorized deployment transaction.

## Future user-owned AI boundary

The planned AI bridge is local and user-owned through `opsi mcp serve`. It is not
implemented at M0.

- MCP returns bounded, structured, redacted evidence and excludes all credentials
  and secret values.
- Application output, logs, commit messages, events, labels, and AI output are
  untrusted. They must be tagged, redacted, bounded, and isolated from policy and
  approval instructions.
- AI must not connect directly to Agent, approve an action, receive an
  ApprovalGrant, or invoke an execute tool.
- Human approval occurs outside the AI/MCP channel through the trusted Local UI
  or interactive CLI.
- Agent owns deterministic policy, risk classification, preflight, grant
  verification, locks, typed allowlisted execution, post-check, and audit.
- R4 operations are forbidden. Free-form shell/kubectl, arbitrary SQL,
  `kube-system` mutation, K3s uninstall, host deletion, credential export,
  firewall/package mutation, database mutation, and autonomous destructive
  remediation are not made valid by approval.

## Data minimization

Cloud may store bounded `BuildRecord` metadata, repository ID, commit SHA, image
digest, workflow/run identifiers, deployment result metadata, and provenance
references. Cloud must not persist source repository contents, Docker build
context, raw build logs, raw runtime logs, raw metric streams, app secret values,
registry password plaintext, kubeconfig, or unrestricted manifests. Future
`IncidentEvidence v1` remains Agent-owned and contains only bounded facts,
redacted excerpts, hashes, and sanitization/prompt-injection metadata.

## Current security limitations

Production readiness remains unproven. Missing evidence includes the complete
clean VPS pass, GitHub App/OIDC trust, digest artifact delivery, managed gateway
security, public evidence API, Safe ActionPlane, CLI MCP hardening, release
provenance, and repeated recovery/acceptance runs. P01 clean control-plane VPS
checkpoint `CP-VPS-1` is `DEFERRED / UNPROVEN`; no pass evidence exists.
