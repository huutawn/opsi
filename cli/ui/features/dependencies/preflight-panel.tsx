"use client";

import { useMemo } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import type { PreflightCheck, PreflightResult } from "@/lib/contracts/registry";

type Props = {
  acknowledgedWarnings: Record<string, boolean>;
  onAcknowledgeWarning: (id: string, checked: boolean) => void;
  onRemediate?: (code: string, check: PreflightCheck) => void;
  preflight?: PreflightResult;
};

type CheckCategory = "build" | "placement" | "dependency" | "exposure" | "source_risk";

function categorizeCheck(check: PreflightCheck): CheckCategory {
  if (check.code.startsWith("BUILD_")) return "build";
  if (check.code.startsWith("PLACEMENT_") || check.code.startsWith("RUNTIME_") || check.code.startsWith("AGENT_") || check.code.startsWith("CAPACITY_")) {
    return "placement";
  }
  if (check.code.startsWith("SOURCE_")) return "source_risk";
  if (check.code.includes("ROUTE") || check.code.includes("PUBLIC_ENDPOINT")) return "exposure";
  return "dependency";
}

const CATEGORY_LABELS: Record<CheckCategory, { title: string; icon: string }> = {
  build: { title: "Build & Artifact Verification", icon: "inventory_2" },
  placement: { title: "Placement & Agent Runtime", icon: "dns" },
  dependency: { title: "Dependency Contracts & Realization", icon: "hub" },
  exposure: { title: "Routing & Exposure Preflight", icon: "public" },
  source_risk: { title: "Source Risk Analysis (ADC-05)", icon: "security" },
};

const REMEDIATION_LABELS: Record<string, string> = {
  CREATE_BUILD: "Start Build",
  REBUILD_REQUIRED: "Review Build",
  PLAN_PLACEMENT: "Plan Placement",
  REALIZE_DEPENDENCY: "Review Connection",
  WAIT_FOR_RESOURCE: "Open Managed Resource",
  CONFIGURE_EXPOSURE: "Configure Exposure",
  RESOLVE_ROUTE_CONFLICT: "Resolve Route Conflict",
  REVIEW_CONFIGURATION: "Review Configuration",
  INCLUDE_DEPENDENCY_TARGET: "Include Target in Deployment",
  EXPLICIT_MIGRATION_REQUIRED: "Open Database Migration",
};

