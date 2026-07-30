import { StatusBadge } from "@/components/ui/primitives";
import type { SourceState } from "@/features/observability/data";

export function SourceBadge({ label, state }: { label: string; state: SourceState }) {
  return <div className="coverageItem"><span>{label}</span><StatusBadge value={state === "ready" ? "healthy" : state === "partial" ? "degraded" : state} /></div>;
}

export function Fact({ label, value }: { label: string; value: string | number }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>;
}

export function formatObserved(value?: number) {
  return value ? new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(value * 1000) : "Not reported";
}
