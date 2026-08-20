"use client";

import { useMemo, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { PreflightPanel } from "@/features/dependencies/preflight-panel";
import { RealizationReviewDialog } from "@/features/dependencies/realization-review-panel";
import { deploymentPollInterval, terminalDeployment } from "@/features/delivery/polling-model";
import {
  deploymentStage,
  reviewFingerprint,
  reviewSubmissionKey,
  retryableReviewStates,
  type ReviewSubmitState,
} from "@/features/infrastructure/deployment-review-model";
import { LocalAPIError, LocalClient, type ResolvedDeploymentRequest } from "@/lib/api/local-client";
import {
  hashExposure,
  type BuildRecord,
  type DeploymentJob,
  type DeploymentPolicy,
  type DeploymentPreview,
  type ExposureSpec,
  type PlacementFacts,
  type PreflightCheck,
  type PreflightResult,
  type PublicRouteIntent,
  type ServiceRecord,
  type TopologyAssignment,
  type TopologyPlan,
} from "@/lib/contracts/registry";
import { deploymentAssignmentFor } from "@/lib/presentation/infrastructure/model";

type PhaseState = ReviewSubmitState | "pending" | "ready" | "skipped";
type Phase = { state: PhaseState; job?: DeploymentJob; error?: string; detail?: string };
type ReviewEntry = {
  service: ServiceRecord;
  assignment?: TopologyAssignment;
  build?: BuildRecord;
  request?: ResolvedDeploymentRequest;
  preview?: DeploymentPreview;
  publicRoute?: PublicRouteIntent;
  state: ReviewSubmitState | "ready";
  workloadPhase: Phase;
  routePhase: Phase;
  error?: string;
};

type Props = {
  builds: BuildRecord[];
  console: ConsoleController;
  environmentID: string;
  environmentName: string;
  facts: PlacementFacts;
  onLive: () => void;
  policies: DeploymentPolicy[];
  topology: TopologyPlan;
};

const staleReviewCodes = [
  "TOPOLOGY_REVIEW_STALE",
  "CONFIGURATION_REVIEW_STALE",
  "POLICY_REVIEW_STALE",
  "ROUTING_TOPOLOGY_CHANGED",
  "ROUTING_POLICY_CHANGED",
  "EXPOSURE_STATE_CONFLICT",
  "PREFLIGHT_REVIEW_STALE",
];

export function DeploymentReview({
  builds,
  console,
  environmentID,
  environmentName,
  facts,
  onLive,
  policies,
  topology,
}: Props) {
  const client = useMemo(() => new LocalClient(), []);
  const services = console.state.services.filter(
    (service) => service.type === "application" && facts.services.some((fact) => fact.id === service.id)
  );
  const assignments = useMemo(
    () =>
      new Map(
        topology.assignments
          .filter((assignment) => assignment.environment_id === environmentID)
          .map((assignment) => [assignment.service_key, assignment])
      ),
    [environmentID, topology]
  );
  const acceptedBuilds = (service: ServiceRecord) =>
    builds
      .filter((build) => build.service_id === service.id && build.service_key === service.name && build.build.status === "succeeded")
      .sort((a, b) => b.created_at.localeCompare(a.created_at));
  const latestBuild = (service: ServiceRecord) => acceptedBuilds(service)[0];
  const placed = services.filter((service) => assignments.has(service.name));

  const [selected, setSelected] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(placed.map((service) => [service.id, true]))
  );
  const [buildIDs, setBuildIDs] = useState<Record<string, string>>({});
  const [entries, setEntries] = useState<Record<string, ReviewEntry>>({});
  const [reviewID, setReviewID] = useState("");
  const [reviewAuthority, setReviewAuthority] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [acknowledgedWarnings, setAcknowledgedWarnings] = useState<Record<string, boolean>>({});
  const [realizationTarget, setRealizationTarget] = useState<ServiceRecord | null>(null);

  const authorityFingerprint = reviewFingerprint([
    environmentID,
    topology.revision,
    topology.plan_hash,
    ...placed.flatMap((service) => {
      const build = latestBuild(service);
      const config = service.configuration;
      return [
        service.id,
        build?.id,
        config?.revision,
        config?.state_hash,
        policies.map((policy) => policy.id + ":" + policy.revision + ":" + policy.policy_hash).join(","),
      ];
    }),
  ]);

  const reviewStale = Boolean(reviewID && reviewAuthority && authorityFingerprint !== reviewAuthority);

  // Aggregate Preflight across all reviewed entries
  const aggregatedPreflight: PreflightResult | undefined = useMemo(() => {
    const validEntries = Object.values(entries).filter((e) => e.preview?.preflight);
    if (!validEntries.length) return undefined;

    const allChecks: PreflightCheck[] = [];
    const seenCheckIds = new Set<string>();
    let status: "PASS" | "PASS_WITH_WARNINGS" | "BLOCKED" = "PASS";
    let hash = "";
    let authority = "";

    for (const e of validEntries) {
      const pf = e.preview?.preflight;
      if (!pf) continue;
      hash = pf.preflight_hash || hash;
      authority = pf.authority_fingerprint || authority;
      if (pf.status === "BLOCKED") status = "BLOCKED";
      else if (pf.status === "PASS_WITH_WARNINGS" && status !== "BLOCKED") status = "PASS_WITH_WARNINGS";

      for (const chk of pf.checks) {
        if (!seenCheckIds.has(chk.id)) {
          seenCheckIds.add(chk.id);
          allChecks.push(chk);
        }
      }
    }

    return {
      status,
      checks: allChecks,
      authority_fingerprint: authority,
      preflight_hash: hash,
      generated_at: new Date().toISOString(),
    };
  }, [entries]);

  const warningChecks = useMemo(
    () => aggregatedPreflight?.checks.filter((c) => c.severity === "WARN") ?? [],
    [aggregatedPreflight]
  );
  const blockChecks = useMemo(
    () => aggregatedPreflight?.checks.filter((c) => c.severity === "BLOCK") ?? [],
    [aggregatedPreflight]
  );
  const unacknowledgedWarnings = useMemo(
    () => warningChecks.filter((c) => !acknowledgedWarnings[c.id]),
    [warningChecks, acknowledgedWarnings]
  );

  function toggle(serviceID: string) {
    setSelected((current) => ({ ...current, [serviceID]: !current[serviceID] }));
    resetReview();
  }

  function resetReview() {
    setEntries({});
    setReviewID("");
    setReviewAuthority("");
    setMessage("");
    setAcknowledgedWarnings({});
  }

  function updateEntry(serviceID: string, update: (entry: ReviewEntry) => ReviewEntry) {
    setEntries((current) => (current[serviceID] ? { ...current, [serviceID]: update(current[serviceID]) } : current));
  }

  function handleAcknowledgeWarning(id: string, checked: boolean) {
    setAcknowledgedWarnings((current) => ({ ...current, [id]: checked }));
  }

  function handleRemediate(code: string, check: PreflightCheck) {
    if (code === "INCLUDE_DEPENDENCY_TARGET" && check.target_safe_id) {
      const targetService = services.find((s) => s.id === check.target_safe_id || s.name === check.target_safe_id);
      if (targetService) {
        setSelected((curr) => ({ ...curr, [targetService.id]: true }));
        setMessage("Included " + targetService.name + " in deployment set. Click \"Review selected\" to evaluate.");
        resetReview();
      }
    } else if (code === "CREATE_BUILD" || code === "REBUILD_REQUIRED") {
      console.navigate({ view: "delivery", tab: "builds" });
    } else if (code === "PLAN_PLACEMENT") {
      console.navigate({ view: "infrastructure", tab: "topology" });
    } else if (code === "WAIT_FOR_RESOURCE") {
      console.navigate({ view: "infrastructure", tab: "resources" });
    } else if (code === "CONFIGURE_EXPOSURE") {
      console.navigate({ view: "delivery", tab: "exposure" });
    } else if (code === "REALIZE_DEPENDENCY") {
      const consumer = services.find((s) => s.id === check.scope_id);
      if (consumer) setRealizationTarget(consumer);
    }
  }

  async function review() {
    const reviewKey = crypto.randomUUID();
    const chosen = placed.filter((service) => selected[service.id]);
    if (!chosen.length) {
      setMessage("Select at least one placed application.");
      return;
    }
    setBusy(true);
    setMessage("");
    setEntries({});
    setReviewAuthority("");
    setAcknowledgedWarnings({});

    const batchBuildIDs = chosen
      .map((service) => (builds.find((item) => item.id === buildIDs[service.id]) ?? latestBuild(service))?.id)
      .filter(Boolean) as string[];

    const next: Record<string, ReviewEntry> = {};
    await Promise.all(
      chosen.map(async (service) => {
        const assignment = assignments.get(service.name);
        const build = builds.find((item) => item.id === buildIDs[service.id]) ?? latestBuild(service);
        const config = service.configuration;

        if (!assignment) {
          next[service.id] = blocked(service, "No applied topology assignment exists in the current environment.");
          return;
        }
        if (!build) {
          next[service.id] = blocked(service, "No succeeded accepted BuildRecord is available.");
          return;
        }
        if (!config || !config.state_hash || config.state_hash.length !== 64) {
          next[service.id] = blocked(service, "Service configuration revision/hash is unavailable; apply configuration first.");
          return;
        }

        const request: ResolvedDeploymentRequest = {
          schema_version: "opsi.deployment_job/v1",
          build_record_id: build.id,
          environment_id: environmentID,
          expected_topology_revision: topology.revision,
          expected_topology_hash: topology.plan_hash,
          expected_configuration_revision: config.revision,
          expected_configuration_state_hash: config.state_hash,
          deployment_batch: batchBuildIDs,
        };
        const publicRoute = assignment.exposure.mode === "public" ? config.public_route : undefined;

        try {
          const preview = await client.deploymentPreview(facts.project_id, request);
          const authority = preview.snapshot.authority;
          const reviewedRequest: ResolvedDeploymentRequest = {
            ...request,
            expected_deployment_policy_revision: authority.deployment_policy_revision,
            expected_deployment_policy_hash: authority.deployment_policy_hash,
            expected_preflight_hash: preview.preflight?.preflight_hash,
          };
          const ready = preview.eligible && preview.preflight?.status !== "BLOCKED";
          next[service.id] = {
            service,
            assignment,
            build,
            request: reviewedRequest,
            preview,
            publicRoute,
            state: ready ? "ready" : "blocked",
            workloadPhase: { state: ready ? "ready" : "blocked", error: ready ? undefined : preview.message },
            routePhase: publicRoute
              ? { state: ready ? "ready" : "blocked", detail: publicRoute.hostname + publicRoute.path }
              : { state: "skipped", detail: "No public route requested" },
            error: ready ? undefined : preview.message,
          };
        } catch (error) {
          const detail = error instanceof Error ? error.message : "Cloud rejected deployment review.";
          next[service.id] = {
            service,
            assignment,
            build,
            request,
            publicRoute,
            state: "blocked",
            workloadPhase: { state: "blocked", error: detail },
            routePhase: publicRoute
              ? { state: "blocked", error: detail }
              : { state: "skipped", detail: "No public route requested" },
            error: detail,
          };
        }
      })
    );

    setEntries(next);
    setReviewID(reviewKey);
    setReviewAuthority(authorityFingerprint);
    setBusy(false);
  }

  async function submit() {
    if (reviewStale) {
      resetReview();
      setMessage("Environment, topology, configuration, BuildRecord, or policy changed. Review deployment again.");
      return;
    }
    if (blockChecks.length > 0) {
      setMessage("Deployment is blocked by preflight safety checks. Resolve blockers before deploying.");
      return;
    }
    if (unacknowledgedWarnings.length > 0) {
      setMessage("Acknowledge all preflight warnings before proceeding with deployment.");
      return;
    }

    const reviewKey = reviewID;
    if (!reviewKey || !Object.values(entries).some((entry) => entry.state === "ready" || retryableReviewStates(entry.state))) return;

    setBusy(true);
    setMessage("");
    let created = 0;
    let reviewClosed = false;
    const submitted: Array<{ entry: ReviewEntry; job: DeploymentJob }> = [];
    const acks = Object.keys(acknowledgedWarnings).filter((k) => acknowledgedWarnings[k]);

    try {
      for (const entry of Object.values(entries)) {
        if (!entry.request || (entry.state !== "ready" && !retryableReviewStates(entry.state))) continue;

        const finalRequest: ResolvedDeploymentRequest = {
          ...entry.request,
          warning_acknowledgements: acks,
        };

        updateEntry(entry.service.id, (current) => ({
          ...current,
          state: "queued",
          workloadPhase: { ...current.workloadPhase, state: "queued", error: undefined },
          routePhase: current.publicRoute
            ? { ...current.routePhase, state: "pending", detail: "Waiting for workload success" }
            : current.routePhase,
          error: undefined,
        }));

        try {
          const job = await client.deploymentApply(
            facts.project_id,
            finalRequest,
            reviewSubmissionKey(reviewKey, entry.service.id)
          );
          created++;
          submitted.push({ entry, job });
          updateEntry(entry.service.id, (current) => ({
            ...current,
            workloadPhase: { state: terminalDeployment(job) ? phaseState(job) : "queued", job },
          }));
        } catch (error) {
          if (closeStaleReview(error)) {
            reviewClosed = true;
            break;
          }
          const detail = error instanceof Error ? error.message : "Deployment submission failed.";
          updateEntry(entry.service.id, (current) => ({
            ...current,
            state: "failed",
            workloadPhase: { ...current.workloadPhase, state: "failed", error: detail },
            error: detail,
          }));
        }
      }

      const results = await Promise.all(submitted.map(({ entry, job }) => finishPhases(entry, job, reviewKey)));
      reviewClosed = reviewClosed || results.includes("stale");
      if (created) {
        try {
          await console.actions.load();
        } catch (error) {
          setMessage(error instanceof Error ? "Jobs finished, but refresh failed: " + error.message : "Jobs finished, but refresh failed.");
        }
        if (!reviewClosed) onLive();
      }
    } finally {
      setBusy(false);
    }
  }

  async function finishPhases(entry: ReviewEntry, initial: DeploymentJob, reviewKey: string) {
    try {
      const workload = await waitForTerminalDeployment(client, facts.project_id, initial);
      if ((workload.rollout_state || workload.status) !== "succeeded") {
        const detail = workload.failure_message_redacted || workload.failure_code || deploymentStage(workload);
        updateEntry(entry.service.id, (current) => ({
          ...current,
          state: "failed",
          workloadPhase: { state: "failed", job: workload, error: detail },
          routePhase: current.publicRoute
            ? { state: "blocked", error: "Workload did not succeed; route was not published." }
            : current.routePhase,
          error: detail,
        }));
        return;
      }
      updateEntry(entry.service.id, (current) => ({
        ...current,
        state: current.publicRoute ? "queued" : "succeeded",
        workloadPhase: { state: "succeeded", job: workload },
        routePhase: current.publicRoute
          ? { ...current.routePhase, state: "queued", detail: "Previewing factual route diff" }
          : current.routePhase,
      }));
      if (!entry.publicRoute) return;

      await assertRouteAuthority(client, facts.project_id, entry, topology);
      const deploymentID = "exp-" + workload.id;
      const servicePort = workload.snapshot?.workload.container_port ?? entry.service.container_port;
      if (!workload.runtime_id || !servicePort)
        throw new Error("Workload runtime identity or service port is unavailable for route publication.");

      const draft: Omit<ExposureSpec, "spec_hash"> = {
        schema_version: "opsi.exposure_spec/v1",
        project_id: facts.project_id,
        environment_id: environmentID,
        runtime_id: workload.runtime_id,
        service_key: workload.snapshot?.workload.service_key ?? entry.service.name,
        deployment_job_id: deploymentID,
        hostname: entry.publicRoute.hostname.toLowerCase(),
        path: normalizeRoutePath(entry.publicRoute.path),
        service_port: servicePort,
        tls: { mode: "disabled" },
      };
      const request = {
        schema_version: "opsi.exposure_mutation/v1" as const,
        base_deployment_job_id: workload.id,
        exposure: { ...draft, spec_hash: await hashExposure(draft) },
      };
      const preview = await client.exposurePreview(facts.project_id, request);
      if (preview.changes.length === 1 && preview.changes[0] === "unchanged") {
        updateEntry(entry.service.id, (current) => ({
          ...current,
          state: "succeeded",
          routePhase: { state: "succeeded", detail: "Existing route already matches; no rollout created." },
        }));
        return;
      }
      const routeJob = await client.exposureApply(
        facts.project_id,
        { ...request, expected_state_hash: preview.state_hash, exposure: preview.desired },
        reviewSubmissionKey(reviewKey, entry.service.id) + ":route"
      );
      updateEntry(entry.service.id, (current) => ({
        ...current,
        routePhase: { state: terminalDeployment(routeJob) ? phaseState(routeJob) : "queued", job: routeJob },
      }));
      const terminal = await waitForTerminalDeployment(client, facts.project_id, routeJob);
      if ((terminal.rollout_state || terminal.status) === "succeeded")
        updateEntry(entry.service.id, (current) => ({
          ...current,
          state: "succeeded",
          routePhase: { state: "succeeded", job: terminal },
        }));
      else {
        const detail = terminal.failure_message_redacted || terminal.failure_code || deploymentStage(terminal);
        updateEntry(entry.service.id, (current) => ({
          ...current,
          state: "failed",
          routePhase: { state: "failed", job: terminal, error: detail },
          error: "Workload is running; route publish failed: " + detail,
        }));
      }
    } catch (error) {
      if (closeStaleReview(error)) return "stale" as const;
      const detail = error instanceof Error ? error.message : "Deployment phase failed.";
      updateEntry(entry.service.id, (current) => ({
        ...current,
        state: "failed",
        routePhase: current.publicRoute ? { ...current.routePhase, state: "failed", error: detail } : current.routePhase,
        error: current.publicRoute ? "Workload state is preserved; route publish failed: " + detail : detail,
      }));
    }
  }

  function closeStaleReview(error: unknown) {
    if (!(error instanceof LocalAPIError) || !staleReviewCodes.includes(error.code)) return false;
    setEntries({});
    setReviewID("");
    setReviewAuthority("");
    setAcknowledgedWarnings({});
    setMessage("Authority or preflight facts changed after review. Review deployment again.");
    return true;
  }

  const canDeploy =
    !busy &&
    !reviewStale &&
    blockChecks.length === 0 &&
    unacknowledgedWarnings.length === 0 &&
    Object.values(entries).some((entry) => entry.state === "ready" || retryableReviewStates(entry.state));

  if (!environmentID)
    return (
      <section className="deploymentReview" aria-labelledby="deployment-review-heading">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Deployment authority</p>
            <h3 id="deployment-review-heading">Review deployment</h3>
          </div>
        </div>
        <p className="truthCallout" role="alert">
          Choose the current environment before reviewing or deploying. Opsi will not select one from multiple environments.
        </p>
      </section>
    );

  if (!placed.length)
    return (
      <section className="deploymentReview" aria-labelledby="deployment-review-heading">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Deployment authority</p>
            <h3 id="deployment-review-heading">Review deployment</h3>
          </div>
        </div>
        <p className="muted">
          No application is placed in {environmentName}. Apply a TopologyPlan assignment for this environment first.
        </p>
      </section>
    );

  return (
    <section className="deploymentReview space-y-6" aria-labelledby="deployment-review-heading">
      <div className="sectionHeading">
        <div>
          <p className="eyebrow">Deployment authority · {environmentName}</p>
          <h3 id="deployment-review-heading">Review Deployment & Preflight</h3>
          <p>
            Cloud compiles immutable WorkloadSpecs and executes Unified Preflight across builds, placement, dependency contracts, routing, and source risk scans.
          </p>
        </div>
        <span>{placed.length} placed</span>
      </div>

      <div className="deploymentReviewToolbar flex items-center justify-between gap-4">
        <label className="flex items-center gap-2 cursor-pointer text-xs font-semibold text-on-surface">
          <input
            aria-label="Select all placed applications"
            checked={placed.every((service) => selected[service.id])}
            onChange={(event) => {
              setSelected(Object.fromEntries(placed.map((service) => [service.id, event.target.checked])));
              resetReview();
            }}
            type="checkbox"
          />
          <span>Select all placed</span>
        </label>

        <div className="flex items-center gap-3">
          <button className="secondaryAction" disabled={busy} onClick={() => void review()} type="button">
            {busy ? "Evaluating Preflight…" : "Review selected"}
          </button>
          <button className="primary" disabled={!canDeploy} onClick={() => void submit()} type="button">
            Deploy
          </button>
        </div>
      </div>

      {reviewStale || message ? (
        <div className="truthCallout p-3 rounded-xl bg-status-warning/10 border border-status-warning/30 text-xs text-status-warning flex items-center justify-between" role="status">
          <span>{reviewStale ? "Deployment facts or preflight changed. Refresh review." : message}</span>
          {reviewStale ? (
            <Button size="sm" variant="secondary" onClick={() => void review()}>
              Refresh Review
            </Button>
          ) : null}
        </div>
      ) : null}

      {/* Preflight Results Panel */}
      {aggregatedPreflight ? (
        <PreflightPanel
          acknowledgedWarnings={acknowledgedWarnings}
          onAcknowledgeWarning={handleAcknowledgeWarning}
          onRemediate={handleRemediate}
          preflight={aggregatedPreflight}
        />
      ) : null}

      {/* Deployment Set Rows */}
      <ul className="deploymentReviewList space-y-3">
        {placed.map((service) => (
          <ReviewRow
            builds={acceptedBuilds(service)}
            buildID={buildIDs[service.id]}
            entry={reviewStale ? undefined : entries[service.id]}
            environmentName={environmentName}
            key={service.id}
            onBuild={(id) => {
              setBuildIDs((current) => ({ ...current, [service.id]: id }));
              resetReview();
            }}
            onToggle={() => toggle(service.id)}
            selected={Boolean(selected[service.id])}
            service={service}
          />
        ))}
      </ul>

      {/* Realization Modal if remediated from Preflight */}
      {realizationTarget ? (
        <RealizationReviewDialog
          consumer={realizationTarget}
          onApplied={async () => {
            await console.actions.load();
            setRealizationTarget(null);
            void review();
          }}
          onClose={() => setRealizationTarget(null)}
          projectID={facts.project_id}
        />
      ) : null}
    </section>
  );
}

