import type { ReactNode } from "react";

export function Panel({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="panel">
      <h2>{title}</h2>
      {children}
    </section>
  );
}

export function Empty({ text }: { text: string }) {
  return <div className="empty">{text}</div>;
}

export function Metric({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="metric">
      <span className="muted">{label}</span>
      <b>{value}</b>
    </div>
  );
}

export function StatusBadge({ value }: { value: string }) {
  const cls = ["ready", "healthy", "succeeded", "active"].includes(value)
    ? "ok"
    : ["blocked", "failed", "removed", "cancelled"].includes(value)
      ? "bad"
      : "warn";
  return <span className={`status ${cls}`}>{value}</span>;
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
