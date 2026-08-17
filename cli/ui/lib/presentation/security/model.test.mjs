import assert from "node:assert/strict";
import test from "node:test";
import {
  categorizeAuditEvent,
  classifyActor,
  classifyResult,
  deriveAuditRow,
  deriveSecuritySummary,
  isHighImpactAction,
  isSensitiveKey,
  mapSecurityError,
  redactSensitiveString,
  safeAuditMetadata,
} from "./model.ts";

test("actor type formatting classifies human, machine, agent, worker, github_actions, and unknown actors accurately", () => {
  const human = classifyActor("human", "alice");
  assert.equal(human.kind, "human");
  assert.equal(human.label, "Human actor");
  assert.equal(human.identifier, "alice");
  assert.equal(human.isHuman, true);
  assert.equal(human.isMachine, false);

  const agent = classifyActor("agent", "node-1");
  assert.equal(agent.kind, "agent");
  assert.equal(agent.label, "Agent");
  assert.equal(agent.isMachine, true);
  assert.equal(agent.isHuman, false);

  const worker = classifyActor("worker", "worker-7");
  assert.equal(worker.kind, "worker");
  assert.equal(worker.label, "Worker automation");
  assert.equal(worker.isMachine, true);

  const actions = classifyActor("github_actions", "");
  assert.equal(actions.kind, "github_actions");
  assert.equal(actions.label, "GitHub Actions");
  assert.equal(actions.isMachine, true);

  const system = classifyActor("system", "");
  assert.equal(system.kind, "machine");
  assert.equal(system.label, "Machine actor");
  assert.equal(system.isMachine, true);

  // Unknown actor without identifier does not fabricate "System"
  const unknown = classifyActor("", "");
  assert.equal(unknown.kind, "unknown");
  assert.equal(unknown.label, "Unknown actor");
  assert.equal(unknown.isMachine, false);
  assert.equal(unknown.isHuman, false);
});

test("event category presentation maps factual events to correct domains", () => {
  assert.equal(categorizeAuditEvent("AUTH_LOGIN_SUCCESS").category, "access");
  assert.equal(categorizeAuditEvent("RBAC_DENIED").category, "access");
  assert.equal(categorizeAuditEvent("PAT_ROTATED").category, "access");
  assert.equal(categorizeAuditEvent("BOOTSTRAP_SESSION_CREATED").category, "server");
  assert.equal(categorizeAuditEvent("NODE_MARKED_OFFLINE").category, "server");
  assert.equal(categorizeAuditEvent("BUILD_RECORD_FINALIZED").category, "build");
  assert.equal(categorizeAuditEvent("DEPLOYMENT_ROLLBACK_STARTED").category, "deployment");
  assert.equal(categorizeAuditEvent("RESOURCE_CREATED").category, "managed_resource");
  assert.equal(categorizeAuditEvent("RESOURCE_BINDING_REVOKED").category, "managed_resource");
  assert.equal(categorizeAuditEvent("BACKUP_REQUESTED").category, "dr_backup_restore");
  assert.equal(categorizeAuditEvent("RESTORE_SUCCEEDED").category, "dr_backup_restore");
  assert.equal(categorizeAuditEvent("CUTOVER_FINALIZE_REQUESTED").category, "cutover");
  assert.equal(categorizeAuditEvent("CUTOVER_FINALIZED").category, "cutover");
  assert.equal(categorizeAuditEvent("RETAINED_STORAGE_DESTROYED").category, "storage");
  assert.equal(categorizeAuditEvent("SECRET_REVEALED").category, "security");
});

test("event outcome distinguishes requested, succeeded, failed, and denied accurately", () => {
  assert.equal(classifyResult("success", "CUTOVER_FINALIZED"), "succeeded");
  assert.equal(classifyResult("success", "CUTOVER_FINALIZE_REQUESTED"), "requested");
  assert.equal(classifyResult("pending", "DEPLOYMENT_ROLLBACK_STARTED"), "requested");
  assert.equal(classifyResult("failure", "BACKUP_FAILED"), "failed");
  assert.equal(classifyResult("denied", "RBAC_DENIED"), "denied");
  assert.equal(classifyResult("rejected", "NODE_LIFECYCLE_REQUEST_REJECTED"), "denied");
});

