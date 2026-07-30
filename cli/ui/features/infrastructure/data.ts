"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, DeploymentPolicy, GitHubBinding, GitHubRepository, PlacementFacts, TopologyPlan } from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";

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

  return { data, source, error, load };
}
