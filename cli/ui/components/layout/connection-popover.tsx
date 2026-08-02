import type { LocalSessionStatus } from "@/lib/api/local-client";

export function ConnectionPopover({ session }: { session: LocalSessionStatus }) {
  const degraded = session.cloud_connected !== "ok" || session.agent_connected !== "ok";
  return <details className="connectionPopover">
    <summary aria-label="Connection status"><span className={`connectionDot ${degraded ? "degraded" : "healthy"}`} aria-hidden="true" /><span>{degraded ? "Connection issue" : "Connections"}</span></summary>
    <div className="connectionMenu"><p className="eyebrow">Local sources</p><Connection label="Cloud" value={session.cloud_connected} /><Connection label="Agent" value={session.agent_connected} /><p>Healthy sources stay here; only unavailable sources are promoted into page context.</p></div>
  </details>;
}

function Connection({ label, value }: { label: string; value: string }) {
  const ok = value === "ok";
  return <div className="connectionRow"><span><i className={ok ? "healthy" : "degraded"} aria-hidden="true" />{label}</span><strong>{ok ? "Connected" : value === "failed" ? "Unavailable" : "Unknown"}</strong></div>;
}
