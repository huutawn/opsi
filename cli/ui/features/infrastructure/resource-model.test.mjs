import assert from "node:assert/strict";
import test from "node:test";

import {
  backupLifecyclePresentation,
  buildNATSCreateRequest,
  buildPostgresCreateRequest,
  buildValkeyCreateRequest,
  canCreateBackup,
  canCutover,
  canFinalize,
  canRestore,
  canRollback,
  compileResourceOperations,
  cutoverLifecyclePresentation,
  cutoverWarningExplanation,
  formatBytes,
  POSTGRES_STORAGE_POLICY,
  POSTGRES_VERSION,
  resourceCatalogItem,
  resourceErrorExplanation,
  resourceLifecyclePresentation,
  resourceTypeCatalog,
  restoreLifecyclePresentation,
  retainedStorageLifecyclePresentation,
  serverLifecyclePresentation,
} from "../../lib/presentation/resources/model.ts";

test("infrastructure catalog exposes implemented capabilities and excludes deferred RabbitMQ", () => {
  const catalog = resourceTypeCatalog();
  assert.equal(catalog.length, 3);
  assert.deepEqual(
    catalog.map((item) => item.type),
    ["postgres", "redis", "nats"],
  );
  assert.equal(catalog.some((item) => item.type === "rabbitmq"), false);

  const pg = resourceCatalogItem("postgres");
  assert.ok(pg);
  assert.equal(pg.stateful, true);
  assert.equal(pg.storageRequired, true);
  assert.equal(pg.defaultPort, 5432);
  assert.equal(pg.category, "database");

  const valkey = resourceCatalogItem("redis");
  assert.ok(valkey);
  assert.equal(valkey.stateful, false);
  assert.equal(valkey.storageRequired, false);
  assert.equal(valkey.defaultPort, 6379);

  const nats = resourceCatalogItem("nats");
  assert.ok(nats);
  assert.equal(nats.stateful, false);
  assert.equal(nats.storageRequired, false);
  assert.equal(nats.defaultPort, 4222);
});

