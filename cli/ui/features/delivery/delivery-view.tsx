"use client";

import { useEffect } from "react";
import type { ConsoleController } from "@/features/console/types";
import { useDeliveryData } from "@/features/delivery/data";
import { PipelineView } from "@/features/delivery/pipeline-view";
import { BuildsView } from "@/features/delivery/builds-view";
import { DeploymentsView } from "@/features/delivery/deployments-view";
import { ExposureView } from "@/features/delivery/exposure-view";
import { SourceView } from "@/features/delivery/source-view";

export function DeliveryView({ console }: { console: ConsoleController }) {
  const projectID = console.state.project?.id ?? "";
  const data = useDeliveryData(projectID, console.state.services, console.state.deployments);
  const selectedService = data.services.find((item) => item.id === console.route.service) ?? data.services.find((item) => item.type === "application") ?? data.services[0];

  useEffect(() => {
    if (!console.route.service && selectedService) console.navigate({ service: selectedService.id });
  }, [console, selectedService]);

  const common = { console, data, selectedService };
  switch (console.route.tab) {
    case "builds": return <BuildsView {...common} />;
    case "deployments": return <DeploymentsView {...common} />;
    case "exposure": return <ExposureView {...common} />;
    case "source": return <SourceView {...common} />;
    default: return <PipelineView {...common} />;
  }
}
