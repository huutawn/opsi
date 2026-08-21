"use client";

import type { ConsoleController } from "@/features/console/types";
import { useDeliveryData } from "@/features/delivery/data";

export function useServicesData(console: ConsoleController) {
  const projectID = console.state.project?.id ?? "";
  return useDeliveryData(projectID, console.state.services, console.state.deployments);
}
