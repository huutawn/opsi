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

export type TimeWindow = "1h" | "6h" | "24h";

export function TimeWindowSelector({
  value,
  onChange,
  disabled = false,
}: {
  value: string;
  onChange: (window: TimeWindow) => void;
  disabled?: boolean;
}) {
  const current = (value === "24h" ? "24h" : value === "6h" ? "6h" : "1h") as TimeWindow;
  const windows: Array<{ id: TimeWindow; label: string }> = [
    { id: "1h", label: "1h" },
    { id: "6h", label: "6h" },
    { id: "24h", label: "24h" },
  ];

  return (
    <div
      aria-label="Time window"
      className="inline-flex items-center p-0.5 rounded-lg bg-surface-container border border-outline-variant/30 text-xs font-code-md"
      role="radiogroup"
    >
      <span className="px-2 text-[11px] text-on-surface-variant font-medium select-none">
        Window:
      </span>
      {windows.map((w) => {
        const selected = current === w.id;
        return (
          <button
            key={w.id}
            aria-checked={selected}
            aria-label={`Last ${w.label}`}
            className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors cursor-pointer ${
              selected
                ? "bg-surface-container-highest text-primary font-bold shadow-xs"
                : "text-on-surface-variant hover:text-on-surface hover:bg-surface-container-high/50"
            }`}
            disabled={disabled}
            onClick={() => onChange(w.id)}
            role="radio"
            type="button"
          >
            {w.label}
          </button>
        );
      })}
    </div>
  );
}

export function PartialCoverageBanner({
  coverage,
  sourceName,
}: {
  coverage?: {
    status: string;
    expected_agents: number;
    successful_agents: number;
    failed_agents: number;
    errors?: Array<{ node_id?: string; code?: string; message_redacted?: string; actionable_cause?: string }>;
  };
  sourceName: string;
}) {
  if (!coverage || coverage.status !== "partial") return null;

  return (
    <div
      aria-live="polite"
      className="p-3.5 bg-status-warning/10 border border-status-warning/30 rounded-xl text-status-warning text-xs space-y-1.5"
      data-testid="partial-coverage-banner"
    >
      <div className="flex items-center justify-between font-semibold">
        <span>
          Degraded {sourceName} Coverage ({coverage.successful_agents}/{coverage.expected_agents} agents)
        </span>
        <span className="text-[11px] font-code-md">
          {coverage.failed_agents} unavailable
        </span>
      </div>
      {coverage.errors && coverage.errors.length > 0 ? (
        <ul className="space-y-1 text-on-surface-variant text-[11px]">
          {coverage.errors.map((err, i) => (
            <li key={i} className="flex items-start gap-1.5">
              <span className="font-code-md font-semibold text-on-surface shrink-0">
                {err.node_id ? `[${err.node_id}]` : `[${err.code || "AGENT_ERROR"}]`}
              </span>
              <span>{err.actionable_cause || err.message_redacted}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
