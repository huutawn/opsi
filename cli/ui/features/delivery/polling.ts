"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { LocalClient } from "@/lib/api/local-client";
import type { DeploymentJob, TimelineEvent } from "@/lib/contracts/registry";
import { deploymentPollInterval, terminalDeployment } from "@/features/delivery/polling-model";
export { deploymentPollInterval, shouldPoll, terminalDeployment } from "@/features/delivery/polling-model";

export function useSelectedDeployment(projectID: string, deploymentID: string, initial: DeploymentJob | undefined, onUpdate: (job: DeploymentJob) => void) {
  const client = useMemo(() => new LocalClient(), []);
  const [job, setJob] = useState<DeploymentJob | null>(initial ?? null);
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [error, setError] = useState("");
  const key = `${projectID}:${deploymentID}`;
  const activeKey = useRef(key);
  const request = useRef(0);
  const applied = useRef(0);

  useEffect(() => {
    activeKey.current = key;
    request.current = 0;
    applied.current = 0;
    queueMicrotask(() => {
      if (activeKey.current !== key) return;
      setJob(initial ?? null);
      setEvents([]);
      setError("");
    });
    if (!projectID || !deploymentID) return;
    let timer = 0;
    let disposed = false;
    let stopped = terminalDeployment(initial);

    async function refresh() {
      if (disposed || document.hidden) return;
      const sequence = ++request.current;
      try {
        const [nextJob, nextEvents] = await Promise.all([client.deployment(projectID, deploymentID), client.deploymentEvents(projectID, deploymentID)]);
        if (disposed || activeKey.current !== key || sequence < applied.current) return;
        applied.current = sequence;
        setJob(nextJob);
        setEvents(nextEvents.events ?? []);
        setError("");
        if (terminalDeployment(nextJob)) { stopped = true; window.clearTimeout(timer); onUpdate(nextJob); }
      } catch (reason) {
        if (!disposed && activeKey.current === key) setError(reason instanceof Error ? reason.message : "Deployment refresh failed; last factual state is preserved.");
      }
    }

    function schedule() {
      window.clearTimeout(timer);
      if (!disposed && !stopped && !document.hidden) timer = window.setTimeout(async () => { await refresh(); schedule(); }, deploymentPollInterval);
    }

    function visibilityChanged() {
      if (document.hidden) window.clearTimeout(timer);
      else { void refresh(); schedule(); }
    }

    void refresh();
    schedule();
    document.addEventListener("visibilitychange", visibilityChanged);
    return () => {
      disposed = true;
      activeKey.current = "";
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", visibilityChanged);
    };
  }, [client, deploymentID, initial, key, onUpdate, projectID]);

  return { job, events, error };
}