test("high-impact operations identify destructive and security-sensitive events", () => {
  const highImpactCases = [
    "SERVER_REMOVED",
    "NODE_LIFECYCLE_REQUESTED",
    "NODE_MARKED_OFFLINE",
    "RESOURCE_DELETED",
    "RESOURCE_BINDING_REVOKED",
    "RETAINED_STORAGE_DESTROY_REQUESTED",
    "RETAINED_STORAGE_DESTROYED",
    "CUTOVER_FINALIZE_REQUESTED",
    "CUTOVER_FINALIZED",
    "CUTOVER_ROLLBACK_REQUESTED",
    "DEPLOYMENT_ROLLBACK_STARTED",
    "BOOTSTRAP_MANUAL_RETRY_REQUESTED",
    "PAT_REVOKED",
    "AGENT_REVOKED",
  ];

  for (const action of highImpactCases) {
    const check = isHighImpactAction(action);
    assert.equal(check.highImpact, true, `expected ${action} to be high impact`);
    assert.ok(check.reason.length > 0);
  }

  // Normal read / create operations are not high-impact
  assert.equal(isHighImpactAction("SERVICE_CREATED").highImpact, false);
  assert.equal(isHighImpactAction("BUILD_RECORD_FINALIZED").highImpact, false);
  assert.equal(isHighImpactAction("TOPOLOGY_VIEW").highImpact, false);
});

