import type { ReactNode } from "react";
import { StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { DeliveryData } from "@/features/delivery/data";
import type { ServiceRecord } from "@/lib/contracts/registry";
import type { PipelineStatus } from "@/lib/presentation/delivery/model";

export type DeliveryViewProps = { console: ConsoleController; data: DeliveryData; selectedService?: ServiceRecord };

export function ServiceFilter({ console, services, selected }: { console: ConsoleController; services: ServiceRecord[]; selected?: ServiceRecord }) {
  return <label className="deliveryServiceFilter">Service<select aria-label="Filter Delivery by service" value={selected?.id ?? ""} onChange={(event) => console.navigate({ service: event.target.value, build: "", deployment: "" })}><option value="">All services</option>{services.filter((service) => service.type === "application").map((service) => <option key={service.id} value={service.id}>{service.name}</option>)}</select></label>;
}

export function DeliveryStatus({ status, label }: { status: PipelineStatus | string; label?: string }) {
  const presentation = status === "succeeded" ? "healthy" : status === "failed" ? "failed" : status === "in_progress" || status === "waiting" ? "in_progress" : status === "unavailable" ? "unavailable" : status === "rolled_back" ? "degraded" : "unknown";
  return <StatusBadge label={label} value={presentation} />;
}

export function Evidence({ label, value, mono = false }: { label: string; value?: ReactNode; mono?: boolean }) {
  return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{value || "Not reported"}</dd></div>;
}

export function short(value = "", size = 12) {
  if (value.length <= size) return value || "Not reported";
  return `${value.slice(0, Math.max(6, size - 5))}…${value.slice(-4)}`;
}

export function displayTime(value?: string) {
  if (!value) return "Not reported";
  const time = new Date(value);
  return Number.isNaN(time.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(time);
}

export function isTerminal(status?: string) {
  return ["succeeded", "failed", "rolled_back", "rollback_failed", "cancelled", "cleaned"].includes(status ?? "");
}
