"use client";

import { useMemo } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import { DeliveryStatus, Evidence, displayTime, short } from "@/features/delivery/shared";
import type { ConsoleController } from "@/features/console/types";
import type { DeliveryData } from "@/features/delivery/data";
import { useSelectedDeployment } from "@/features/delivery/polling";
import { DependencyVerificationPanel } from "@/features/dependencies/verification-panel";
import { LocalClient } from "@/lib/api/local-client";
import type { DeploymentJob } from "@/lib/contracts/registry";

export function DeploymentDetail({
  console,
  data,
  selected,
}: {
  console: ConsoleController;
  data: DeliveryData;
  selected?: DeploymentJob;
}) {
  const projectID = console.state.project?.id ?? "";
  const polled = useSelectedDeployment(projectID, selected?.id ?? "", selected, data.mergeDeployment);
  const job = polled.job ?? selected;

  const service = useMemo(
    () => (job ? data.services.find((s) => s.id === job.service_id) : undefined),
    [data.services, job]
  );
  const dependencies = useMemo(
    () => service?.configuration?.dependencies ?? [],
    [service]
  );

  if (!job) {
    return (
      <aside className="detailPanel">
        <Empty
          title="Select a deployment"
          text="Choose a DeploymentJob to inspect artifact, rollout, verification, failure, rollback, and event evidence."
        />
      </aside>
    );
  }

  const currentDigest = job.current_digest || job.terminal_result?.current_digest;
  const canCancel = job.status === "queued" && ["", "prepared", "queued"].includes(job.rollout_state ?? "");
  const canRetry = job.status === "failed" && job.failure_code === "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" && !job.terminal_result;
  const canCleanup = Boolean(job.snapshot?.preview) && !["cleaning", "cleaned"].includes(job.rollout_state ?? "");
  const client = new LocalClient();

  function mutate(operation: "cancel" | "retry" | "cleanup") {
    if (!job) return;
    const exact = operation === "cleanup";
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: job.snapshot?.preview ? "preview deployment" : "deployment",
        targetID: job.id,
        operation,
        diff: [
          operation === "cleanup"
            ? "remove active preview runtime resources"
            : operation === "retry"
            ? "retry the same unresolved DeploymentJob"
            : "cancel before the unsafe execution boundary",
        ],
        risk:
          operation === "cleanup"
            ? "Destructive runtime mutation; the exact job identity is required."
            : "The backend remains authoritative and may reject stale eligibility.",
        confirmation: exact ? job.id : undefined,
      },
      async (key) => {
        const result =
          operation === "cancel"
            ? await client.deploymentCancel(projectID, job.id, key)
            : operation === "retry"
            ? await client.deploymentRetry(projectID, job.id, key)
            : await client.previewCleanup(projectID, job.id, key, "manual");
        data.mergeDeployment(result);
        await data.refreshDeployments();
        return operation + " returned factual state " + (result.rollout_state || result.status) + " for " + result.id + ".";
      }
    );
  }

  return (
    <aside className="detailPanel space-y-6" aria-label="Selected deployment detail">
      <div className="detailHeading">
        <div>
          <p className="eyebrow">Deployment Detail</p>
          <h2>{service?.name ?? job.service_id}</h2>
          <p>
            {job.environment_id || "Environment not reported"} · {short(job.desired_digest || job.snapshot?.image.digest, 22)}
          </p>
        </div>
        <DeliveryStatus status={job.rollout_state || job.status} />
      </div>

      {polled.error ? (
        <p aria-live="polite" className="truthCallout">
          {polled.error}
        </p>
      ) : null}

      <section>
        <h3>Identity</h3>
        <dl className="evidenceGrid">
          <Evidence label="Deployment ID" mono value={job.id} />
          <Evidence label="BuildRecord ID" mono value={job.snapshot?.authority.build_record.id} />
          <Evidence label="Created" value={displayTime(job.created_at)} />
          <Evidence label="Finished" value={displayTime(job.finished_at)} />
        </dl>
      </section>

      <section>
        <h3>Artifact & Target</h3>
        <dl className="evidenceGrid">
          <Evidence label="Immutable reference" mono value={job.snapshot?.image.reference} />
          <Evidence label="Desired digest" mono value={job.desired_digest} />
          <Evidence label="Current digest" mono value={currentDigest} />
          <Evidence
            label="Runtime / node"
            value={(job.runtime_id || "Not reported") + " / " + (job.node_id || "Not reported")}
          />
        </dl>
      </section>

      {/* Preflight Snapshot */}
      {job.snapshot?.authority.expected_preflight_hash || job.warning_acknowledgements?.length ? (
        <section>
          <h3>Preflight Verification Snapshot</h3>
          <dl className="evidenceGrid">
            <Evidence
              label="Preflight Hash"
              mono
              value={job.snapshot?.authority.expected_preflight_hash || "Clean pass"}
            />
            <Evidence
              label="Warnings Acknowledged"
              value={
                job.warning_acknowledgements && job.warning_acknowledgements.length > 0
                  ? job.warning_acknowledgements.length + " acknowledged (" + job.warning_acknowledgements.join(", ") + ")"
                  : "0 warnings"
              }
            />
          </dl>
        </section>
      ) : null}

      <section>
        <h3>Rollout & Verification</h3>
        <dl className="evidenceGrid">
          <Evidence label="Rollout state" value={job.rollout_state || job.status} />
          <Evidence
            label="Attempt"
            value={String(job.attempt_count ?? 0) + " / " + String(job.max_attempts ?? "Not reported")}
          />
          <Evidence label="Application image ID" mono value={job.terminal_result?.application_image_id} />
          <Evidence
            label="Available replicas"
            value={job.terminal_result ? job.terminal_result.available_replicas : undefined}
          />
          <Evidence
            label="Readiness evidence"
            mono
            value={job.readiness_evidence_hash || job.terminal_result?.readiness_evidence_hash}
          />
        </dl>
      </section>

      {/* 5-Layer Dependency Verification */}
      {dependencies.length > 0 ? (
        <section className="space-y-3">
          <div className="flex items-center justify-between">
            <h3>Post-Deploy Dependency Verification</h3>
            <span className="text-xs text-on-surface-variant font-code-md">
              {dependencies.length} declared
            </span>
          </div>

          <div className="space-y-3">
            {dependencies.map((dep) => (
              <DependencyVerificationPanel
                applicationID={job.service_id}
                dependency={dep}
                deploymentJobID={job.id}
                environmentID={job.environment_id}
                key={dep.logical_name}
                projectID={projectID}
              />
            ))}
          </div>
        </section>
      ) : null}

      {job.failure_code ||
      job.rollback_eligible ||
      job.rollback_blocked_reason ||
      job.rollout_state === "rolled_back" ? (
        <section>
          <h3>Failure & Rollback</h3>
          <dl className="evidenceGrid">
            <Evidence
              label="Failure"
              value={
                job.failure_code
                  ? job.failure_code + ": " + (job.failure_message_redacted || "No redacted message")
                  : "No failure reported"
              }
            />
            <Evidence label="Known-good" mono value={job.known_good_id || job.terminal_result?.known_good_id} />
            <Evidence label="Previous digest" mono value={job.previous_digest} />
            <Evidence
              label="Rollback eligibility"
              value={job.rollback_eligible ? "Eligible" : job.rollback_blocked_reason || "Not eligible"}
            />
          </dl>
        </section>
      ) : null}

      <div className="detailActions">
        {canCancel ? (
          <button onClick={() => mutate("cancel")} type="button">
            Cancel Before Mutation
          </button>
        ) : null}
        {canRetry ? (
          <button onClick={() => mutate("retry")} type="button">
            Retry Same Job
          </button>
        ) : null}
        {job.rollback_eligible ? (
          <button onClick={() => console.actions.rollback(job.id)} type="button">
            Review Exact Rollback
          </button>
        ) : null}
        {canCleanup ? (
          <button onClick={() => mutate("cleanup")} type="button">
            Clean Up Preview
          </button>
        ) : null}
      </div>

      <section>
        <h3>Events</h3>
        {polled.events.length ? (
          <ol className="eventTimeline">
            {polled.events.map((event) => (
              <li key={event.id}>
                <span aria-hidden="true" />
                <div>
                  <strong>{event.step}</strong>
                  <p>{event.message_redacted}</p>
                  <small>
                    {displayTime(event.created_at)} · {event.progress_percent}% · attempt {event.attempt ?? 0}
                  </small>
                  <details>
                    <summary>Technical Evidence</summary>
                    <code>request {event.request_id || "not reported"}</code>
                  </details>
                </div>
              </li>
            ))}
          </ol>
        ) : (
          <Empty
            title="No events reported"
            text="The Local API has not returned DeploymentJob events for this selection."
          />
        )}
      </section>

      <details>
        <summary>Technical Evidence</summary>
        <dl className="evidenceGrid">
          <Evidence label="Intent hash" mono value={job.intent_hash} />
          <Evidence label="State hash" mono value={job.rollout_state_hash} />
          <Evidence label="Spec hash" mono value={job.spec_hash} />
          <Evidence label="Known-good hash" mono value={job.known_good_hash} />
        </dl>
      </details>
    </aside>
  );
}