export function PreflightPanel({
  acknowledgedWarnings,
  onAcknowledgeWarning,
  onRemediate,
  preflight,
}: Props) {
  const grouped = useMemo(() => {
    const map: Record<CheckCategory, PreflightCheck[]> = {
      build: [],
      placement: [],
      dependency: [],
      exposure: [],
      source_risk: [],
    };
    if (!preflight) return map;
    for (const chk of preflight.checks) {
      map[categorizeCheck(chk)].push(chk);
    }
    return map;
  }, [preflight?.checks]);

  if (!preflight) return null;

  const isBlocked = preflight.status === "BLOCKED";
  const hasWarnings = preflight.status === "PASS_WITH_WARNINGS";

  const warningCount = preflight.checks.filter((c) => c.severity === "WARN").length;
  const blockCount = preflight.checks.filter((c) => c.severity === "BLOCK").length;

  return (
    <section className="space-y-4 text-xs" aria-label="Unified Deployment Preflight">
      {/* Overall Preflight Banner */}
      <div
        className={`p-4 rounded-xl border flex items-start gap-3.5 shadow-sm ${
          isBlocked
            ? "bg-error-container/20 border-status-failed/40 text-on-surface"
            : hasWarnings
            ? "bg-status-warning/10 border-status-warning/30 text-on-surface"
            : "bg-status-ready/10 border-status-ready/30 text-on-surface"
        }`}
      >
        <div
          className={`p-2 rounded-lg shrink-0 ${
            isBlocked
              ? "bg-status-failed/20 text-status-failed"
              : hasWarnings
              ? "bg-status-warning/20 text-status-warning"
              : "bg-status-ready/20 text-status-ready"
          }`}
        >
          <Icon
            name={isBlocked ? "block" : hasWarnings ? "warning" : "check_circle"}
            className="text-[22px]"
          />
        </div>
        <div className="flex-1 space-y-1">
          <div className="flex items-center justify-between">
            <h4 className="font-headline-md text-sm font-bold">
              {isBlocked
                ? "Deployment Preflight Blocked"
                : hasWarnings
                ? "Deployment Preflight Passed with Warnings"
                : "Deployment Preflight Passed"}
            </h4>
            <StatusBadge
              label={preflight.status.replaceAll("_", " ")}
              value={isBlocked ? "failed" : hasWarnings ? "in_progress" : "healthy"}
            />
          </div>
          <p className="text-on-surface-variant text-[11px]">
            {isBlocked
              ? `${blockCount} blocker${blockCount === 1 ? "" : "s"} must be resolved before deployment is authorized. Preflight blocks cannot be overridden.`
              : hasWarnings
              ? `${warningCount} warning${warningCount === 1 ? "" : "s"} require explicit acknowledgement before deployment.`
              : "All safety gates, dependency realization, placement facts, and immutable artifacts verified."}
          </p>
          <div className="font-code-md text-[10px] text-on-surface-variant/70 pt-0.5">
            Preflight hash: <code>{preflight.preflight_hash?.slice(0, 16) || "none"}…</code>
          </div>
        </div>
      </div>

      {/* Grouped Checks */}
      <div className="space-y-3">
        {(["build", "placement", "dependency", "exposure", "source_risk"] as CheckCategory[]).map((cat) => {
          const checks = grouped[cat];
          if (!checks.length) return null;
          const { title, icon } = CATEGORY_LABELS[cat];

          return (
            <div
              key={cat}
              className="bg-surface-container rounded-xl border border-outline-variant/20 overflow-hidden"
            >
              <div className="px-4 py-2.5 bg-surface-container-high/60 border-b border-outline-variant/15 flex items-center justify-between">
                <div className="flex items-center gap-2 font-semibold text-on-surface">
                  <Icon name={icon} className="text-[16px] text-primary" />
                  <span>{title}</span>
                </div>
                <span className="font-code-md text-[11px] text-on-surface-variant">
                  {checks.length} check{checks.length === 1 ? "" : "s"}
                </span>
              </div>

              <div className="divide-y divide-outline-variant/15">
                {checks.map((chk) => {
                  const isBlock = chk.severity === "BLOCK";
                  const isWarn = chk.severity === "WARN";
                  const isAck = Boolean(acknowledgedWarnings[chk.id]);

                  return (
                    <div
                      key={chk.id}
                      className={`p-3.5 flex flex-col gap-2 ${
                        isBlock
                          ? "bg-error-container/10"
                          : isWarn
                          ? "bg-status-warning/5"
                          : ""
                      }`}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="flex items-start gap-2 min-w-0">
                          <span
                            className={`mt-0.5 w-2 h-2 rounded-full shrink-0 ${
                              isBlock
                                ? "bg-status-failed"
                                : isWarn
                                ? "bg-status-warning"
                                : "bg-status-ready"
                            }`}
                          />
                          <div>
                            <span className="font-code-md font-bold text-on-surface block">
                              {chk.code}
                            </span>
                            <p className="text-on-surface-variant text-[11px] mt-0.5">
                              {chk.message}
                            </p>
                            {chk.scope_id ? (
                              <span className="text-[10px] text-on-surface-variant/70 font-code-md block mt-0.5">
                                Scope: {chk.scope_kind} · {chk.scope_id}
                                {chk.dependency_logical_name ? ` (dep: ${chk.dependency_logical_name})` : ""}
                              </span>
                            ) : null}
                          </div>
                        </div>

                        {/* Severity & Remediation Action */}
                        <div className="flex flex-col items-end gap-1.5 shrink-0">
                          <span
                            className={`px-2 py-0.5 rounded text-[10px] font-label-sm font-bold uppercase ${
                              isBlock
                                ? "bg-status-failed/20 text-status-failed border border-status-failed/30"
                                : isWarn
                                ? "bg-status-warning/20 text-status-warning border border-status-warning/30"
                                : "bg-status-ready/20 text-status-ready border border-status-ready/30"
                            }`}
                          >
                            {chk.severity}
                          </span>

                          {chk.remediation_code && onRemediate ? (
                            <button
                              type="button"
                              onClick={() => onRemediate(chk.remediation_code!, chk)}
                              className="text-xs text-primary hover:underline font-semibold flex items-center gap-1 cursor-pointer"
                            >
                              <span>{REMEDIATION_LABELS[chk.remediation_code] || chk.remediation_code}</span>
                              <Icon name="arrow_forward" className="text-[14px]" />
                            </button>
                          ) : null}
                        </div>
                      </div>

                      {/* Transitive Tree / Evidence */}
                      {chk.safe_evidence && Object.keys(chk.safe_evidence).length > 0 ? (
                        <div className="mt-1 bg-surface-container-highest p-2 rounded-lg font-code-md text-[10px] text-on-surface-variant space-y-0.5">
                          {Object.entries(chk.safe_evidence).map(([k, v]) => (
                            <div key={k} className="flex items-center justify-between">
                              <span className="font-semibold text-on-surface">{k}:</span>
                              <span className="truncate max-w-[280px]">{v}</span>
                            </div>
                          ))}
                        </div>
                      ) : null}

                      {/* Warning Acknowledgement Checkbox */}
                      {isWarn ? (
                        <div className="pt-1.5 border-t border-outline-variant/15 flex items-center gap-2">
                          <label className="flex items-center gap-2 cursor-pointer text-on-surface text-[11px] font-medium">
                            <input
                              type="checkbox"
                              checked={isAck}
                              onChange={(e) => onAcknowledgeWarning(chk.id, e.target.checked)}
                              className="rounded accent-primary"
                            />
                            <span>
                              I reviewed <strong className="font-code-md text-primary">{chk.code}</strong> in scope <strong className="font-code-md">{chk.scope_id || chk.scope_kind}</strong>
                            </span>
                          </label>
                        </div>
                      ) : null}
                    </div>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}
