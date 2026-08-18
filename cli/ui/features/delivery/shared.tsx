import type { ReactNode } from "react";
import { Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { DeliveryData } from "@/features/delivery/data";
import type { ServiceRecord } from "@/lib/contracts/registry";
import type { PipelineStatus } from "@/lib/presentation/delivery/model";

export type DeliveryViewProps = {
  console: ConsoleController;
  data: DeliveryData;
  selectedService?: ServiceRecord;
};

export function ServiceFilter({
  console,
  services,
  selected,
}: {
  console: ConsoleController;
  services: ServiceRecord[];
  selected?: ServiceRecord;
}) {
  return (
    <div className="relative w-full sm:w-auto sm:min-w-[200px]">
      <select
        aria-label="Filter Delivery by service"
        className="w-full bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
        onChange={(event) => console.navigate({ service: event.target.value, build: "", deployment: "" })}
        value={selected?.id ?? ""}
      >
        <option value="">All Services ({services.filter((s) => s.type === "application").length})</option>
        {services
          .filter((service) => service.type === "application")
          .map((service) => (
            <option key={service.id} value={service.id}>
              {service.name}
            </option>
          ))}
      </select>
      <Icon
        name="expand_more"
        className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]"
      />
    </div>
  );
}

export function DeliveryStatus({ status, label }: { status: PipelineStatus | string; label?: string }) {
  const presentation =
    status === "succeeded" || status === "running"
      ? "healthy"
      : status === "failed" || status === "rollback_failed"
        ? "failed"
        : status === "in_progress" || status === "waiting" || status === "pulling" || status === "applying" || status === "waiting_ready" || status === "leased" || status === "queued"
          ? "in_progress"
          : status === "unavailable"
            ? "unavailable"
            : status === "rolled_back"
              ? "degraded"
              : "unknown";
  return <StatusBadge label={label} value={presentation} />;
}

export function Evidence({ label, value, mono = false }: { label: string; value?: ReactNode; mono?: boolean }) {
  return (
    <div className="flex flex-col min-w-0">
      <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider">{label}</dt>
      <dd className={`text-xs text-on-surface font-semibold truncate mt-0.5 ${mono ? "font-code-md" : "font-body-md"}`}>
        {value || "Not reported"}
      </dd>
    </div>
  );
}

export function short(value = "", size = 12) {
  if (value.length <= size) return value || "Not reported";
  return `${value.slice(0, Math.max(6, size - 5))}…${value.slice(-4)}`;
}

export function displayTime(value?: string) {
  if (!value) return "Not reported";
  const time = new Date(value);
  return Number.isNaN(time.getTime())
    ? value
    : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(time);
}

export function isTerminal(status?: string) {
  return ["succeeded", "failed", "rolled_back", "rollback_failed", "cancelled", "cleaned"].includes(status ?? "");
}
