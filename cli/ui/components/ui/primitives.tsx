import React, { ButtonHTMLAttributes, HTMLAttributes, InputHTMLAttributes, ReactNode, Ref, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";

export function Icon({ name, className = "" }: { name: string; className?: string }) {
  return (
    <svg
      aria-hidden="true"
      className={`icon select-none ${className}`}
      data-icon={name}
      fill="none"
      focusable="false"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.9"
      viewBox="0 0 24 24"
    >
      <IconGlyph name={name} />
    </svg>
  );
}

/**
 * Opsi's canonical, dependency-free icon set.  Icons deliberately render as
 * SVG paths instead of a ligature font so the Local Edge UI never relies on a
 * remote font request or falls back to visible identifier text.
 */
function IconGlyph({ name }: { name: string }) {
  switch (name) {
    case "search": return <><circle cx="10.8" cy="10.8" r="6.2" /><path d="m16 16 4 4" /></>;
    case "close": return <><path d="m6 6 12 12M18 6 6 18" /></>;
    case "add": return <path d="M12 5v14M5 12h14" />;
    case "delete": return <><path d="M4 7h16M10 11v6M14 11v6M9 7l1-2h4l1 2M6 7l1 13h10l1-13" /></>;
    case "check": return <path d="m5 12 4 4L19 6" />;
    case "check_circle": return <><circle cx="12" cy="12" r="8.5" /><path d="m8 12 2.6 2.7L16.5 9" /></>;
    case "error": return <><circle cx="12" cy="12" r="8.5" /><path d="M12 8v5M12 16h.01" /></>;
    case "warning": return <><path d="m12 3 9 17H3L12 3Z" /><path d="M12 9v4M12 16h.01" /></>;
    case "block": return <><circle cx="12" cy="12" r="8.5" /><path d="m6 6 12 12" /></>;
    case "info": return <><circle cx="12" cy="12" r="8.5" /><path d="M12 11v5M12 8h.01" /></>;
    case "refresh": case "sync": case "rotate_right": return <><path d="M20 11a8 8 0 1 0 1 4" /><path d="M20 5v6h-6" /></>;
    case "progress_activity": return <path d="M12 4a8 8 0 1 0 7.4 5" />;
    case "notifications": return <><path d="M18 10a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" /></>;
    case "settings": return <><circle cx="12" cy="12" r="3" /><path d="M19 13.5v-3l-2-.6-.8-1.8 1-1.8-2.1-2.1-1.8 1-1.8-.8L11 3H8l-.6 2.1-1.8.8-1.8-1-2.1 2.1 1 1.8-.8 1.8L0 11v3l2.1.6.8 1.8-1 1.8 2.1 2.1 1.8-1 1.8.8L8 22h3l.6-2.1 1.8-.8 1.8 1 2.1-2.1-1-1.8.8-1.8 2.1-.6Z" /></>;
    case "menu": return <path d="M4 7h16M4 12h16M4 17h16" />;
    case "arrow_forward": case "chevron_right": return <path d="M5 12h13m-5-5 5 5-5 5" />;
    case "expand_more": case "arrow_drop_down": return <path d="m6 9 6 6 6-6" />;
    case "unfold_more": return <path d="m8 9 4-4 4 4M8 15l4 4 4-4" />;
    case "undo": return <path d="M9 8 5 12l4 4M5 12h8a5 5 0 0 1 5 5" />;
    case "play_arrow": return <path d="m9 6 10 6-10 6V6Z" fill="currentColor" stroke="none" />;
    case "pause": return <path d="M8 6v12M16 6v12" />;
    case "grid_view": case "dashboard": return <><rect x="4" y="4" width="6" height="6" rx="1" /><rect x="14" y="4" width="6" height="6" rx="1" /><rect x="4" y="14" width="6" height="6" rx="1" /><rect x="14" y="14" width="6" height="6" rx="1" /></>;
    case "table_rows": return <><path d="M5 6h14M5 12h14M5 18h14" /><path d="M8 5v14" /></>;
    case "layers": return <><path d="m12 4 8 4-8 4-8-4 8-4Z" /><path d="m4 12 8 4 8-4M4 16l8 4 8-4" /></>;
    case "account_tree": return <><circle cx="6" cy="5" r="1.5" /><circle cx="18" cy="8" r="1.5" /><circle cx="18" cy="18" r="1.5" /><path d="M7.5 5H12v13h4.5M12 8h4.5" /></>;
    case "rocket_launch": return <><path d="M14 5c2.5-2 5-2 5-2s0 2.5-2 5l-5 5-4-4 6-4Z" /><path d="m8 9-3 1-1 4 4-1M11 14l-1 5 4-1 1-3M8 16l-3 3" /><circle cx="15" cy="7" r="1" /></>;
    case "dns": case "storage": return <><rect x="4" y="4" width="16" height="6" rx="1" /><rect x="4" y="14" width="16" height="6" rx="1" /><path d="M7 7h.01M7 17h.01M10 7h6M10 17h6" /></>;
    case "database": return <><ellipse cx="12" cy="5.5" rx="7" ry="2.5" /><path d="M5 5.5v7c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5v-7M5 12.5v6C5 20 8.1 21 12 21s7-1 7-2.5v-6" /></>;
    case "memory": return <><rect x="6" y="6" width="12" height="12" rx="1" /><path d="M9 3v3m3-3v3m3-3v3M9 18v3m3-3v3m3-3v3M3 9h3m-3 3h3m-3 3h3m12-6h3m-3 3h3m-3 3h3" /></>;
    case "monitoring": return <><path d="M4 18V6M4 18h16" /><path d="m7 15 3-4 3 2 5-6" /></>;
    case "security": case "verified_user": case "shield": return <><path d="M12 3 19 6v5c0 4.5-3 7.5-7 10-4-2.5-7-5.5-7-10V6l7-3Z" /><path d="m9 12 2 2 4-4" /></>;
    case "network_check": return <><path d="M4 18h16M6 15a8.5 8.5 0 0 1 12 0M9 12a4.3 4.3 0 0 1 6 0" /><circle cx="12" cy="18" r="1" fill="currentColor" stroke="none" /></>;
    case "public": return <><circle cx="12" cy="12" r="8.5" /><path d="M3.8 12h16.4M12 3.5c2.2 2.4 3.3 5.2 3.3 8.5S14.2 18.1 12 20.5c-2.2-2.4-3.3-5.2-3.3-8.5S9.8 5.9 12 3.5" /></>;
    case "folder": return <path d="M3 7h7l2 2h9v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z" />;
    case "home": return <><path d="m4 11 8-7 8 7v9H4v-9Z" /><path d="M10 20v-6h4v6" /></>;
    case "person": return <><circle cx="12" cy="8" r="3" /><path d="M5 20c.7-3 2.8-5 7-5s6.3 2 7 5" /></>;
    case "logout": return <><path d="M10 5H5v14h5M14 8l4 4-4 4M9 12h9" /></>;
    case "api": case "hub": return <><circle cx="12" cy="12" r="2" /><circle cx="5" cy="6" r="1.5" /><circle cx="19" cy="6" r="1.5" /><circle cx="12" cy="19" r="1.5" /><path d="m10.5 10.5-4-3m7 3 4-3m-5 6v4" /></>;
    default: return <><rect x="5" y="5" width="14" height="14" rx="3" /><path d="M9 12h6" /></>;
  }
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
