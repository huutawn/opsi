import type { ReactNode } from "react";
import { normalizeStatus, statusLabel, type PresentationStatus } from "@/lib/presentation/project";

export function Surface({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="surfaceBlock">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

export function Empty({ action, text, title = "No data yet" }: { action?: ReactNode; text: string; title?: string }) {
  return <div className="empty" role="status"><strong>{title}</strong><p>{text}</p>{action}</div>;
}

export function StatusBadge({ label, value }: { label?: string; value: string | PresentationStatus }) {
  const normalized = normalizeStatus(value);
  const text = label ?? (String(value) === normalized ? statusLabel(normalized) : String(value).replaceAll("_", " "));
  return <span className={`status ${normalized}`} data-status={normalized}><StatusIcon status={normalized} />{text}</span>;
}

function StatusIcon({ status }: { status: PresentationStatus }) {
  const path = status === "healthy" ? "m4 8 3 3 5-6" : status === "failed" ? "M5 5l6 6m0-6-6 6" : status === "degraded" ? "M8 4v5m0 3h.01" : status === "in_progress" ? "M8 3a5 5 0 1 1-4.33 2.5M3 3v3h3" : "M8 4v4m0 3h.01";
  return <svg aria-hidden="true" className="statusIcon" viewBox="0 0 16 16"><path d={path} /></svg>;
}

export function PageHeader({ action, eyebrow, title, description }: { action?: ReactNode; eyebrow?: string; title: string; description?: string }) {
  return <header className="pageHeader"><div>{eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}<h1>{title}</h1>{description ? <p>{description}</p> : null}</div>{action ? <div className="pageAction">{action}</div> : null}</header>;
}

export function StatePanel({ title, text, retry }: { title: string; text: string; retry?: () => void }) {
  return (
    <Surface title={title}>
      <div className="empty">
        <p>{text}</p>
        {retry ? (
          <button onClick={retry} type="button">
            Retry
          </button>
        ) : null}
      </div>
    </Surface>
  );
}
