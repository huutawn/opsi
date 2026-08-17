"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type {
  BuildJob,
  BuildRecord,
  BuildRecordList,
  DeploymentJob,
  DeploymentPolicy,
  GitHubBinding,
  GitHubInstallation,
  GitHubRepository,
  PlacementFacts,
  ServiceRecord,
  TopologyPlan,
} from "@/lib/contracts/registry";
import { terminalBuild } from "@/lib/presentation/build";
import { isTerminal } from "@/lib/presentation/delivery/model";

export type DeliverySource = "ready" | "loading" | "unavailable";

export type DeliveryData = {
  services: ServiceRecord[];
  builds: BuildRecord[];
  buildResults: BuildRecordList;
  buildJobs: Record<string, BuildJob[]>;
  buildJobsError: string;
  deployments: DeploymentJob[];
  exposures: DeploymentJob[];
  bindings: GitHubBinding[];
  installations: GitHubInstallation[];
  repositories: GitHubRepository[];
  policies: DeploymentPolicy[];
  placement: PlacementFacts | null;
  topology: TopologyPlan | null;
  sourceState: DeliverySource;
  buildState: DeliverySource;
  deploymentState: DeliverySource;
  exposureState: DeliverySource;
  sourceError: string;
  buildError: string;
  deploymentError: string;
  exposureError: string;
  hasLoaded: boolean;
  loadBuilds: (filters?: { serviceKey?: string; repositoryID?: string; sha?: string; status?: string; cursor?: string }) => Promise<void>;
  loadBuildJobs: (targetServices?: ServiceRecord[]) => Promise<void>;
  createBuild: (service: ServiceRecord, idempotencyKey: string) => Promise<BuildJob>;
  refreshDeployments: () => Promise<void>;
  mergeDeployment: (job: DeploymentJob) => void;
};