function blocked(service: ServiceRecord, error: string): ReviewEntry {
  return {
    service,
    state: "blocked",
    workloadPhase: { state: "blocked", error },
    routePhase: { state: "blocked", error },
    error,
  };
}

function ReviewRow({
  builds,
  buildID,
  entry,
  environmentName,
  onBuild,
  onToggle,
  selected,
  service,
}: {
  builds: BuildRecord[];
  buildID?: string;
  entry?: ReviewEntry;
  environmentName: string;
  onBuild: (id: string) => void;
  onToggle: () => void;
  selected: boolean;
  service: ServiceRecord;
}) {
  const selectedBuild = builds.find((build) => build.id === buildID) ?? builds[0];
  const snapshot = entry?.preview?.snapshot;
  const workload = snapshot?.workload;
  const localError = reviewBlockReason(service, selectedBuild);
  const fallbackState = localError ? "blocked" : "pending";

  return (
    <li className="deploymentReviewRow" data-state={entry?.state ?? fallbackState}>
      <label className="deploymentReviewSelect">
        <input aria-label={"Select " + service.name} checked={selected} onChange={onToggle} type="checkbox" />
        <strong>{service.name}</strong>
        <small>{environmentName}</small>
      </label>
      <label className="deploymentReviewBuild">
        Accepted BuildRecord
        <select
          aria-label={service.name + " BuildRecord"}
          disabled={!builds.length}
          onChange={(event) => onBuild(event.target.value)}
          value={selectedBuild?.id ?? ""}
        >
          {builds.length ? (
            builds.map((build) => (
              <option key={build.id} value={build.id}>
                {build.id} · {build.build.oci_digest.slice(0, 18)}
              </option>
            ))
          ) : (
            <option value="">No succeeded build</option>
          )}
        </select>
      </label>
      <span className="deploymentReviewMeta">
        {workload && snapshot ? (
          <>
            <code title={snapshot.image.digest}>{snapshot.image.digest}</code>
            <small>
              {snapshot.authority.runtime_id} · {workload.replicas} replicas · {workload.resources.requests.cpu} / {workload.resources.requests.memory}
            </small>
            <small>
              probes {workload.readiness_probe?.path || "none"} / {workload.liveness_probe?.path || "none"}
            </small>
            <small>
              env {workload.environment?.map((item) => item.name + "=" + item.value).join(", ") || "none"}
            </small>
            <small>
              configuration revision {snapshot.authority.service_configuration_revision ?? "unavailable"}
            </small>
            <small>
              route {entry.publicRoute ? entry.publicRoute.hostname + entry.publicRoute.path : "internal only"}
            </small>
          </>
        ) : (
          <small>{entry?.error || localError || "Review to resolve immutable workload."}</small>
        )}
      </span>
      <span className="deploymentReviewStatus">
        <PhaseStatus label="Deploy workload" phase={entry?.workloadPhase ?? { state: fallbackState, error: localError }} />
        <PhaseStatus
          label="Publish route"
          phase={entry?.routePhase ?? { state: "pending", detail: "Review to resolve route intent" }}
        />
      </span>
    </li>
  );
}

