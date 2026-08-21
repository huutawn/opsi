"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type {
  ApplicationCutover,
  ApplicationCutoverFinalization,
  ApplicationCutoverReview,
  ApplicationCutoverRollback,
  Backup,
  BootstrapSession,
  NodeRecord,
  PlacementFacts,
  Resource,
  ResourceBinding,
  Restore,
  RetainedStorage,
  ServiceRecord,
} from "@/lib/contracts/registry";
import type { ConsoleController } from "@/features/console/types";

export type InfrastructureCenterState = {
  resources: Resource[];
  bindings: ResourceBinding[];
  retainedStorages: RetainedStorage[];
  backups: Backup[];
  restores: Restore[];
  cutoverReviews: ApplicationCutoverReview[];
  cutovers: ApplicationCutover[];
  rollbacks: ApplicationCutoverRollback[];
  finalizations: ApplicationCutoverFinalization[];
  nodes: NodeRecord[];
  services: ServiceRecord[];
  sessions: BootstrapSession[];
  facts: PlacementFacts | null;
};

const emptyCenterState: InfrastructureCenterState = {
  resources: [],
  bindings: [],
  retainedStorages: [],
  backups: [],
  restores: [],
  cutoverReviews: [],
  cutovers: [],
  rollbacks: [],
  finalizations: [],
  nodes: [],
  services: [],
  sessions: [],
  facts: null,
};

export function useInfrastructureCenterData(console: ConsoleController) {
  const projectID = console.state.project?.id ?? "";
  const environmentID = console.route.environment ?? "";
  const client = useMemo(() => new LocalClient(), []);
  const sequence = useRef(0);
  const dataRef = useRef<InfrastructureCenterState>(emptyCenterState);
  const [data, setData] = useState<InfrastructureCenterState>(emptyCenterState);
  const [loading, setLoading] = useState(Boolean(projectID));
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    if (!projectID) return;
    const current = ++sequence.current;
    setError("");

    try {
      const [
        resourcesRes,
        bindingsRes,
        storageRes,
        backupsRes,
        restoresRes,
        cutoverReviewsRes,
        cutoversRes,
        rollbacksRes,
        finalizationsRes,
        nodesRes,
        servicesRes,
        sessionsRes,
        factsRes,
      ] = await Promise.allSettled([
        client.resources(projectID, environmentID || undefined),
        client.resourceBindings(projectID, environmentID || undefined),
        client.retainedStorages(projectID, environmentID || undefined),
        client.backups(projectID),
        client.restores(projectID),
        client.cutoverReviews(projectID),
        client.cutovers(projectID),
        client.cutoverRollbacks(projectID),
        client.cutoverFinalizations(projectID),
        client.nodes(projectID),
        client.services(projectID),
        client.bootstrapSessions(projectID),
        client.placementFacts(projectID),
      ]);

      if (current !== sequence.current) return;

      const prev = dataRef.current;
      const next: InfrastructureCenterState = {
        resources: resourcesRes.status === "fulfilled" ? resourcesRes.value : prev.resources,
        bindings: bindingsRes.status === "fulfilled" ? bindingsRes.value : prev.bindings,
        retainedStorages: storageRes.status === "fulfilled" ? storageRes.value : prev.retainedStorages,
        backups: backupsRes.status === "fulfilled" ? backupsRes.value : prev.backups,
        restores: restoresRes.status === "fulfilled" ? restoresRes.value : prev.restores,
        cutoverReviews: cutoverReviewsRes.status === "fulfilled" ? cutoverReviewsRes.value : prev.cutoverReviews,
        cutovers: cutoversRes.status === "fulfilled" ? cutoversRes.value : prev.cutovers,
        rollbacks: rollbacksRes.status === "fulfilled" ? rollbacksRes.value : prev.rollbacks,
        finalizations: finalizationsRes.status === "fulfilled" ? finalizationsRes.value : prev.finalizations,
        nodes: nodesRes.status === "fulfilled" ? nodesRes.value : prev.nodes,
        services: servicesRes.status === "fulfilled" ? servicesRes.value.services ?? [] : prev.services,
        sessions: sessionsRes.status === "fulfilled" ? sessionsRes.value.sessions ?? [] : prev.sessions,
        facts: factsRes.status === "fulfilled" ? factsRes.value : prev.facts,
      };

      dataRef.current = next;
      setData(next);
      setLoading(false);

      const failures = [resourcesRes, bindingsRes, storageRes, nodesRes].filter((r) => r.status === "rejected");
      if (failures.length > 0) {
        setError("Some infrastructure authorities are unreachable; cached factual state is preserved.");
      }
    } catch (cause) {
      if (current !== sequence.current) return;
      setLoading(false);
      setError(cause instanceof Error ? cause.message : "Failed to load infrastructure resources.");
    }
  }, [client, environmentID, projectID]);

  useEffect(() => {
    sequence.current++;
    queueMicrotask(() => {
      dataRef.current = emptyCenterState;
      setData(emptyCenterState);
      setLoading(Boolean(projectID));
      setError("");
      void load();
    });
  }, [projectID, environmentID, load]);

  // Periodic poll for active background operations (e.g. provisioning, backups, restores, cutovers)
  useEffect(() => {
    if (!projectID) return;
    const hasActiveOps =
      data.resources.some((r) => r.lifecycle === "provisioning" || r.lifecycle === "updating" || r.lifecycle === "deleting") ||
      data.backups.some((b) => b.lifecycle === "queued" || b.lifecycle === "leased" || b.lifecycle === "running") ||
      data.restores.some((rst) => rst.lifecycle === "queued" || rst.lifecycle === "leased" || rst.lifecycle === "running" || rst.lifecycle === "verifying") ||
      data.cutovers.some((c) => c.lifecycle === "queued" || c.lifecycle === "validating" || c.lifecycle === "applying" || c.lifecycle === "deploying" || c.lifecycle === "verifying") ||
      data.rollbacks.some((rb) => rb.lifecycle === "queued" || rb.lifecycle === "validating" || rb.lifecycle === "applying" || rb.lifecycle === "deploying" || rb.lifecycle === "verifying") ||
      data.finalizations.some((f) => f.lifecycle === "queued" || f.lifecycle === "validating" || f.lifecycle === "revoking_source_binding" || f.lifecycle === "verifying") ||
      data.retainedStorages.some((s) => s.lifecycle === "destroying");

    if (!hasActiveOps) return;

    const interval = window.setInterval(() => {
      if (!document.hidden) void load();
    }, 4000);

    return () => window.clearInterval(interval);
  }, [data, load, projectID]);

  return { data, loading, error, reload: load };
}