test("resource and server lifecycle presentation map factual states truthfully", () => {
  assert.deepEqual(resourceLifecyclePresentation("ready"), { label: "Ready", tone: "ready", statusValue: "ready" });
  assert.deepEqual(resourceLifecyclePresentation("provisioning"), { label: "Provisioning", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(resourceLifecyclePresentation("updating"), { label: "Updating", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(resourceLifecyclePresentation("deleting"), { label: "Deleting", tone: "warning", statusValue: "warning" });
  assert.deepEqual(resourceLifecyclePresentation("failed"), { label: "Failed", tone: "failed", statusValue: "failed" });
  assert.deepEqual(resourceLifecyclePresentation("degraded"), { label: "Degraded", tone: "warning", statusValue: "warning" });
  assert.deepEqual(resourceLifecyclePresentation("unplaced"), { label: "Unplaced", tone: "neutral", statusValue: "idle" });

  assert.deepEqual(serverLifecyclePresentation("Ready"), { label: "Ready", tone: "ready", statusValue: "ready" });
  assert.deepEqual(serverLifecyclePresentation("Connecting"), { label: "Connecting", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(serverLifecyclePresentation("Bootstrapping"), { label: "Bootstrapping", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(serverLifecyclePresentation("Offline"), { label: "Offline", tone: "warning", statusValue: "offline" });
  assert.deepEqual(serverLifecyclePresentation("Failed"), { label: "Failed", tone: "failed", statusValue: "failed" });

  assert.deepEqual(retainedStorageLifecyclePresentation("retained"), { label: "Retained", tone: "warning", statusValue: "warning" });
  assert.deepEqual(retainedStorageLifecyclePresentation("destroying"), { label: "Destroying", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(retainedStorageLifecyclePresentation("destroyed"), { label: "Destroyed", tone: "neutral", statusValue: "idle" });
  assert.deepEqual(retainedStorageLifecyclePresentation("destroy_failed"), { label: "Destroy Failed", tone: "failed", statusValue: "failed" });

  assert.deepEqual(backupLifecyclePresentation("succeeded"), { label: "Succeeded", tone: "ready", statusValue: "ready" });
  assert.deepEqual(backupLifecyclePresentation("running"), { label: "Running", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(backupLifecyclePresentation("failed"), { label: "Failed", tone: "failed", statusValue: "failed" });

  assert.deepEqual(restoreLifecyclePresentation("succeeded"), { label: "Succeeded", tone: "ready", statusValue: "ready" });
  assert.deepEqual(restoreLifecyclePresentation("running"), { label: "Restoring", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(restoreLifecyclePresentation("verifying"), { label: "Verifying", tone: "bootstrapping", statusValue: "bootstrapping" });

  assert.deepEqual(cutoverLifecyclePresentation("succeeded"), { label: "Succeeded", tone: "ready", statusValue: "ready" });
  assert.deepEqual(cutoverLifecyclePresentation("applying"), { label: "Applying", tone: "bootstrapping", statusValue: "bootstrapping" });
  assert.deepEqual(cutoverLifecyclePresentation("revoking_source_binding"), { label: "Revoking source binding", tone: "bootstrapping", statusValue: "bootstrapping" });
});

test("typed error codes map to actionable user explanations", () => {
  const bindingActive = resourceErrorExplanation("RESOURCE_BINDING_ACTIVE");
  assert.match(bindingActive.summary, /still connected to one or more Applications/i);
  assert.match(bindingActive.action, /Connections tab/i);

  const restoreNotEmpty = resourceErrorExplanation("RESTORE_TARGET_NOT_EMPTY");
  assert.match(restoreNotEmpty.summary, /already contains tables/i);
  assert.match(restoreNotEmpty.action, /pristine/i);

  const staleCutover = resourceErrorExplanation("CUTOVER_STALE_REVIEW");
  assert.match(staleCutover.summary, /stale/i);

  const rollbackIneligible = resourceErrorExplanation("ROLLBACK_CUTOVER_INELIGIBLE");
  assert.match(rollbackIneligible.summary, /ineligible for rollback/i);

  const finalized = resourceErrorExplanation("CUTOVER_FINALIZED");
  assert.match(finalized.summary, /already been finalized/i);

  const retainedConflict = resourceErrorExplanation("RETAINED_STORAGE_ACTIVE_REFERENCE");
  assert.match(retainedConflict.summary, /active resource or binding/i);
});

test("cutover warning explanations preserve safety semantics", () => {
  const notSynced = cutoverWarningExplanation("TARGET_NOT_CONTINUOUSLY_SYNCHRONIZED");
  assert.match(notSynced, /point-in-time restored database/i);
  assert.match(notSynced, /not continuously synchronized/i);

  const ageWarning = cutoverWarningExplanation("BACKUP_AGE_NONZERO");
  assert.match(ageWarning, /writes on source after backup are not present/i);

  const rollbackDivergence = cutoverWarningExplanation("TARGET_WRITES_MAY_NOT_EXIST_ON_SOURCE");
  assert.match(rollbackDivergence, /does not synchronize TARGET writes back into SOURCE/i);
});

test("resource request builders construct canonical specifications without credentials", () => {
  const pgReq = buildPostgresCreateRequest({
    environmentID: "env-prod",
    name: "db-main",
    cpuMillicores: 1000,
    memoryBytes: 2 * 1024 * 1024 * 1024,
    storageBytes: 20 * 1024 * 1024 * 1024,
  });

  assert.equal(pgReq.environment_id, "env-prod");
  assert.equal(pgReq.name, "db-main");
  assert.equal(pgReq.kind, "managed_service");
  assert.equal(pgReq.type, "postgres");
  assert.equal(pgReq.managed?.version, POSTGRES_VERSION);
  assert.equal(pgReq.managed?.storage.policy_ref, POSTGRES_STORAGE_POLICY);
  assert.equal(pgReq.managed?.storage.persistent, true);
  assert.equal(pgReq.managed?.storage.size_bytes, 20 * 1024 * 1024 * 1024);
  assert.equal(pgReq.managed?.connection_policy.mode, "internal");
  // No plaintext passwords or tokens in request
  assert.equal(JSON.stringify(pgReq).includes("password"), false);

  const valkeyReq = buildValkeyCreateRequest({
    environmentID: "env-prod",
    name: "cache-1",
  });
  assert.equal(valkeyReq.type, "redis");
  assert.equal(valkeyReq.managed?.storage.persistent, false);

  const natsReq = buildNATSCreateRequest({
    environmentID: "env-prod",
    name: "queue-1",
  });
  assert.equal(natsReq.type, "nats");
  assert.equal(natsReq.managed?.storage.persistent, false);
});

test("operation guards enforce strict precondition rules", () => {
  const readyPG = {
    id: "res-1",
    project_id: "p1",
    environment_id: "env1",
    name: "pg-1",
    kind: "managed_service",
    type: "postgres",
    lifecycle: "ready",
    created_by: "tawn",
    created_at: "2026-08-16T12:00:00Z",
    updated_at: "2026-08-16T12:00:00Z",
  };
  assert.equal(canCreateBackup(readyPG), true);

  const provPG = { ...readyPG, lifecycle: "provisioning" };
  assert.equal(canCreateBackup(provPG), false);

  const redisRes = { ...readyPG, type: "redis" };
  assert.equal(canCreateBackup(redisRes), false);

  const succeededBackup = {
    schema_version: "1.0",
    id: "bk-1",
    project_id: "p1",
    environment_id: "env1",
    source_resource_id: "res-1",
    source_node_id: "node-1",
    resource_type: "postgres",
    backup_type: "logical",
    source_database: "opsi",
    source_spec_revision: 1,
    source_spec_hash: "hash",
    source_pvc_name: "pvc-1",
    source_pvc_uid: "uid-1",
    source_storage_hash: "shash",
    format: "custom",
    dump_options: [],
    lifecycle: "succeeded",
    store_id: "store-1",
    object_key: "key",
    archive_verified: true,
    requested_by: "tawn",
    requested_at: "2026-08-16T12:00:00Z",
    created_at: "2026-08-16T12:00:00Z",
    attempt_count: 1,
  };
  assert.equal(canRestore(succeededBackup), true);

  const runningBackup = { ...succeededBackup, lifecycle: "running" };
  assert.equal(canRestore(runningBackup), false);

  const validReview = {
    schema_version: "1.0",
    id: "cr-1",
    project_id: "p1",
    environment_id: "env1",
    application_id: "app-api",
    source_binding_id: "b-1",
    source_resource_id: "res-1",
    target_resource_id: "res-2",
    target_binding_id: "b-2",
    application_config_revision: 1,
    application_config_hash: "hash",
    source_binding_revision: "1",
    target_binding_revision: "1",
    source_resource_revision: 1,
    source_resource_spec_hash: "shash",
    target_resource_revision: 1,
    target_resource_spec_hash: "thash",
    target_restore_id: "rst-1",
    target_restore_revision: "1",
    backup_id: "bk-1",
    backup_age_seconds: 120,
    validation_summary: {
      source_sql_preflight: "pass",
      target_sql_preflight: "pass",
      target_role_attributes: "LOGIN",
      source_binding_ready: true,
      target_binding_ready: true,
      target_restore_ready: true,
    },
    warnings: ["BACKUP_AGE_NONZERO"],
    lifecycle: "succeeded",
    requested_by: "tawn",
    requested_at: "2026-08-16T12:00:00Z",
    attempt_count: 1,
  };
  assert.equal(canCutover(validReview), true);

  const invalidReview = {
    ...validReview,
    validation_summary: { ...validReview.validation_summary, target_restore_ready: false },
  };
  assert.equal(canCutover(invalidReview), false);

  const succeededCutover = {
    schema_version: "1.0",
    id: "cut-1",
    project_id: "p1",
    environment_id: "env1",
    application_id: "app-api",
    cutover_review_id: "cr-1",
    source_binding_id: "b-1",
    target_binding_id: "b-2",
    source_resource_id: "res-1",
    target_resource_id: "res-2",
    reviewed_application_config_revision: 1,
    reviewed_application_config_hash: "hash",
    pre_cutover_application_config_revision: 1,
    pre_cutover_application_config_hash: "hash",
    resulting_application_config_revision: 2,
    resulting_application_config_hash: "hash2",
    lifecycle: "succeeded",
    requested_by: "tawn",
    requested_at: "2026-08-16T12:00:00Z",
    updated_at: "2026-08-16T12:05:00Z",
    verification_summary: {
      source_sql_preflight: "pass",
      target_sql_preflight: "pass",
      target_role_attributes: "LOGIN",
      deployment_ready: true,
      workload_ready: true,
      target_db_connected: true,
      restored_data_verified: true,
      target_only_marker_present: true,
      source_only_marker_absent: true,
      post_cutover_target_written: true,
      source_rollback_preserved: true,
    },
  };
  assert.equal(canRollback(succeededCutover), true);
  assert.equal(canFinalize(succeededCutover), true);

  const finalizedState = {
    schema_version: "1.0",
    id: "fin-1",
    project_id: "p1",
    environment_id: "env1",
    application_id: "app-api",
    cutover_id: "cut-1",
    source_binding_id: "b-1",
    target_binding_id: "b-2",
    source_resource_id: "res-1",
    target_resource_id: "res-2",
    application_config_revision: 3,
    application_config_hash: "hash3",
    cutover_evidence_hash: "ehash",
    lifecycle: "succeeded",
    requested_by: "tawn",
    requested_at: "2026-08-16T12:10:00Z",
    updated_at: "2026-08-16T12:11:00Z",
    verification_summary: {
      target_sql_preflight: "pass",
      target_role_attributes: "LOGIN",
      target_db_connected: true,
      target_only_marker_present: true,
      post_cutover_marker_present: true,
      source_marker_absent: true,
      source_binding_revoked: true,
      source_credential_rejected: true,
      source_resource_retained: true,
      post_finalize_target_written: true,
    },
  };
  assert.equal(canRollback(succeededCutover, finalizedState), false);
  assert.equal(canFinalize(succeededCutover, finalizedState), false);
});

test("formatBytes formats storage values accurately", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(1024), "1 KiB");
  assert.equal(formatBytes(1536), "1.5 KiB");
  assert.equal(formatBytes(512 * 1024 * 1024), "512 MiB");
  assert.equal(formatBytes(10 * 1024 * 1024 * 1024), "10 GiB");
  assert.equal(formatBytes(undefined), "Not reported");
});

test("compileResourceOperations aggregates durable events chronologically", () => {
  const resource = {
    id: "res-1",
    project_id: "p1",
    environment_id: "env1",
    name: "pg-main",
    kind: "managed_service",
    type: "postgres",
    lifecycle: "ready",
    created_by: "tawn",
    created_at: "2026-08-16T10:00:00Z",
    updated_at: "2026-08-16T10:00:00Z",
  };

  const bindings = [
    {
      id: "bind-1",
      project_id: "p1",
      environment_id: "env1",
      source: { kind: "application", id: "app-api" },
      target: { kind: "managed_service", id: "res-1" },
      protocol: "postgres",
      logical_name: "DATABASE",
      lifecycle: "ready",
      created_at: "2026-08-16T10:15:00Z",
      updated_at: "2026-08-16T10:15:00Z",
    },
  ];

  const backups = [
    {
      schema_version: "1.0",
      id: "bk-1",
      project_id: "p1",
      environment_id: "env1",
      source_resource_id: "res-1",
      source_node_id: "node-1",
      resource_type: "postgres",
      backup_type: "logical",
      source_database: "opsi",
      source_spec_revision: 1,
      source_spec_hash: "hash",
      source_pvc_name: "pvc-1",
      source_pvc_uid: "uid-1",
      source_storage_hash: "shash",
      format: "custom",
      dump_options: [],
      lifecycle: "succeeded",
      store_id: "store-1",
      object_key: "key",
      archive_verified: true,
      requested_by: "tawn",
      requested_at: "2026-08-16T10:30:00Z",
      created_at: "2026-08-16T10:30:00Z",
      completed_at: "2026-08-16T10:32:00Z",
      attempt_count: 1,
    },
  ];

  const ops = compileResourceOperations(
    resource,
    bindings,
    backups,
    [],
    [],
    [],
    [],
    null,
  );

  assert.equal(ops.length, 3);
  // Sort order is descending: backup (10:32) -> binding (10:15) -> provision (10:00)
  assert.equal(ops[0].type, "backup");
  assert.equal(ops[1].type, "binding");
  assert.equal(ops[2].type, "provision");
});