function PhaseStatus({ label, phase }: { label: string; phase: Phase }) {
  return (
    <span className="deploymentPhase">
      <b>{label}</b>
      <StatusBadge value={phase.state === "ready" ? "reviewed" : phase.state} />
      <small>{phase.job ? deploymentStage(phase.job) + " · " + phase.job.id : phase.error || phase.detail || phase.state}</small>
    </span>
  );
}

function reviewBlockReason(service: ServiceRecord, build?: BuildRecord) {
  if (!build) return "No succeeded accepted BuildRecord is available.";
  if (!service.configuration?.state_hash || service.configuration.state_hash.length !== 64)
    return "Service configuration revision/hash is unavailable; apply configuration first.";
  return "";
}

async function assertRouteAuthority(
  client: LocalClient,
  projectID: string,
  entry: ReviewEntry,
  reviewedTopology: TopologyPlan
) {
  const [services, topology] = await Promise.all([client.services(projectID), client.topology(projectID)]);
  const service = services.services.find((item) => item.id === entry.service.id);
  const assignment = deploymentAssignmentFor(topology, entry.service.name, entry.assignment?.environment_id ?? "");
  if (
    !service?.configuration ||
    service.configuration.state_hash !== entry.service.configuration?.state_hash ||
    topology.revision !== reviewedTopology.revision ||
    topology.plan_hash !== reviewedTopology.plan_hash ||
    assignment?.runtime_id !== entry.assignment?.runtime_id ||
    assignment?.exposure.mode !== "public"
  ) {
    const error = new LocalAPIError("Public route, configuration, or topology changed after review.");
    error.status = 409;
    error.code = "CONFIGURATION_REVIEW_STALE";
    error.nextAction = "review_again";
    throw error;
  }
}

async function waitForTerminalDeployment(client: LocalClient, projectID: string, initial: DeploymentJob) {
  let current = initial;
  for (let attempt = 0; attempt < 120; attempt++) {
    if (terminalDeployment(current)) return current;
    current = await client.deployment(projectID, current.id);
    if (terminalDeployment(current)) return current;
    await new Promise((resolve) => window.setTimeout(resolve, deploymentPollInterval));
  }
  throw new Error("Deployment " + current.id + " did not reach a terminal state within 10 minutes.");
}

function phaseState(job: DeploymentJob): "succeeded" | "failed" {
  return (job.rollout_state || job.status) === "succeeded" ? "succeeded" : "failed";
}
function normalizeRoutePath(path: string) {
  return path !== "/" && path.endsWith("/") ? path.slice(0, -1) : path;
}