test("secret exclusion drops nested sensitive keys and credential URLs while preserving safe IDs and digests", () => {
  // Test detection of sensitive keys
  for (const key of [
    "password",
    "DATABASE_PASSWORD",
    "secret",
    "token",
    "authorization",
    "cookie",
    "bearer",
    "private_key",
    "privateKey",
    "database_url",
    "registry_auth",
    "registry_password",
    "lease_token",
    "review_token",
    "cutover_token",
    "ssh_password",
    "ssh_private_key",
    "kubeconfig",
    "app_secret",
    "pat",
  ]) {
    assert.equal(isSensitiveKey(key), true, `expected ${key} to be identified as sensitive`);
  }

  // Test safe keys are NOT marked as sensitive
  for (const safeKey of [
    "build_job_id",
    "attempt_id",
    "repository",
    "digest",
    "oci_digest",
    "sha",
    "pvc_uid",
    "pvc_name",
    "pv_name",
    "pv_uid",
    "request_id",
    "correlation_id",
    "evidence_hash",
    "reason",
    "action",
    "status",
    "role",
    "auth_method",
    "node_id",
    "service_id",
    "runtime_id",
    "environment_id",
    "revision",
  ]) {
    assert.equal(isSensitiveKey(safeKey), false, `expected ${safeKey} to be safe`);
  }

  // Test URL credential redaction
  const postgresURL = "postgres://opsi_user:super_secret_pw@db.internal:5432/production_db";
  const sanitizedURL = redactSensitiveString(postgresURL);
  assert.doesNotMatch(sanitizedURL, /super_secret_pw/);
  assert.match(sanitizedURL, /postgres:\/\/opsi_user:\[REDACTED\]@db\.internal:5432\/production_db/);

  // Test private key redaction
  const privateKey = ["-----BEGIN", "OPENSSH PRIVATE KEY-----"].join(" ") + "\nb3BzaS1zZWNyZXQ=\n" + ["-----END", "OPENSSH PRIVATE KEY-----"].join(" ");
  const sanitizedKey = redactSensitiveString(privateKey);
  assert.equal(sanitizedKey, "[PRIVATE KEY REDACTED]");

  // Test rich nested payload
  const nestedPayload = {
    build_job_id: "bj-101",
    repository: "opsi-dev/demo",
    digest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    pvc_uid: "pvc-uid-456",
    evidence_hash: "a1b2c3d4e5f6",
    request_id: "req-999",
    DATABASE_PASSWORD: "secret_db_password_never_show",
    database_url: "postgres://user:db_pw@localhost/db",
    registry_password: "docker_hub_token",
    token: "ghp_1234567890abcdef",
    authorization: "Bearer secret-token",
    cookie: "session=xyz123",
    private_key: "fake-rsa-private-key-material-for-test",
    database_url: "postgres://user:secret@db.internal:5432/db",
    lease_token: "lease-secret-token-123",
    cutover_token: "cutover-123",
    nested_config: {
      safe_param: "value_1",
      secret_key: "hidden_value",
      inner: {
        password: "nested_password",
        node_id: "node-55",
      },
    },
  };

  const safeEntries = safeAuditMetadata(nestedPayload);
  const metadataMap = Object.fromEntries(safeEntries);

  // Assert sensitive keys are absent
  assert.equal(metadataMap.DATABASE_PASSWORD, undefined);
  assert.equal(metadataMap.database_url, undefined);
  assert.equal(metadataMap.registry_password, undefined);
  assert.equal(metadataMap.token, undefined);
  assert.equal(metadataMap.authorization, undefined);
  assert.equal(metadataMap.cookie, undefined);
  assert.equal(metadataMap.private_key, undefined);
  assert.equal(metadataMap.lease_token, undefined);
  assert.equal(metadataMap.review_token, undefined);
  assert.equal(metadataMap.cutover_token, undefined);

  // Assert safe fields are present and intact
  assert.equal(metadataMap.build_job_id, "bj-101");
  assert.equal(metadataMap.repository, "opsi-dev/demo");
  assert.equal(metadataMap.digest, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
  assert.equal(metadataMap.pvc_uid, "pvc-uid-456");
  assert.equal(metadataMap.evidence_hash, "a1b2c3d4e5f6");
  assert.equal(metadataMap.request_id, "req-999");

  // Assert nested object has filtered out inner sensitive keys
  assert.ok(metadataMap.nested_config);
  assert.doesNotMatch(metadataMap.nested_config, /hidden_value|nested_password/);
  assert.match(metadataMap.nested_config, /value_1/);
  assert.match(metadataMap.nested_config, /node-55/);
});

test("factual event sequences preserve independent authorities without incorrect collapsing", () => {
  const cutoverSequence = [
    { id: "aud-1", actor_type: "human", actor_user_id: "admin", action: "CUTOVER_REVIEW_REQUESTED", resource_type: "cutover_review", resource_id: "cr-1", result: "success", created_at: "2026-08-01T10:00:00Z" },
    { id: "aud-2", actor_type: "worker", action: "CUTOVER_REVIEW_SUCCEEDED", resource_type: "cutover_review", resource_id: "cr-1", result: "success", created_at: "2026-08-01T10:01:00Z" },
    { id: "aud-3", actor_type: "human", actor_user_id: "admin", action: "CUTOVER_REQUESTED", resource_type: "cutover", resource_id: "cut-1", result: "success", created_at: "2026-08-01T10:02:00Z" },
    { id: "aud-4", actor_type: "worker", action: "CUTOVER_SUCCEEDED", resource_type: "cutover", resource_id: "cut-1", result: "success", created_at: "2026-08-01T10:05:00Z" },
    { id: "aud-5", actor_type: "human", actor_user_id: "admin", action: "CUTOVER_FINALIZE_REQUESTED", resource_type: "cutover_finalization", resource_id: "fin-1", result: "success", created_at: "2026-08-01T10:10:00Z" },
    { id: "aud-6", actor_type: "worker", action: "CUTOVER_FINALIZED", resource_type: "cutover_finalization", resource_id: "fin-1", result: "success", created_at: "2026-08-01T10:12:00Z" },
  ];

  const rows = cutoverSequence.map(deriveAuditRow);
  assert.equal(rows.length, 6);
  assert.equal(rows[0].outcome, "requested");
  assert.equal(rows[1].outcome, "succeeded");
  assert.equal(rows[2].outcome, "requested");
  assert.equal(rows[3].outcome, "succeeded");
  assert.equal(rows[4].outcome, "requested");
  assert.equal(rows[5].outcome, "succeeded");
  assert.equal(rows[4].isHighImpact, true);
  assert.equal(rows[5].isHighImpact, true);
});

test("security error mapping produces actionable guidance for recognized codes", () => {
  const rbac = mapSecurityError("PERMISSION_DENIED");
  assert.match(rbac.message, /do not have permission/i);
  assert.match(rbac.action, /Owner or Admin/i);

  const auth = mapSecurityError("AUTHENTICATION_REQUIRED");
  assert.match(auth.message, /requires authentication/i);
  assert.match(auth.action, /Sign in/i);

  const retry = mapSecurityError("BOOTSTRAP_RETRY_FORBIDDEN");
  assert.match(retry.message, /Owner or Admin/i);

  const binding = mapSecurityError("RESOURCE_BINDING_ACTIVE");
  assert.match(binding.message, /Active Resource Binding/i);
});

test("deriveSecuritySummary aggregates factual summary counts accurately", () => {
  const events = [
    { id: "a1", actor_type: "human", actor_user_id: "dev", action: "RBAC_DENIED", resource_type: "bootstrap_session", resource_id: "boot-1", result: "denied", created_at: "2026-08-01T10:00:00Z" },
    { id: "a2", actor_type: "human", actor_user_id: "owner", action: "RESOURCE_DELETED", resource_type: "resource", resource_id: "res-pg", result: "success", created_at: "2026-08-01T10:05:00Z" },
    { id: "a3", actor_type: "agent", action: "SECRET_REVEALED", resource_type: "secret", resource_id: "app-key", result: "success", created_at: "2026-08-01T10:10:00Z" },
    { id: "a4", actor_type: "worker", action: "BACKUP_FAILED", resource_type: "backup", resource_id: "bak-1", result: "failed", created_at: "2026-08-01T10:15:00Z" },
  ];

  const summary = deriveSecuritySummary(events);
  assert.equal(summary.totalLoadedEvents, 4);
  assert.equal(summary.deniedEventsCount, 1);
  assert.equal(summary.failedEventsCount, 1);
  assert.equal(summary.highImpactEventsCount, 2); // RESOURCE_DELETED and SECRET_REVEALED
  assert.equal(summary.recentDeniedEvents.length, 1);
  assert.equal(summary.recentHighImpactEvents.length, 2);
  assert.equal(summary.scopedRoleSafety.length, 6);
});
