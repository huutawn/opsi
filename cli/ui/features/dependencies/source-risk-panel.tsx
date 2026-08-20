"use client";

import { useEffect, useMemo, useState } from "react";
import { Icon, StatusBadge } from "@/components/ui/primitives";
import { LocalClient } from "@/lib/api/local-client";
import type { SourceRiskReport } from "@/lib/contracts/registry";

type Props = {
  applicationID: string;
  currentCommitSHA?: string;
  projectID: string;
};

const RULE_DESCRIPTIONS: Record<string, string> = {
  SOURCE_LOOPBACK_ENDPOINT: "Loopback address (127.0.0.1 / localhost) detected in source. In containerized environments, loopback references will not reach external services.",
  SOURCE_HARDCODED_IP_ENDPOINT: "Potential hardcoded IP endpoint detected in application source code.",
  SOURCE_BROWSER_INTERNAL_DNS: "Browser client references internal cluster DNS name, which will be unreachable from end-user browsers.",
  SOURCE_SAME_ORIGIN_ABSOLUTE_ENDPOINT: "Absolute endpoint URL found for same-origin dependency where relative route is expected.",
  SOURCE_DECLARED_ENV_NOT_OBSERVED: "Declared dependency environment variable reference was not observed in scanned source files.",
  SOURCE_ALTERNATE_DEPENDENCY_ENV_OBSERVED: "Alternative environment variable name detected that may conflict with declared dependency mapping.",
  SOURCE_EMBEDDED_CREDENTIAL_SUSPECTED: "Potential hardcoded secret or credential token pattern suspected in source file.",
};

export function SourceRiskPanel({
  applicationID,
  currentCommitSHA,
  projectID,
}: Props) {
  const client = useMemo(() => new LocalClient(), []);
  const [report, setReport] = useState<SourceRiskReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    async function fetchReport() {
      setLoading(true);
      setError(null);
      try {
        const res = await client.sourceRiskReport(projectID, applicationID);
        if (active) setReport(res);
      } catch (cause) {
        if (!active) return;
        setError((cause as Error).message || "Source risk report unavailable.");
        setReport(null);
      } finally {
        if (active) setLoading(false);
      }
    }
    void fetchReport();
    return () => {
      active = false;
    };
  }, [applicationID, client, projectID]);

  if (loading) {
    return (
      <div className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 text-center text-xs text-on-surface-variant flex items-center justify-center gap-2">
        <Icon name="sync" className="animate-spin text-[16px]" />
        <span>Loading source risk report…</span>
      </div>
    );
  }

  if (error || !report || report.analysis_status === "unavailable") {
    return (
      <div className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 text-xs text-on-surface-variant space-y-1">
        <div className="flex items-center gap-2 text-on-surface font-semibold">
          <Icon name="security" className="text-primary text-[18px]" />
          <span>Source Analysis Unavailable</span>
        </div>
        <p className="text-[11px]">
          No source risk analysis report is available for the current revision. Analysis is performed during BuildRecord generation.
        </p>
      </div>
    );
  }

  const isStale = Boolean(
    currentCommitSHA && report.commit_sha && currentCommitSHA !== report.commit_sha
  );

  return (
    <div className="bg-surface-container rounded-xl p-4 border border-outline-variant/20 space-y-4 text-xs">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2">
            <Icon name="security" className="text-primary text-[18px]" />
            <h4 className="font-headline-md text-sm font-semibold text-on-surface">
              Source Risk Warnings (ADC-05)
            </h4>
          </div>
          <p className="text-[11px] text-on-surface-variant mt-0.5">
            Static heuristic scan of materialized source code. Warnings inform review; they never block builds.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {isStale ? (
            <StatusBadge label="Outdated Analysis" value="in_progress" />
          ) : (
            <StatusBadge label={report.analysis_status} value={report.analysis_status === "complete" ? "healthy" : "in_progress"} />
          )}
        </div>
      </div>

      {isStale ? (
        <div className="p-3 bg-status-warning/10 border border-status-warning/30 rounded-lg text-xs text-status-warning flex items-center gap-2">
          <Icon name="warning" className="text-[16px] shrink-0" />
          <span>Analysis was generated for commit <code>{report.commit_sha?.slice(0, 8)}</code> which differs from current commit.</span>
        </div>
      ) : null}

      <div className="grid grid-cols-3 gap-3 font-code-md text-[11px] bg-surface-container-highest p-3 rounded-lg border border-outline-variant/15">
        <div>
          <span className="text-[10px] text-on-surface-variant block uppercase">Files Scanned</span>
          <strong className="text-on-surface">{report.files_scanned}</strong>
        </div>
        <div>
          <span className="text-[10px] text-on-surface-variant block uppercase">Findings</span>
          <strong className="text-on-surface">{(report.findings ?? []).length}</strong>
        </div>
        <div>
          <span className="text-[10px] text-on-surface-variant block uppercase">Scanner</span>
          <strong className="text-on-surface">{report.scanner_version}</strong>
        </div>
      </div>

      {(report.findings ?? []).length > 0 ? (
        <div className="space-y-2.5">
          {(report.findings ?? []).map((f, idx) => (
            <div
              key={f.finding_id || idx}
              className="p-3 bg-surface-container-highest rounded-lg border border-outline-variant/20 space-y-1.5"
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className="w-2 h-2 rounded-full bg-status-warning" />
                  <strong className="font-code-md text-status-warning">{f.rule_id}</strong>
                </div>
                <div className="flex items-center gap-2">
                  <span className="px-1.5 py-0.5 rounded text-[10px] font-label-sm uppercase bg-surface-container font-semibold text-on-surface-variant">
                    {f.confidence} confidence
                  </span>
                  <span className="px-1.5 py-0.5 rounded text-[10px] font-label-sm uppercase bg-status-warning/20 text-status-warning font-bold">
                    {f.severity}
                  </span>
                </div>
              </div>

              <p className="text-on-surface text-[11px]">
                {RULE_DESCRIPTIONS[f.rule_id] || f.rule_id}
              </p>

              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-code-md text-[10px] text-on-surface-variant pt-1 border-t border-outline-variant/15">
                <span>
                  File: <strong className="text-on-surface">{f.file || (f as any).file_path}</strong>:{f.line || (f as any).line_number}{f.column ? `:${f.column}` : ""}
                </span>
                {f.dependency_logical_name ? (
                  <span>
                    Dependency: <strong className="text-primary">{f.dependency_logical_name}</strong>
                  </span>
                ) : null}
              </div>

              {(f.safe_evidence || (f as any).redacted_snippet) ? (
                <div className="bg-surface-container p-2 rounded text-[10px] font-code-md text-on-surface-variant break-all">
                  <code>{f.safe_evidence || (f as any).redacted_snippet}</code>
                </div>
              ) : null}
            </div>
          ))}
        </div>
      ) : (
        <div className="p-3 bg-surface-container-highest rounded-lg text-on-surface-variant text-[11px] flex items-center gap-2">
          <Icon name="check_circle" className="text-status-ready text-[16px]" />
          <span>No risk patterns or suspicious endpoints detected in the scanned application source.</span>
        </div>
      )}
    </div>
  );
}
