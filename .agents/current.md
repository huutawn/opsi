# Opsi Current Snapshot

Detailed snapshot: `docs/current_state.md`. Architecture: `docs/architecture.md`.

## Repo Shape

- Go workspace with modules: `agent/`, `cli/`, `cloud/`, `contracts/go/`.
- Canonical SRS is `docs/opsi_srs.md` (SRS v4 active production-ready contract); legacy SRS v3.2 is archived under `docs/archive/`.
- Public contracts live under `contracts/`; current Go binding is hand-written JSON gRPC, not generated protobuf.
- Runtime code exists for Phase 1-5 minimum slices; Cloud now serves a static production workflow UI for project/node/service/deploy/topology/audit flows.

## Implemented Runtime

  - Agent:
  - gRPC services: Status, Deployment, ServiceManager, Telemetry, Secret, Incident.
  - HTTP `/health`, TLS 1.3 config, optional client cert verification.
  - SQLite WAL stores for service/deployment/managed service/telemetry/audit state.
  - Deployment supports project/service scoped deploy, safe relative source path containment for build context/Dockerfile/manifest/watch paths, containerd-first builder, Docker fallback, dry-run, progress stream, rollout watch/rollback with post-rollback verification, redacted failure/status messages, and `depends_on` service binding injection.
  - Service binding supports alias/prefix/default env policy for multiple same-type dependencies, deterministic binding checksum annotations, and typed rollout-failure classification before auto rollback.
  - Service catalog supports PostgreSQL/Redis managed runtime plus external service registration; managed PostgreSQL/Redis manifests include probes/resources/security-context basics, and external service registration stores TCP probe status as healthy/unhealthy.
  - Telemetry supports Kubernetes/cAdvisor/kubectl/runtime collectors, pod ready/restart signals, retention, zstd sync chunks, project-scoped sync, redacted query summaries, service health, and cursor-paginated recent logs.
  - Secret vault supports Kubernetes Secret storage via stdin-applied Secret manifests (no secret values in kubectl argv), Cloud PAT verify cache, OTP/TOTP reveal gate, rotation restart, and audit.
  - Incident path supports evidence-backed sanitized RCA context from local telemetry metric/log fingerprint windows, typed mitigation allowlist, stale-action hash guard, post-action verification, resolution, MTTR, and audit.
  - Cloud relay client can poll deployment leases, heartbeat, submit redacted deployment results, and sign Agent requests with HMAC headers for production Cloud guards.
  - CloudRunner can poll Cloud deployment jobs with `DeploymentIntent` v1, execute git-source deploys through the Agent deployment engine using intent-scoped source/runtime/resource/binding fields, reject image-source jobs clearly for P0, report `intent_hash`, retry result reporting, and run with the Agent daemon when `cloud_relay.enabled=true`.

  - CLI:
  - Cobra commands: `status`, `deploy`, `sync`, `service`, `secret`, `incident`, `login`, `start`.
  - Agent gRPC client supports TLS, client cert, and server cert pinning.
  - PAT storage uses OS keychain; tests use fake store.
  - `opsi start` serves `/health`, supports `--dev-ui http://localhost:3000`, serves `cli/ui/out` when built, and returns an honest 503 when no UI build exists.
  - Local secret create/reveal/rotate endpoints are wired Browser -> CLI local backend -> Agent gRPC SecretService -> runtime Secret store. Create/rotate return metadata only; reveal requires explicit intent plus OTP/TOTP and returns `Cache-Control: no-store` with a short TTL. List/delete/secret-audit remain unsupported instead of fake-success.
  - CLI UI is split by route/layout/feature/hook/API/contracts and browser code now calls `/api/local/...` for project/readiness/node/service/deploy/topology/audit/support workflows. `opsi start` proxies Cloud registry calls through the CLI backend using the OS-keychain PAT, exposes `/api/local/session` with a short local session token, requires `X-Local-Session` plus `Idempotency-Key` on mutations, exposes `/api/local/status` for Agent status, exposes `/api/local/projects/{project_id}/telemetry/summary`, `/telemetry/services/{service_id}`, and `/logs` from Agent telemetry query without Cloud/raw payloads, and never returns the PAT to the browser.
  - CLI local deploy submit validates image-source services before Cloud job creation when the service is known locally via the registry read model.
  - Local incident list/detail/analyze/approve/resolve endpoints are wired Browser -> CLI local backend -> Agent gRPC IncidentService. Analyze returns Agent RCA metadata and advisory-only policy; approve requires explicit local session, idempotency key, action approval, and action hash before Agent execution.

