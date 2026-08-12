"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ConsoleController } from "@/features/console/types";
import { useDeliveryData } from "@/features/delivery/data";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildJob, ServiceRecord } from "@/lib/contracts/registry";
import { terminalBuild } from "@/lib/presentation/build";

export function useServicesData(console: ConsoleController) {
  const projectID = console.state.project?.id ?? "";
  const client = useMemo(() => new LocalClient(), []);
  const delivery = useDeliveryData(projectID, console.state.services, console.state.deployments);
  const loadBuilds = delivery.loadBuilds;
  const [buildJobs, setBuildJobs] = useState<Record<string, BuildJob[]>>({});
  const [buildJobsError, setBuildJobsError] = useState("");
  const sequence = useRef(0);
  const applications = useMemo(() => console.state.services.filter((service) => service.type === "application"), [console.state.services]);

  const loadBuildJobs = useCallback(async (services: ServiceRecord[] = applications) => {
    if (!projectID) return;
    const current = ++sequence.current;
    const results = await Promise.allSettled(services.map(async (service) => [service.id, (await client.buildJobs(projectID, service.id)).build_jobs ?? []] as const));
    if (current !== sequence.current) return;
    setBuildJobs(Object.fromEntries(results.flatMap((result) => result.status === "fulfilled" ? [result.value] : [])));
    setBuildJobsError(results.some((result) => result.status === "rejected") ? "Some BuildJob histories are unavailable; loaded factual state is preserved." : "");
  }, [applications, client, projectID]);

  useEffect(() => {
    const current = sequence.current;
    queueMicrotask(() => void loadBuildJobs());
    return () => { sequence.current = Math.max(sequence.current, current + 1); };
  }, [loadBuildJobs]);

  const active = Object.values(buildJobs).flat().some((job) => !terminalBuild(job));
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => void loadBuildJobs().then(() => loadBuilds()), 3000);
    return () => window.clearInterval(timer);
  }, [active, loadBuildJobs, loadBuilds]);

  const createBuild = useCallback(async (service: ServiceRecord, idempotencyKey: string) => {
    const job = await client.createBuildJob(projectID, service.id, idempotencyKey);
    setBuildJobs((current) => ({ ...current, [service.id]: [job, ...(current[service.id] ?? []).filter((item) => item.id !== job.id)] }));
    return job;
  }, [client, projectID]);

  return { ...delivery, buildJobs, buildJobsError, createBuild, loadBuildJobs };
}
