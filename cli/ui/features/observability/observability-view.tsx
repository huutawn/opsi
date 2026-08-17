"use client";

import { Empty } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { ApplicationsTab } from "@/features/observability/applications-tab";
import { useObservabilityData } from "@/features/observability/data";
import { HealthTab } from "@/features/observability/health-tab";
import { IncidentsTab } from "@/features/observability/incidents-tab";
import { LogsTab } from "@/features/observability/logs-tab";
import { MetricsTab } from "@/features/observability/metrics-tab";
import { OverviewTab } from "@/features/observability/overview-tab";
import { ResourcesTab } from "@/features/observability/resources-tab";
import { ServersTab } from "@/features/observability/servers-tab";

export function ObservabilityView({ console }: { console: ConsoleController }) {
  const model = useObservabilityData(console);
  if (!console.state.project) return <Empty text="Select a project first." />;

  const tab = console.route.tab;

  return (
    <div className="observabilityPage">
      {tab === "applications" ? (
        <ApplicationsTab console={console} model={model} />
      ) : tab === "servers" ? (
        <ServersTab console={console} model={model} />
      ) : tab === "resources" ? (
        <ResourcesTab console={console} model={model} />
      ) : tab === "health" ? (
        <HealthTab console={console} model={model} />
      ) : tab === "metrics" ? (
        <MetricsTab console={console} model={model} />
      ) : tab === "logs" ? (
        <LogsTab console={console} model={model} />
      ) : tab === "incidents" ? (
        <IncidentsTab console={console} model={model} />
      ) : (
        <OverviewTab console={console} model={model} />
      )}
    </div>
  );
}

export type ObservabilityModel = ReturnType<typeof useObservabilityData>;
