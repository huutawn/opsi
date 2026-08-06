"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentPolicy, GitHubBinding, GitHubRepository, PlacementFacts, TopologyPlan } from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";
import { bootstrapPollInterval, latestActiveBootstrap, serverLifecycle, terminalBootstrap } from "@/lib/presentation/infrastructure/model";

export type SourceState = "loading" | "ready" | "empty" | "unavailable";
export type InfrastructureData = {
  facts: PlacementFacts | null;
  topology: TopologyPlan | null;
  repositories: GitHubRepository[];
  bindings: GitHubBinding[];
  builds: BuildRecord[];
  policies: DeploymentPolicy[];
};

const emptyData: InfrastructureData = { facts: null, topology: null, repositories: [], bindings: [], builds: [], policies: [] };

export function useInfrastructureData(console: ConsoleController) {
  const projectID = console.state.project?.id ?? "";
  const client = useMemo(() => new LocalClient(), []);
  const sequence = useRef(0);
  const dataRef = useRef<InfrastructureData>(emptyData);
  const [data, setData] = useState<InfrastructureData>(emptyData);
  const [source, setSource] = useState<SourceState>(projectID ? "loading" : "empty");
  const [error, setError] = useState("");
  const refreshBootstrap = useRef(console.actions.refreshBootstrap);
  const pollingScope = useRef("");
  useEffect(() => { refreshBootstrap.current = console.actions.refreshBootstrap; }, [console.actions.refreshBootstrap]);
  const scope = `${projectID}:${console.route.tab}`;
  useEffect(() => {
    pollingScope.current = scope;
    return () => { if (pollingScope.current === scope) pollingScope.current = ""; };
  }, [scope]);

  const load = useCallback(async () => {
    if (!projectID) return;
    const current = ++sequence.current;
    const previous = dataRef.current;
    setSource((state) => (previous.facts ? state : "loading"));
    setError("");
    const [facts, topology, repositories, bindings, builds, policies] = await Promise.allSettled([
      client.placementFacts(projectID),
      client.topology(projectID),
      client.githubRepositories(projectID),
      client.githubBindings(projectID),
      client.buildRecords(projectID),
      client.deploymentPolicies(projectID),
    ]);
    if (current !== sequence.current) return;
    const next = {
      facts: facts.status === "fulfilled" ? facts.value : previous.facts,
      topology: topology.status === "fulfilled" ? topology.value : topology.status === "rejected" && topology.reason?.status === 404 ? null : previous.topology,
      repositories: repositories.status === "fulfilled" ? repositories.value.repositories ?? [] : previous.repositories,
      bindings: bindings.status === "fulfilled" ? bindings.value.bindings ?? [] : previous.bindings,
      builds: builds.status === "fulfilled" ? builds.value.records ?? [] : previous.builds,
      policies: policies.status === "fulfilled" ? policies.value.policies ?? [] : previous.policies,
    } satisfies InfrastructureData;
    dataRef.current = next;
    setData(next);
    const failed = [facts, repositories, bindings].some((result) => result.status === "rejected");
    setSource(next.facts ? failed ? "unavailable" : next.facts.runtimes.length || next.facts.environments.length ? "ready" : "empty" : "unavailable");
    if (failed) setError("Some infrastructure sources are unavailable; factual data already loaded is preserved.");
  }, [client, projectID]);

  useEffect(() => {
    sequence.current++;
    queueMicrotask(() => {
      dataRef.current = emptyData;
      setData(emptyData);
      setSource(projectID ? "loading" : "empty");
      setError("");
      void load();
    });
  }, [projectID]); // eslint-disable-line react-hooks/exhaustive-deps

  const consoleFacts = console.state.foundation.placement;
  useEffect(() => {
    if (!consoleFacts || consoleFacts.project_id !== projectID) return;
    queueMicrotask(() => {
      const next = { ...dataRef.current, facts: consoleFacts };
      dataRef.current = next;
      setData(next);
      setSource((state) => state === "unavailable" ? state : consoleFacts.runtimes.length || consoleFacts.environments.length ? "ready" : "empty");
    });
  }, [consoleFacts, projectID]);

  const consoleTopology = console.state.foundation.topology;
  useEffect(() => {
    if (!consoleTopology || consoleTopology.project_id !== projectID) return;
    queueMicrotask(() => {
      const next = { ...dataRef.current, topology: consoleTopology };
      dataRef.current = next;
      setData(next);
    });
  }, [consoleTopology, projectID]);

  const currentFacts = consoleFacts?.project_id === projectID ? consoleFacts : data.facts;
  const factualServerReady = currentFacts ? serverLifecycle(currentFacts, []).status === "Ready" : false;
  const activeBootstrapID = factualServerReady ? "" : latestActiveBootstrap(console.state.sessions)?.id ?? "";
  useEffect(() => {
    if (!projectID || console.route.tab !== "topology" || !activeBootstrapID) return;
    let disposed = false;
    let inFlight = false;
    let timer = 0;
    async function refresh() {
      if (disposed || inFlight) return;
      inFlight = true;
      try {
        const updated = await refreshBootstrap.current(activeBootstrapID);
        if (pollingScope.current !== scope) return;
        await load();
        if (!disposed && !terminalBootstrap(updated)) timer = window.setTimeout(refresh, bootstrapPollInterval);
      } catch (reason) {
        if (!disposed) {
          setError(reason instanceof Error ? `Bootstrap refresh failed: ${reason.message}` : "Bootstrap refresh failed; last factual state is preserved.");
          timer = window.setTimeout(refresh, bootstrapPollInterval);
        }
      } finally {
        inFlight = false;
      }
    }
    void refresh();
    return () => {
      disposed = true;
      window.clearTimeout(timer);
    };
  }, [activeBootstrapID, console.route.tab, load, projectID, scope]);

  return { data, source, error, load };
}
