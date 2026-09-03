import { formatObserved as formatObservedI18n, type Locale } from "../../lib/i18n/index.ts";
import { StatusBadge } from "@/components/ui/primitives";
import type { SourceState } from "@/features/observability/data";

export function SourceBadge({ label, state }: { label: string; state: SourceState }) {
  return (
    <div className="bg-surface-container p-3.5 rounded-xl border border-outline-variant/20 flex items-center justify-between">
      <span className="font-body-md text-xs text-on-surface font-medium">{label}</span>
      <StatusBadge value={state === "ready" ? "healthy" : state === "partial" ? "degraded" : state} />
    </div>
  );
}

export function Fact({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="flex flex-col min-w-0">
      <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider">{label}</dt>
      <dd className="font-body-md text-xs font-semibold text-on-surface truncate mt-0.5">{value}</dd>
    </div>
  );
}

export function formatObserved(value?: number, locale: Locale = "en") {
  return formatObservedI18n(value, locale);
}
