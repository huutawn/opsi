import React, { ButtonHTMLAttributes, HTMLAttributes, InputHTMLAttributes, ReactNode, Ref, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";

export function Icon({ name, className = "" }: { name: string; className?: string }) {
  return (
    <span className={`material-symbols-outlined select-none ${className}`} aria-hidden="true" data-icon={name}>
      {name}
    </span>
  );
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "danger" | "ghost" | "secondary" | "outline";
  size?: "sm" | "md" | "lg";
  ref?: Ref<HTMLButtonElement>;
}

export function Button({ variant = "primary", size = "md", className = "", children, ref, ...props }: ButtonProps) {
  const sizeClasses = size === "sm" ? "px-3 py-1.5 text-xs min-h-[32px]" : size === "lg" ? "px-6 py-3 text-base min-h-[48px]" : "px-4 py-2.5 text-sm min-h-[40px]";
  let variantClasses = "";
  switch (variant) {
    case "primary":
      variantClasses = "bg-primary text-on-primary hover:bg-primary/90 font-medium"; break;
    case "danger":
      variantClasses = "bg-error text-error-container hover:bg-error/90 font-medium"; break;
    case "ghost":
      variantClasses = "text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest"; break;
    case "secondary":
      variantClasses = "bg-surface-container-high text-on-surface hover:bg-surface-container-highest"; break;
    case "outline":
      variantClasses = "border border-outline-variant text-on-surface hover:bg-surface-container-high"; break;
  }
  return (
    <button ref={ref} className={`rounded-lg inline-flex items-center justify-center gap-2 cursor-pointer transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${sizeClasses} ${variantClasses} ${className}`} {...props}>
      {children}
    </button>
  );
}

export function IconButton({ icon, title, className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { icon: string; title?: string }) {
  return (
    <button title={title} aria-label={title} className={`min-w-[40px] min-h-[40px] inline-flex items-center justify-center p-2 rounded-full text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed ${className}`} {...props}>
      <Icon name={icon} className="text-[20px]" />
    </button>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input className={`w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 text-sm text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50 disabled:opacity-50 ${props.className || ""}`} {...props} />
  );
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className="relative w-full">
      <select className={`w-full bg-surface-container-highest border border-outline-variant/30 text-on-surface text-sm rounded-lg py-3 pl-3 pr-10 appearance-none focus:outline-none focus:border-primary/50 transition-colors cursor-pointer disabled:opacity-50 ${props.className || ""}`} {...props}>
        {props.children}
      </select>
      <Icon name="expand_more" className="absolute right-3 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[20px]" />
    </div>
  );
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea className={`w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 text-sm text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50 disabled:opacity-50 ${props.className || ""}`} {...props} />
  );
}

export function StatusBadge({ label, value, status, className = "" }: { label?: string; value?: string; status?: "ready" | "healthy" | "failed" | "in_progress" | "degraded" | "unknown" | "blocked" | "pending"; className?: string }) {
  const resolved = status ?? value ?? "unknown";
  let colorClass = "bg-status-unknown/20 text-status-unknown";
  const displayLabel = label ?? (resolved === "ready" ? "Ready" : resolved === "healthy" ? "Healthy" : resolved === "failed" ? "Failed" : resolved === "in_progress" ? "In Progress" : resolved === "degraded" ? "Degraded" : resolved);
  switch (resolved) {
    case "ready":
    case "healthy":
      colorClass = "bg-status-ready/20 text-status-ready"; break;
    case "failed":
    case "blocked":
      colorClass = "bg-status-failed/20 text-status-failed"; break;
    case "in_progress":
      colorClass = "bg-status-progress/20 text-status-progress"; break;
    case "degraded":
      colorClass = "bg-status-warning/20 text-status-warning"; break;
  }
  return (
    <span className={`status ${resolved} inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium ${colorClass} ${className}`}>
      <span className={`statusIcon w-1.5 h-1.5 rounded-full ${resolved === "ready" || resolved === "healthy" ? "bg-status-ready" : resolved === "in_progress" ? "bg-status-progress animate-pulse" : resolved === "degraded" ? "bg-status-warning" : resolved === "failed" || resolved === "blocked" ? "bg-status-failed" : "bg-status-unknown"}`} />
      <span>{displayLabel}</span>
    </span>
  );
}

