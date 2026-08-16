"use client";

import { useState } from "react";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type {
  ApplicationCutover,
  ApplicationCutoverFinalization,
  ApplicationCutoverRollback,
  Backup,
  Resource,
  ResourceBinding,
  Restore,
  RetainedStorage,
  ServiceRecord,
} from "@/lib/contracts/registry";
import {
  backupLifecyclePresentation,
  canCreateBackup,
  canFinalize,
  canRollback,
  compileResourceOperations,
  cutoverLifecyclePresentation,
  formatBytes,
  resourceErrorExplanation,
  resourceLifecyclePresentation,
  restoreLifecyclePresentation,
} from "@/lib/presentation/resources/model";
import { ConnectApplicationDialog } from "./connect-application-dialog";
import { RestoreWizardDialog } from "./restore-wizard-dialog";
import { CutoverFinalizeDialog, CutoverReviewDialog, CutoverRollbackDialog } from "./cutover-dialogs";

export type ResourceDetailTab = "overview" | "connections" | "operations" | "backups" | "restore" | "cutover";

export function ResourceDetail({
  allResources,
  backups,
  bindings,
  cutovers,
  finalizations,
  onClose,
  onReload,
  projectID,
  resource,
  restores,
  retainedStorages,
  rollbacks,
  services,
}: {
  allResources: Resource[];
  backups: Backup[];
  bindings: ResourceBinding[];
  cutovers: ApplicationCutover[];
  finalizations: ApplicationCutoverFinalization[];
  onClose: () => void;
  onReload: () => Promise<void>;
  projectID: string;
  resource: Resource;
  restores: Restore[];
  retainedStorages: RetainedStorage[];
  rollbacks: ApplicationCutoverRollback[];
  services: ServiceRecord[];
}) {
  const isPostgres = resource.type === "postgres";
  const [activeTab, setActiveTab] = useState<ResourceDetailTab>("overview");
  const [showConnectApp, setShowConnectApp] = useState(false);
  const [showRestoreWizard, setShowRestoreWizard] = useState(false);
  const [selectedBackupForRestore, setSelectedBackupForRestore] = useState<string>("");
  const [showCutoverReview, setShowCutoverReview] = useState(false);
  const [selectedCutoverForRollback, setSelectedCutoverForRollback] = useState<ApplicationCutover | null>(null);
  const [selectedCutoverForFinalize, setSelectedCutoverForFinalize] = useState<ApplicationCutover | null>(null);

  const [busyAction, setBusyAction] = useState<string>("");
  const [actionError, setActionError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  const resourceBindings = bindings.filter((b) => b.target.id === resource.id);
  const resourceBackups = backups.filter((b) => b.source_resource_id === resource.id);
  const resourceRestores = restores.filter(
    (rst) => rst.source_resource_id === resource.id || rst.target_resource_id === resource.id,
  );
  const resourceCutovers = cutovers.filter(
    (c) => c.source_resource_id === resource.id || c.target_resource_id === resource.id,
  );
  const resourceFinalizations = finalizations.filter(
    (f) => f.source_resource_id === resource.id || f.target_resource_id === resource.id,
  );
  const matchingStorage = retainedStorages.find((s) => s.original_resource_id === resource.id);

  const pres = resourceLifecyclePresentation(resource.lifecycle);
  const operations = compileResourceOperations(
    resource,
    bindings,
    backups,
    restores,
    cutovers,
    rollbacks,
    finalizations,
    matchingStorage,
  );

  async function handleCreateBackup() {
    if (busyAction || !canCreateBackup(resource)) return;
    setBusyAction("backup");
    setActionError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      await client.createBackup(projectID, resource.id, idempotencyKey);
      await onReload();
      setActiveTab("backups");
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setActionError({ ...explanation, code: apiErr.code });
    } finally {
      setBusyAction("");
    }
  }

  async function handleDeleteResource() {
    if (busyAction) return;
    if (resourceBindings.length > 0) {
      setActionError(
        resourceErrorExplanation(
          "RESOURCE_BINDING_ACTIVE",
          "Resource is still connected to one or more Applications.",
        ),
      );
      return;
    }

    setBusyAction("delete");
    setActionError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      await client.deleteResource(projectID, resource.id, idempotencyKey);
      await onReload();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setActionError({ ...explanation, code: apiErr.code });
    } finally {
      setBusyAction("");
    }
  }

  async function handleDisconnectBinding(bindingID: string) {
    if (busyAction) return;
    setBusyAction(`disconnect-${bindingID}`);
    setActionError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      await client.deleteResourceBinding(projectID, bindingID, idempotencyKey);
      await onReload();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setActionError({ ...explanation, code: apiErr.code });
    } finally {
      setBusyAction("");
    }
  }

  return (
    <aside aria-label={`Resource details for ${resource.name}`} className="canvasInspector resourceDetailPanel">
      <div className="inspectorHeading">
        <div>
          <p className="canvasPath">
            Infrastructure / {resource.type.toUpperCase()} / {resource.environment_id || "Default"}
          </p>
          <span className="canvasNodeKind">Managed Resource</span>
          <h2 tabIndex={-1}>{resource.name}</h2>
        </div>
        <div className="inspectorHeaderActions">
          <span className={`draftState ${pres.tone}`}>{pres.label}</span>
          <button aria-label="Close detail" className="iconButton" onClick={onClose} type="button">
            <svg aria-hidden="true" viewBox="0 0 20 20">
              <path d="m5 5 10 10M15 5 5 15" />
            </svg>
          </button>
        </div>
      </div>

      <nav aria-label="Resource sections" className="resourceTabs">
        <button
          aria-selected={activeTab === "overview"}
          className={activeTab === "overview" ? "active" : ""}
          onClick={() => setActiveTab("overview")}
          role="tab"
          type="button"
        >
          Overview
        </button>
        <button
          aria-selected={activeTab === "connections"}
          className={activeTab === "connections" ? "active" : ""}
          onClick={() => setActiveTab("connections")}
          role="tab"
          type="button"
        >
          Connections ({resourceBindings.length})
        </button>
        <button
          aria-selected={activeTab === "operations"}
          className={activeTab === "operations" ? "active" : ""}
          onClick={() => setActiveTab("operations")}
          role="tab"
          type="button"
        >
          Operations ({operations.length})
        </button>
        {isPostgres ? (
          <>
            <button
              aria-selected={activeTab === "backups"}
              className={activeTab === "backups" ? "active" : ""}
              onClick={() => setActiveTab("backups")}
              role="tab"
              type="button"
            >
              Backups ({resourceBackups.length})
            </button>
            <button
              aria-selected={activeTab === "restore"}
              className={activeTab === "restore" ? "active" : ""}
              onClick={() => setActiveTab("restore")}
              role="tab"
              type="button"
            >
              Restore ({resourceRestores.length})
            </button>
            <button
              aria-selected={activeTab === "cutover"}
              className={activeTab === "cutover" ? "active" : ""}
              onClick={() => setActiveTab("cutover")}
              role="tab"
              type="button"
            >
              Cutover ({resourceCutovers.length})
            </button>
          </>
        ) : null}
      </nav>

      {actionError ? (
        <div className="truthCallout span2" role="alert">
          <b>{actionError.summary}</b>
          <p>{actionError.action}</p>
          {actionError.code ? <small className="errorCode">Error code: {actionError.code}</small> : null}
        </div>
      ) : null}

      {activeTab === "overview" ? (
        <div className="detailSection">
          <section className="inspectorSection">
            <h4>Runtime Facts</h4>
            <dl className="factsGrid">
              <div>
                <dt>Resource ID</dt>
                <dd><code>{resource.id}</code></dd>
              </div>
              <div>
                <dt>Engine & Version</dt>
                <dd>{resource.type === "postgres" ? "PostgreSQL 16" : resource.type.toUpperCase()}</dd>
              </div>
              <div>
                <dt>CPU Allocation</dt>
                <dd>{resource.managed?.cpu_millicores ? `${resource.managed.cpu_millicores}m` : "Not reported"}</dd>
              </div>
              <div>
                <dt>Memory Allocation</dt>
                <dd>{formatBytes(resource.managed?.memory_bytes)}</dd>
              </div>
              <div>
                <dt>Replicas</dt>
                <dd>{resource.managed?.replicas ?? 1}</dd>
              </div>
              <div>
                <dt>Workload Ready</dt>
                <dd className={resource.runtime?.evidence?.workload_ready ? "statusPass" : "statusNeutral"}>
                  {resource.runtime?.evidence?.workload_ready ? "Ready" : "Pending / Not reported"}
                </dd>
              </div>
              <div>
                <dt>Pod Ready</dt>
                <dd className={resource.runtime?.evidence?.pod_ready ? "statusPass" : "statusNeutral"}>
                  {resource.runtime?.evidence?.pod_ready ? "Ready" : "Pending / Not reported"}
                </dd>
              </div>
              <div>
                <dt>Storage Ready</dt>
                <dd className={resource.runtime?.evidence?.storage_ready ? "statusPass" : "statusNeutral"}>
                  {resource.runtime?.evidence?.storage_ready ? "Ready" : "Pending / In-memory"}
                </dd>
              </div>
            </dl>
          </section>

          {isPostgres && resource.managed?.storage?.persistent ? (
            <section className="inspectorSection">
              <h4>Persistent Storage Status</h4>
              <dl className="factsGrid">
                <div>
                  <dt>Configured Storage Size</dt>
                  <dd>{formatBytes(resource.managed.storage.size_bytes)}</dd>
                </div>
                <div>
                  <dt>Storage Policy</dt>
                  <dd>{resource.managed.storage.policy_ref || "standard-rwo"}</dd>
                </div>
                <div>
                  <dt>PVC Name</dt>
                  <dd><code>{resource.runtime?.evidence?.pvc_name || "Provisioning"}</code></dd>
                </div>
                <div>
                  <dt>Storage Retention State</dt>
                  <dd>
                    {resource.runtime?.evidence?.storage_retained
                      ? "Retained in RetainedStorage inventory"
                      : "Active runtime bound"}
                  </dd>
                </div>
              </dl>
            </section>
          ) : null}

          {resource.runtime?.spec ? (
            <section className="inspectorSection">
              <h4>Secondary Placement & Authority Metadata</h4>
              <dl className="factsGrid secondaryFacts">
                <div>
                  <dt>Assigned Node</dt>
                  <dd><code>{resource.runtime.spec.assignment.node_id || "Unplaced"}</code></dd>
                </div>
                <div>
                  <dt>Configuration Hash</dt>
                  <dd><code>{resource.runtime.spec.configuration_hash ? resource.runtime.spec.configuration_hash.slice(0, 16) : "None"}</code></dd>
                </div>
                <div>
                  <dt>PVC UID</dt>
                  <dd><code>{resource.runtime.evidence?.pvc_uid || "Not reported"}</code></dd>
                </div>
                <div>
                  <dt>Storage Hash</dt>
                  <dd><code>{resource.runtime.evidence?.storage_hash ? resource.runtime.evidence.storage_hash.slice(0, 16) : "Not reported"}</code></dd>
                </div>
              </dl>
            </section>
          ) : null}

          <section className="inspectorSection dangerSection">
            <h4>Resource Lifecycle Actions</h4>
            <p className="sectionHint">
              Deleting this resource removes the runtime instance. For PostgreSQL, persistent storage is retained in the{" "}
              <strong>Retained Storage</strong> tab and can be inspected or destroyed separately.
            </p>
            <div className="buttonRow">
              <button
                className="secondary destructive"
                disabled={Boolean(busyAction) || resource.lifecycle === "deleting"}
                onClick={handleDeleteResource}
                type="button"
              >
                {busyAction === "delete"
                  ? "Deleting Runtime…"
                  : resource.lifecycle === "deleting"
                  ? "Deletion in Progress"
                  : "Delete Resource"}
              </button>
            </div>
          </section>
        </div>
      ) : null}

      {activeTab === "connections" ? (
        <div className="detailSection">
          <div className="sectionActionBar">
            <p>Application services securely connected to {resource.name}.</p>
            <button className="primary" onClick={() => setShowConnectApp(true)} type="button">
              + Connect Application
            </button>
          </div>

          {resourceBindings.length === 0 ? (
            <div className="emptyInspectorState">
              <p>No applications currently connected.</p>
              <small>Click &quot;Connect Application&quot; to bind an application service to this resource.</small>
            </div>
          ) : (
            <div className="bindingsList">
              {resourceBindings.map((b) => (
                <article className="bindingCard" key={b.id}>
                  <div className="bindingCardHeader">
                    <div>
                      <strong>Application: {b.source.id}</strong>
                      <small className="logicalName">Logical name: <code>{b.logical_name}</code></small>
                    </div>
                    <span className="bindingLifecycle">{b.lifecycle}</span>
                  </div>
                  <dl className="bindingFacts">
                    <div>
                      <dt>Protocol</dt>
                      <dd>{b.protocol}</dd>
                    </div>
                    <div>
                      <dt>Role Name</dt>
                      <dd><code>{b.role_name || "Standard role"}</code></dd>
                    </div>
                    <div>
                      <dt>Database</dt>
                      <dd>{b.database || "opsi"}</dd>
                    </div>
                    <div>
                      <dt>Bound At</dt>
                      <dd>{b.created_at}</dd>
                    </div>
                  </dl>
                  <div className="bindingFooter">
                    <small>Credentials injected securely into container runtime.</small>
                    <button
                      className="textButton destructive"
                      disabled={Boolean(busyAction)}
                      onClick={() => handleDisconnectBinding(b.id)}
                      type="button"
                    >
                      {busyAction === `disconnect-${b.id}` ? "Disconnecting…" : "Disconnect"}
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </div>
      ) : null}

      {activeTab === "operations" ? (
        <div className="detailSection">
          <h4>Durable Operation History</h4>
          {operations.length === 0 ? (
            <p className="emptyStateText">No durable operations recorded yet.</p>
          ) : (
            <div className="operationsTimeline">
              {operations.map((op) => (
                <div className="timelineItem" key={op.id}>
                  <div className="timelineDot" data-tone={op.tone} />
                  <div className="timelineContent">
                    <div className="timelineHeader">
                      <strong>{op.title}</strong>
                      <span className={`statusTag ${op.tone}`}>{op.lifecycle}</span>
                    </div>
                    {op.details ? <p className="timelineDetails">{op.details}</p> : null}
                    {op.failureMessage ? (
                      <p className="timelineError">Failure: {op.failureMessage}</p>
                    ) : null}
                    <small className="timelineTime">{op.timestamp}</small>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      ) : null}

      {isPostgres && activeTab === "backups" ? (
        <div className="detailSection">
          <div className="sectionActionBar">
            <div>
              <h4>PostgreSQL Logical Backups</h4>
              <small>Consistent logical pg_dump backups stored in Cloud storage authority.</small>
            </div>
            <button
              className="primary"
              disabled={Boolean(busyAction) || !canCreateBackup(resource)}
              onClick={handleCreateBackup}
              type="button"
            >
              {busyAction === "backup" ? "Creating Backup…" : "Create Backup"}
            </button>
          </div>

          {resourceBackups.length === 0 ? (
            <div className="emptyInspectorState">
              <p>No backups created yet for this resource.</p>
              <small>
                {resource.lifecycle === "ready"
                  ? 'Click "Create Backup" to take a point-in-time logical backup.'
                  : "Database must be in Ready state to take a backup."}
              </small>
            </div>
          ) : (
            <div className="backupsList">
              {resourceBackups.map((bk) => {
                const bkPres = backupLifecyclePresentation(bk.lifecycle);
                return (
                  <article className="backupCard" key={bk.id}>
                    <div className="backupCardHeader">
                      <div>
                        <strong>Backup {bk.id}</strong>
                        <small>{bk.source_database} · {bk.source_postgres_version || "PG 16"}</small>
                      </div>
                      <span className={`statusTag ${bkPres.tone}`}>{bkPres.label}</span>
                    </div>
                    <dl className="factsGrid">
                      <div>
                        <dt>Artifact Size</dt>
                        <dd>{formatBytes(bk.artifact_size)}</dd>
                      </div>
                      <div>
                        <dt>Completed Time</dt>
                        <dd>{bk.completed_at || bk.started_at || bk.created_at}</dd>
                      </div>
                      <div>
                        <dt>Archive Verified</dt>
                        <dd>{bk.archive_verified ? "Yes (Checksum verified)" : "Pending / Failed"}</dd>
                      </div>
                      <div>
                        <dt>Format</dt>
                        <dd>{bk.format || "custom"}</dd>
                      </div>
                    </dl>
                    {bk.failure_message_redacted ? (
                      <p className="timelineError">Error: {bk.failure_message_redacted}</p>
                    ) : null}
                    <div className="backupActions">
                      <button
                        className="secondary"
                        disabled={bk.lifecycle !== "succeeded"}
                        onClick={() => {
                          setSelectedBackupForRestore(bk.id);
                          setShowRestoreWizard(true);
                        }}
                        type="button"
                      >
                        Restore into New Resource →
                      </button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </div>
      ) : null}

      {isPostgres && activeTab === "restore" ? (
        <div className="detailSection">
          <div className="sectionActionBar">
            <div>
              <h4>PostgreSQL Restores</h4>
              <small>Restores create a new separate instance from a point-in-time backup.</small>
            </div>
            <button
              className="primary"
              disabled={resourceBackups.filter((b) => b.lifecycle === "succeeded").length === 0}
              onClick={() => {
                setSelectedBackupForRestore("");
                setShowRestoreWizard(true);
              }}
              type="button"
            >
              Start New Restore
            </button>
          </div>

          {resourceRestores.length === 0 ? (
            <div className="emptyInspectorState">
              <p>No restores executed for this resource.</p>
              <small>Choose a succeeded backup and restore it into a clean PostgreSQL target instance.</small>
            </div>
          ) : (
            <div className="restoresList">
              {resourceRestores.map((rst) => {
                const rstPres = restoreLifecyclePresentation(rst.lifecycle);
                const isTarget = rst.target_resource_id === resource.id;
                return (
                  <article className="restoreCard" key={rst.id}>
                    <div className="restoreCardHeader">
                      <div>
                        <strong>Restore {rst.id}</strong>
                        <small>
                          {isTarget ? "This resource is TARGET" : "This resource is SOURCE"} · From backup{" "}
                          {rst.backup_id}
                        </small>
                      </div>
                      <span className={`statusTag ${rstPres.tone}`}>{rstPres.label}</span>
                    </div>
                    <dl className="factsGrid">
                      <div>
                        <dt>Target Resource</dt>
                        <dd><code>{rst.target_resource_id}</code></dd>
                      </div>
                      <div>
                        <dt>Artifact Size</dt>
                        <dd>{formatBytes(rst.artifact_size)}</dd>
                      </div>
                      <div>
                        <dt>Completed Time</dt>
                        <dd>{rst.completed_at || rst.started_at || rst.created_at}</dd>
                      </div>
                      <div>
                        <dt>Restored Objects</dt>
                        <dd>
                          {rst.restored_objects
                            ? `${rst.restored_objects.tables} tables, ${rst.restored_objects.sequences} seqs`
                            : "Standard objects"}
                        </dd>
                      </div>
                    </dl>
                    {rst.failure_message_redacted ? (
                      <p className="timelineError">Error: {rst.failure_message_redacted}</p>
                    ) : null}
                  </article>
                );
              })}
            </div>
          )}
        </div>
      ) : null}

      {isPostgres && activeTab === "cutover" ? (
        <div className="detailSection">
          <div className="sectionActionBar">
            <div>
              <h4>Database Cutover & Rollback History</h4>
              <small>Safe zero-downtime traffic switching with rollback preservation.</small>
            </div>
            <button className="primary" onClick={() => setShowCutoverReview(true)} type="button">
              Review New Cutover
            </button>
          </div>

          {resourceCutovers.length === 0 ? (
            <div className="emptyInspectorState">
              <p>No cutovers initiated yet.</p>
              <small>
                Once you have a restored target resource and application binding, review and execute a safe cutover.
              </small>
            </div>
          ) : (
            <div className="cutoversList">
              {resourceCutovers.map((cut) => {
                const cutPres = cutoverLifecyclePresentation(cut.lifecycle);
                const matchingFinalization = resourceFinalizations.find((f) => f.cutover_id === cut.id);
                const isFinalized = matchingFinalization?.lifecycle === "succeeded";
                return (
                  <article className="cutoverCard" key={cut.id}>
                    <div className="cutoverCardHeader">
                      <div>
                        <strong>Cutover {cut.id}</strong>
                        <small>Application: {cut.application_id}</small>
                      </div>
                      <span className={`statusTag ${cutPres.tone}`}>{cutPres.label}</span>
                    </div>

                    <div className="cutoverEndpoints">
                      <span>Source: <code>{cut.source_resource_id}</code></span>
                      <span>➔</span>
                      <span>Target: <code>{cut.target_resource_id}</code></span>
                    </div>

                    <dl className="factsGrid">
                      <div>
                        <dt>Completed Time</dt>
                        <dd>{cut.completed_at || cut.applied_at || cut.requested_at}</dd>
                      </div>
                      <div>
                        <dt>Rollback Preserved</dt>
                        <dd>{isFinalized ? "Finalized (Closed)" : "Active & Available"}</dd>
                      </div>
                      <div>
                        <dt>Source Rollback Binding</dt>
                        <dd><code>{cut.source_binding_id}</code></dd>
                      </div>
                      <div>
                        <dt>Target Binding</dt>
                        <dd><code>{cut.target_binding_id}</code></dd>
                      </div>
                    </dl>

                    {cut.failure_message_redacted ? (
                      <p className="timelineError">Error: {cut.failure_message_redacted}</p>
                    ) : null}

                    {cut.lifecycle === "succeeded" && !isFinalized ? (
                      <div className="cutoverActions">
                        <button
                          className="secondary warning"
                          disabled={!canRollback(cut, matchingFinalization)}
                          onClick={() => setSelectedCutoverForRollback(cut)}
                          type="button"
                        >
                          Rollback to Source
                        </button>
                        <button
                          className="primary"
                          disabled={!canFinalize(cut, matchingFinalization)}
                          onClick={() => setSelectedCutoverForFinalize(cut)}
                          type="button"
                        >
                          Finalize Cutover
                        </button>
                      </div>
                    ) : isFinalized ? (
                      <div className="finalizedBanner">
                        <small>
                          Cutover Finalized: Source binding was revoked. Source data is retained in{" "}
                          <code>{cut.source_resource_id}</code>.
                        </small>
                      </div>
                    ) : null}
                  </article>
                );
              })}
            </div>
          )}
        </div>
      ) : null}

      {showConnectApp ? (
        <ConnectApplicationDialog
          environmentID={resource.environment_id}
          onBindingCreated={async () => {
            await onReload();
            setShowConnectApp(false);
          }}
          onClose={() => setShowConnectApp(false)}
          projectID={projectID}
          resource={resource}
          services={services}
        />
      ) : null}

      {showRestoreWizard ? (
        <RestoreWizardDialog
          allResources={allResources}
          backups={backups}
          initialBackupID={selectedBackupForRestore}
          onClose={() => setShowRestoreWizard(false)}
          onRestoreCreated={async () => {
            await onReload();
            setShowRestoreWizard(false);
            setActiveTab("restore");
          }}
          projectID={projectID}
          sourceResource={resource}
        />
      ) : null}

      {showCutoverReview ? (
        <CutoverReviewDialog
          allResources={allResources}
          bindings={bindings}
          onClose={() => setShowCutoverReview(false)}
          onCutoverApplied={async () => {
            await onReload();
            setShowCutoverReview(false);
            setActiveTab("cutover");
          }}
          projectID={projectID}
          sourceResource={resource}
        />
      ) : null}

      {selectedCutoverForRollback ? (
        <CutoverRollbackDialog
          cutover={selectedCutoverForRollback}
          onClose={() => setSelectedCutoverForRollback(null)}
          onRollbackApplied={async () => {
            await onReload();
            setSelectedCutoverForRollback(null);
            setActiveTab("cutover");
          }}
          projectID={projectID}
        />
      ) : null}

      {selectedCutoverForFinalize ? (
        <CutoverFinalizeDialog
          cutover={selectedCutoverForFinalize}
          onClose={() => setSelectedCutoverForFinalize(null)}
          onFinalized={async () => {
            await onReload();
            setSelectedCutoverForFinalize(null);
            setActiveTab("cutover");
          }}
          projectID={projectID}
        />
      ) : null}
    </aside>
  );
}