- Cloud:
- Local/dev runtime for OTP, PAT verify, fixture AI RCA, and Gemini RCA adapter with explicit fallback metadata.
  - Control-plane registry API exposes org project create/list, project readiness, nodes, bootstrap sessions/events, services, deployment job creation, and deployment rollback request creation with write idempotency/request headers, RBAC, audit, bootstrap expiry cleanup, and machine-readable readiness errors.
  - Registry read models expose project-scoped services, deployments with plan/manifest/rollback metadata, bootstrap sessions, deployment events, and audit events for UI refresh/reconnect without mock topology data.
  - Registry security gate enforces owner/admin-only node/bootstrap/agent lifecycle actions, developer deploy/service actions, RBAC denial audit, bootstrap credential TTL/read-once storage, worker-only credential take, one-time agent registration token exchange, bcrypt-hashed agent bearer credentials, revoked/rotated agent poll blocking, redacted bootstrap events/audit metadata, bootstrap/deployment/agent-registration rate limits, and agent register/rotate/revoke records.
  - `opsi-bootstrap-worker` now has a narrow real bootstrap path for Ubuntu first-server targets over SSH password auth: it takes the one-time credential, connects over SSH, preflights Ubuntu/curl/systemd/sudo, installs K3s server, downloads the configured Opsi Agent binary, registers the Agent from the target, writes Agent cloud-relay config, starts systemd, waits for Cloud to observe healthy Agent heartbeat (`verifying`), then marks the session completed. Cloud rejects SSH private-key upload. Worker-node join, non-Ubuntu targets, real VPS e2e evidence, and signed/checksummed Agent artifact release proof remain gaps.
  - Node/K3s/Agent lifecycle supports first-server vs worker bootstrap gating, active-host/idempotency protection, agent heartbeat/inventory readiness reconciliation, and node diagnostics. Drain/remove endpoints now block honestly unless/until Agent-backed K3s execution is wired; only-server removal precondition is still enforced before the blocked response.
  - Deployment release flow enforces server-side service deploy prerequisites, concrete Git revision/build-context/dockerfile/manifest validation, early image-source rejection, deploy-capable Agent readiness, deterministic deployment/manifest/intent hashes, service-specific runtime/resource/binding intent fields, previous revision refs, versioned `DeploymentIntent` job envelopes, redacted deployment events, audit, idempotency, Agent job lease/result contract with lease tokens, expiry retry, dead-letter state, terminal lock release, and expiring service-level deployment locks.
  - Production Cloud config requires Postgres, a strong bootstrap worker token, a strong bootstrap secret key, and Agent HMAC request signatures; it rejects debug UI, OTP dev echo, and non-HTTPS public URLs. With Postgres it uses AES-GCM encrypted bootstrap credential/registration storage, DB-backed rate limits, and DB triggers that make `cloud_audit_events` append-only.
  - Postgres migration and runtime registry repository include org/project/environment/runtime/node/agent/bootstrap/service/deployment/audit/idempotency control-plane schema when `database_url` is configured; dev default uses in-memory registry store.
  - Webhook relay queue uses Postgres `relay_jobs`/`relay_events` when `database_url` is configured and keeps only sanitized envelope metadata plus changed paths, body hash, idempotency key, status/attempt timestamps, and redacted errors. Raw webhook body is not persisted or delivered. In-memory webhook queue remains dev/test only.
  - OTP supports hashed code, TTL, one-time verify, rate limit, optional Postgres, SMTP/outbox, and dev echo.
  - PAT verify uses bcrypt hash + project membership role when Postgres is configured.
  - Static Cloud UI at `/` uses real registry APIs for project list/detail readiness, add-server bootstrap with reconnect-safe timeline, node diagnostics/actions, service drafts/detail, deploy queueing with redacted event timeline, topology from real nodes/services/deployments, and redacted audit.
  - Cloud exposes request-ID echoing, Prometheus-style process/domain metrics at `/metrics`, project support summaries at `/api/projects/:projectID/support`, webhook/outbox alert routing, and deployable Prometheus/Alertmanager/Grafana provisioning artifacts. Support summaries include Grafana-style dashboard panels, SLO signals, configured alerts, active alerts, production gates, break-glass policy, runbooks, redacted support context, and recent deployment request IDs. CLI UI maps Metrics/Support to that real support dashboard. Cloud inline UI is now debug-only behind `enable_debug_ui=true`.
