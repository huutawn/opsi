import type { ReactNode } from "react";
import { normalizeStatus, statusLabel, type PresentationStatus } from "@/lib/presentation/project";

export function Panel({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="panel">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

export function Empty({ action, text, title = "No data yet" }: { action?: ReactNode; text: string; title?: string }) {
  return <div className="empty" role="status"><strong>{title}</strong><p>{text}</p>{action}</div>;
}

export function Metric({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="metric">
      <span className="muted">{label}</span>
      <b>{value}</b>
    </div>
  );
}

export function StatusBadge({ label, value }: { label?: string; value: string | PresentationStatus }) {
  const normalized = normalizeStatus(value);
  const text = label ?? (String(value) === normalized ? statusLabel(normalized) : String(value).replaceAll("_", " "));
  return <span className={`status ${normalized}`}><i aria-hidden="true" />{text}</span>;
}

export function PageHeader({ action, eyebrow, title, description }: { action?: ReactNode; eyebrow?: string; title: string; description?: string }) {
  return <header className="pageHeader"><div>{eyebrow ? <p className="eyebrow">{eyebrow}</p> : null}<h1>{title}</h1>{description ? <p>{description}</p> : null}</div>{action ? <div className="pageAction">{action}</div> : null}</header>;
}

export function StatePanel({ title, text, retry }: { title: string; text: string; retry?: () => void }) {
  return (
    <Panel title={title}>
      <div className="empty">
        <p>{text}</p>
        {retry ? (
          <button onClick={retry} type="button">
            Retry
          </button>
        ) : null}
      </div>
    </Panel>
  );
}
