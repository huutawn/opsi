import React from "react";
import type { LocalSessionStatus } from "@/lib/api/local-client";
import { Icon } from "@/components/ui/primitives";
import { useI18n } from "@/lib/i18n";
export function ConnectionPopover({ session }: { session: LocalSessionStatus }) {
  const { t } = useI18n();
  const degraded = session.cloud_connected !== "ok" || session.agent_connected !== "ok";
  
  return (
    <details className="relative group">
      <summary className="flex items-center gap-2 px-3 py-1.5 rounded-lg cursor-pointer bg-surface-container hover:bg-surface-container-high border border-outline-variant/30 text-sm list-none select-none transition-colors">
        <span className={`w-2 h-2 rounded-full ${degraded ? "bg-status-warning" : "bg-status-ready"}`} aria-hidden="true" />
        <span className="text-on-surface font-medium">{degraded ? t("nav.connection_issue", "Connection issue") : t("nav.connections", "Connections")}</span>
        <Icon name="expand_more" className="text-[18px] text-on-surface-variant group-open:rotate-180 transition-transform" />
      </summary>
      
      <div className="absolute right-0 top-full mt-2 w-64 bg-surface-container-high border border-outline-variant/30 rounded-xl shadow-lg p-4 z-50">
        <p className="text-xs text-on-surface-variant uppercase tracking-wider mb-3">{t("nav.local_sources", "Local sources")}</p>
        <div className="flex flex-col gap-3 mb-4">
          <Connection label="Cloud" value={session.cloud_connected} />
          <Connection label="Agent" value={session.agent_connected} />
        </div>
        <p className="text-xs text-on-surface-variant/70 border-t border-outline-variant/20 pt-3">
          {t("nav.connection_popover_note", "Healthy sources stay here; only unavailable sources are promoted into page context.")}
        </p>
      </div>
    </details>
  );
}

function Connection({ label, value }: { label: string; value: string }) {
  const { t } = useI18n();
  const ok = value === "ok";
  return (
    <div className="flex items-center justify-between text-sm">
      <div className="flex items-center gap-2 text-on-surface">
        <span className={`w-2 h-2 rounded-full ${ok ? "bg-status-ready" : "bg-status-failed"}`} aria-hidden="true" />
        {label}
      </div>
      <strong className={`font-medium ${ok ? "text-status-ready" : "text-status-failed"}`}>
        {ok ? t("status.connected", "Connected") : value === "failed" ? t("status.unavailable", "Unavailable") : value === "not connected" ? t("status.disconnected", "Not connected") : t("status.unknown", "Unknown")}
      </strong>
    </div>
  );
}