- Cloud AI config is explicit (`fixture|gemini`), Gemini RCA calls use sanitized incident context only, fixture fallback is explicit, AI payloads reject raw-log/secret-like keys before analysis, and Agent RCA validation requires visible provider/model/fallback/input-context-hash metadata before storing or returning analysis.
  - Root README documents clean-checkout verification, Go `GOTOOLCHAIN=local`, module test commands, optional `` wrapper fallback, and offline cache expectations. Root Makefile exposes `verify`, `test`, `build`, `clean`, and `package-source`; `verify` covers Go vet/tests plus UI `npm ci`, build/typecheck, and lint; builds binaries into `bin/`, injects a shared build SHA into `opsi version`, `opsi-agent --version`, and `opsi-cloud --version`, and creates a `release/` artifact layout with checksums and demo docs.
  - Runtime security hardening includes Agent production/non-loopback config fail-fast, Cloud production OTP/dev-echo and HTTPS public URL guards, Agent request HMAC compatibility, omitted OTP `code` outside dev echo, secret reveal second-factor negative tests, AI raw-log/secret-like payload rejection tests, Incident action hash/post-action verification, and a proto-vs-handwritten-Go service/RPC drift test.
  - Backup/restore has a tested DR proof for the implemented metadata slice: `scripts/opsi-backup.sh`, `scripts/opsi-restore.sh`, and `make verify-dr` cover Cloud Postgres project/node/agent/service/deployment/idempotency/relay/audit state plus Agent deploy/service-catalog/telemetry incident-audit-uptime SQLite metadata. Raw logs/raw metrics and forbidden plaintext sentinel values are excluded from tested artifacts.

## Known Gaps

- OAuth login/PAT issuance UI not implemented.
- Cloud webhook relay is durable with Postgres when `database_url` is configured; database-backed relay notification/outbox delivery beyond webhook/deployment relay remains future work.
- Cloud AI provider schema is still minimal; Gemini output is currently consumed as root-cause text while recommended actions remain typed fixture actions.
- CLI UI WebSocket/SSE bridge and incident registry endpoint are not implemented; unavailable actions are disabled instead of fake-success.
- Bootstrap worker has a narrow SSH/password Ubuntu first-server runtime installer, but clean VPS Add Server remains blocked for production claims until manual/e2e evidence, signed/checksummed Agent artifact release proof, worker-node join, and HA topology coverage exist.
- Agent-backed K3s drain/remove now exists through typed Cloud `node_lifecycle` job envelopes leased by Agent. Agent runs allowlisted `kubectl` cordon/drain/delete-node operations, verifies before reporting `completed`, and Cloud only updates node status from verified Agent results. Missing Agent, invalid target, K3s failure, timeout, unverified result, unsupported action, or missing remove intent fail closed with redacted errors. Real K3s worker-node manual/e2e evidence is still missing, so K3s node lifecycle remains `Partial`, not production-ready.
- K3s encryption-at-rest is config-gated, not auto-detected.
- Service catalog lacks DB-native rotation, service logs, backup/restore workflows for managed services, and full managed-service readiness reconciliation.
- DR still excludes Cloud encryption key material, K3s datastore/manifests, managed service user data, migration rollback, release-note DR evidence, and timed real-environment RTO.
- HA server topology and heartbeat timeout reconciler remain future work.
- Plan 06 support dashboard and external observability provisioning exist; HA server topology, heartbeat timeout reconciler, and provider-specific Slack/PagerDuty adapters remain future work.
- Local Web UI still needs Agent-owned secret list/delete/secret-audit and runtime audit merge; missing Agent-owned local endpoints return typed unsupported errors instead of fake success.
- Source hygiene gate passes after `make clean`; generated binaries/UI outputs/DB files are no longer required for verification.

## Commands

Run from module dirs, not repo root:

```bash
 go test ./...      # agent/
 go test ./...      # cli/
 go test ./...      # cloud/
 go test ./...      # contracts/go/
```

Last checked for P1 hardening: agent 80 passed, cloud 26 passed, cli 21 passed, contracts/go 1 passed.