export function useDeliveryData(projectID: string, services: ServiceRecord[], initialDeployments: DeploymentJob[]): DeliveryData {
  const client = useMemo(() => new LocalClient(), []);
  const [state, setState] = useState(() => emptyState(services, initialDeployments));
  const [buildJobs, setBuildJobs] = useState<Record<string, BuildJob[]>>({});
  const [buildJobsError, setBuildJobsError] = useState("");
  const sequence = useRef(0);
  const applications = useMemo(() => services.filter((s) => s.type === "application"), [services]);

  const loadBuilds = useCallback(async (filters = {}) => {
    if (!projectID) return;
    setState((current) => ({ ...current, buildState: "loading", buildError: "" }));
    try {
      const result = await client.buildRecords(projectID, filters);
      setState((current) => ({ ...current, builds: result.records ?? current.builds, buildResults: result, buildState: "ready", buildError: "" }));
    } catch (error) {
      setState((current) => ({ ...current, buildState: "unavailable", buildError: message(error, "BuildRecord inventory is unavailable.") }));
    }
  }, [client, projectID]);

  const loadBuildJobs = useCallback(async (targetServices: ServiceRecord[] = applications) => {
    if (!projectID || targetServices.length === 0) return;
    const current = ++sequence.current;
    const results = await Promise.allSettled(
      targetServices.map(async (service) => [service.id, (await client.buildJobs(projectID, service.id)).build_jobs ?? []] as const)
    );
    if (current !== sequence.current) return;
    setBuildJobs(Object.fromEntries(results.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []))));
    setBuildJobsError(results.some((result) => result.status === "rejected") ? "Some BuildJob histories are unavailable; loaded factual state is preserved." : "");
  }, [applications, client, projectID]);

  const createBuild = useCallback(async (service: ServiceRecord, idempotencyKey: string) => {
    const job = await client.createBuildJob(projectID, service.id, idempotencyKey);
    setBuildJobs((current) => ({
      ...current,
      [service.id]: [job, ...(current[service.id] ?? []).filter((item) => item.id !== job.id)],
    }));
    return job;
  }, [client, projectID]);

  const refreshDeployments = useCallback(async () => {
    if (!projectID) return;
    const [deployments, exposures] = await Promise.allSettled([client.deployments(projectID), client.exposures(projectID)]);
    setState((current) => ({
      ...current,
      deployments: deployments.status === "fulfilled" ? deployments.value.deployments ?? [] : current.deployments,
      exposures: exposures.status === "fulfilled" ? exposures.value.exposures ?? [] : current.exposures,
      deploymentState: deployments.status === "fulfilled" ? "ready" : "unavailable",
      exposureState: exposures.status === "fulfilled" ? "ready" : "unavailable",
      deploymentError: deployments.status === "rejected" ? message(deployments.reason, "Deployment inventory is unavailable.") : "",
      exposureError: exposures.status === "rejected" ? message(exposures.reason, "Exposure inventory is unavailable.") : "",
    }));
  }, [client, projectID]);

  useEffect(() => {
    if (!projectID) return;
    let active = true;
    queueMicrotask(() => { if (active) setState(emptyState(services, initialDeployments)); });
    void Promise.allSettled([
      client.buildRecords(projectID),
      client.deployments(projectID),
      client.exposures(projectID),
      client.githubInstallations(projectID),
      client.githubRepositories(projectID),
      client.githubBindings(projectID),
      client.deploymentPolicies(projectID),
      client.placementFacts(projectID),
      client.topology(projectID),
    ]).then((results) => {
      if (!active) return;
      const [builds, deployments, exposures, installations, repositories, bindings, policies, placement, topology] = results;
      const sourceReady = installations.status === "fulfilled" && repositories.status === "fulfilled" && bindings.status === "fulfilled";
      setState((current) => ({
        ...current,
        services,
        builds: builds.status === "fulfilled" ? builds.value.records ?? [] : current.builds,
        buildResults: builds.status === "fulfilled" ? builds.value : current.buildResults,
        deployments: deployments.status === "fulfilled" ? deployments.value.deployments ?? [] : current.deployments,
        exposures: exposures.status === "fulfilled" ? exposures.value.exposures ?? [] : current.exposures,
        installations: installations.status === "fulfilled" ? installations.value.installations ?? [] : current.installations,
        repositories: repositories.status === "fulfilled" ? repositories.value.repositories ?? [] : current.repositories,
        bindings: bindings.status === "fulfilled" ? bindings.value.bindings ?? [] : current.bindings,
        policies: policies.status === "fulfilled" ? policies.value.policies ?? [] : current.policies,
        placement: placement.status === "fulfilled" ? placement.value : current.placement,
        topology: topology.status === "fulfilled" ? topology.value : current.topology,
        hasLoaded: true,
        sourceState: sourceReady ? "ready" : "unavailable",
        buildState: builds.status === "fulfilled" ? "ready" : "unavailable",
        deploymentState: deployments.status === "fulfilled" ? "ready" : "unavailable",
        exposureState: exposures.status === "fulfilled" ? "ready" : "unavailable",
        sourceError: sourceReady ? "" : "Source inventory is partially unavailable; loaded factual state is preserved.",
        buildError: builds.status === "rejected" ? message(builds.reason, "BuildRecord inventory is unavailable.") : "",
        deploymentError: deployments.status === "rejected" ? message(deployments.reason, "Deployment inventory is unavailable.") : "",
        exposureError: exposures.status === "rejected" ? message(exposures.reason, "Exposure inventory is unavailable.") : "",
      }));
    });
    return () => { active = false; };
  }, [client, initialDeployments, projectID, services]);

  // Initial load of build jobs
  useEffect(() => {
    const current = sequence.current;
    queueMicrotask(() => void loadBuildJobs());
    return () => { sequence.current = Math.max(sequence.current, current + 1); };
  }, [loadBuildJobs]);

  // Active polling when builds or deployments are running
  const hasActiveBuilds = Object.values(buildJobs).flat().some((job) => !terminalBuild(job));
  const hasActiveDeployments = state.deployments.some((job) => !isTerminal(job.rollout_state || job.status));
  const isPollingActive = hasActiveBuilds || hasActiveDeployments;

  useEffect(() => {
    if (!isPollingActive) return;
    const timer = window.setInterval(() => {
      if (hasActiveBuilds) {
        void loadBuildJobs().then(() => loadBuilds());
      }
      if (hasActiveDeployments) {
        void refreshDeployments();
      }
    }, 3000);
    return () => window.clearInterval(timer);
  }, [hasActiveBuilds, hasActiveDeployments, isPollingActive, loadBuildJobs, loadBuilds, refreshDeployments]);

  const mergeDeployment = useCallback((job: DeploymentJob) => {
    setState((current) => ({ ...current, deployments: mergeByID(current.deployments, job) }));
  }, []);

  return {
    ...state,
    buildJobs,
    buildJobsError,
    loadBuilds,
    loadBuildJobs,
    createBuild,
    refreshDeployments,
    mergeDeployment,
  };
}

function emptyState(services: ServiceRecord[], deployments: DeploymentJob[]) {
  return {
    services,
    builds: [] as BuildRecord[],
    buildResults: { records: [] } as BuildRecordList,
    deployments,
    exposures: [] as DeploymentJob[],
    bindings: [] as GitHubBinding[],
    installations: [] as GitHubInstallation[],
    repositories: [] as GitHubRepository[],
    policies: [] as DeploymentPolicy[],
    placement: null as PlacementFacts | null,
    topology: null as TopologyPlan | null,
    sourceState: "loading" as DeliverySource,
    buildState: "loading" as DeliverySource,
    deploymentState: "loading" as DeliverySource,
    exposureState: "loading" as DeliverySource,
    sourceError: "",
    buildError: "",
    deploymentError: "",
    exposureError: "",
    hasLoaded: false,
  };
}

function mergeByID(items: DeploymentJob[], job: DeploymentJob) {
  const index = items.findIndex((item) => item.id === job.id);
  if (index < 0) return [job, ...items];
  return items.map((item) => item.id === job.id ? job : item);
}

function message(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}
