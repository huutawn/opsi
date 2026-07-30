"use client";

import { Empty } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { HealthTab } from "@/features/observability/health-tab";
import { IncidentsTab } from "@/features/observability/incidents-tab";
import { LogsTab } from "@/features/observability/logs-tab";
import { MetricsTab } from "@/features/observability/metrics-tab";
import { useObservabilityData } from "@/features/observability/data";

export function ObservabilityView({ console }: { console: ConsoleController }) {
  const model = useObservabilityData(console);
  if (!console.state.project) return <Empty text="Select a project first." />;
  return <div className="observabilityPage">
    {console.route.tab === "health" ? <HealthTab console={console} model={model} /> : null}
    {console.route.tab === "metrics" ? <MetricsTab console={console} model={model} /> : null}
    {console.route.tab === "logs" ? <LogsTab console={console} model={model} /> : null}
    {console.route.tab === "incidents" ? <IncidentsTab console={console} model={model} /> : null}
  </div>;
}

export type ObservabilityModel = ReturnType<typeof useObservabilityData>;