export function Badge({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-full bg-surface-container-highest text-on-surface text-xs font-medium ${className}`}>
      {children}
    </span>
  );
}

export function Card({ className = "", children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={`bg-surface-container rounded-xl p-6 shadow-sm border border-outline-variant/20 ${className}`} {...props}>
      {children}
    </div>
  );
}

export function PageHeader({ title, description, eyebrow, action, icon }: { title: string; description?: string; eyebrow?: string; action?: ReactNode; icon?: string }) {
  return (
    <div className="pageHeader flex flex-col sm:flex-row sm:items-center justify-between pb-8 gap-4">
      <div className="flex items-center gap-4">
        {icon && (
          <div className="p-3 bg-surface-container-high rounded-xl shadow-sm flex items-center justify-center">
            <Icon name={icon} className="text-primary text-[28px]" />
          </div>
        )}
        <div className="flex flex-col">
          {eyebrow && <span className="text-xs text-on-surface-variant uppercase tracking-wider mb-1">{eyebrow}</span>}
          <h1 className="text-3xl font-semibold text-on-surface">{title}</h1>
          {description && <span className="text-base text-on-surface-variant mt-1">{description}</span>}
        </div>
      </div>
      {action && <div className="flex items-center gap-3 shrink-0">{action}</div>}
    </div>
  );
}

export function SectionHeader({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <div className="flex items-center justify-between mb-4 gap-4">
      <div>
        <h2 className="text-xl font-semibold text-on-surface">{title}</h2>
        {description && <p className="text-sm text-on-surface-variant mt-1">{description}</p>}
      </div>
      {action && <div>{action}</div>}
    </div>
  );
}

export function EmptyState({ title = "No data", text, action, icon = "inbox" }: { title?: string; text: string; action?: ReactNode; icon?: string }) {
  return (
    <div className="bg-surface-container-low border border-dashed border-outline-variant/30 rounded-xl p-10 flex flex-col items-center justify-center text-center">
      <Icon name={icon} className="text-[36px] text-on-surface-variant/40 mb-3" />
      <strong className="text-base font-semibold text-on-surface mb-1">{title}</strong>
      <p className="text-sm text-on-surface-variant max-w-sm mb-4">{text}</p>
      {action}
    </div>
  );
}

export function ErrorState({ title = "Error", text, retry }: { title?: string; text: string; retry?: () => void }) {
  return (
    <div className="bg-error-container/10 border border-dashed border-error/30 rounded-xl p-10 flex flex-col items-center justify-center text-center">
      <Icon name="error" className="text-[36px] text-error mb-3" />
      <strong className="text-base font-semibold text-error mb-1">{title}</strong>
      <p className="text-sm text-error/80 max-w-sm mb-4">{text}</p>
      {retry && <Button onClick={retry} variant="danger">Retry</Button>}
    </div>
  );
}

export function LoadingState({ text = "Loading..." }: { text?: string }) {
  return (
    <div className="flex flex-col items-center justify-center p-12 text-on-surface-variant">
      <Icon name="progress_activity" className="animate-spin text-[32px] mb-4 text-primary" />
      <p className="text-sm">{text}</p>
    </div>
  );
}

export function StatePanel({ title, text, retry }: { title: string; text: string; retry?: () => void }) {
  return (
    <div className="bg-surface-container-low border border-dashed border-outline-variant/40 rounded-xl p-12 flex flex-col items-center justify-center text-center max-w-xl mx-auto my-12">
      <div className="w-12 h-12 rounded-full bg-surface-container-high flex items-center justify-center mb-4 text-on-surface-variant">
        <Icon name="info" className="text-[24px]" />
      </div>
      <h2 className="text-2xl font-semibold text-on-surface mb-2">{title}</h2>
      <p className="text-base text-on-surface-variant mb-6 max-w-md">{text}</p>
      {retry && (
        <Button onClick={retry} variant="secondary">
          <Icon name="refresh" className="text-[18px]" />
          Retry
        </Button>
      )}
    </div>
  );
}

export function Dialog({ isOpen, onClose, title, children }: { isOpen: boolean; onClose: () => void; title: string; children: ReactNode }) {
  if (!isOpen) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-surface/80 backdrop-blur-sm" onClick={onClose} />
      <div className="relative bg-surface-container rounded-xl shadow-lg border border-outline-variant/20 w-full max-w-lg overflow-hidden flex flex-col">
        <div className="flex items-center justify-between p-4 border-b border-outline-variant/20">
          <h2 className="text-lg font-semibold text-on-surface">{title}</h2>
          <IconButton icon="close" onClick={onClose} />
        </div>
        <div className="p-4 overflow-y-auto max-h-[70vh]">
      {children}
        </div>
      </div>
    </div>
  );
}

export function Drawer({ isOpen, onClose, title, children, side = "right" }: { isOpen: boolean; onClose: () => void; title: string; children: ReactNode; side?: "left" | "right" }) {
  if (!isOpen) return null;
  const sideClass = side === "right" ? "right-0 border-l" : "left-0 border-r";
  return (
    <div className="fixed inset-0 z-50 flex">
      <div className="absolute inset-0 bg-surface/50 backdrop-blur-sm" onClick={onClose} />
      <div className={`absolute top-0 bottom-0 ${sideClass} w-80 bg-surface-container border-outline-variant/20 shadow-lg flex flex-col`}>
        <div className="flex items-center justify-between p-4 border-b border-outline-variant/20">
          <h2 className="text-lg font-semibold text-on-surface">{title}</h2>
          <IconButton icon="close" onClick={onClose} />
        </div>
        <div className="p-4 overflow-y-auto flex-1">
      {children}
        </div>
      </div>
    </div>
  );
}

export function Tooltip({ content, children }: { content: string; children: ReactNode }) {
  return (
    <div className="group relative inline-block">
      {children}
      <div className="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-2 w-max max-w-xs opacity-0 transition-opacity group-hover:opacity-100 bg-surface-container-highest text-on-surface text-xs rounded px-2 py-1 z-50">
        {content}
      </div>
    </div>
  );
}

export function Table({ headers, rows }: { headers: string[]; rows: ReactNode[][] }) {
  return (
    <div className="w-full overflow-x-auto border border-outline-variant/20 rounded-xl">
      <table className="w-full text-left text-sm">
        <thead className="bg-surface-container-high border-b border-outline-variant/20 text-on-surface-variant font-semibold">
          <tr>
            {headers.map((h, i) => (
              <th key={i} className="px-4 py-3">{h}</th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-outline-variant/20 bg-surface-container">
          {rows.map((row, i) => (
            <tr key={i} className="hover:bg-surface-container-high/50 transition-colors">
              {row.map((cell, j) => (
                <td key={j} className="px-4 py-3 text-on-surface">{cell}</td>
              ))}
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={headers.length} className="px-4 py-8 text-center text-on-surface-variant">No data available</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

/** Backward-compatible alias for EmptyState */
export function Empty({ title, text, action }: { title?: string; text: string; action?: ReactNode }) {
  return <EmptyState title={title} text={text} action={action} />;
}

/** Surface container component */
export function Surface({ title, children, className = "", ...props }: HTMLAttributes<HTMLDivElement> & { title?: string }) {
  return (
    <div className={`bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 ${className}`} {...props}>
      {title ? <h4 className="font-headline-md text-sm font-semibold text-on-surface mb-2">{title}</h4> : null}
      {children}
    </div>
  );
}
