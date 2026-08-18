import React, { type ButtonHTMLAttributes, type HTMLAttributes, type InputHTMLAttributes, type ReactNode, type Ref, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { normalizeStatus, type PresentationStatus } from "@/lib/presentation/project";

export function Icon({ name, className = "" }: { name: string; className?: string }) {
  return <span aria-hidden="true" className={`material-symbols-outlined select-none ${className}`} data-icon={name} />;
}

export function StatusBadge({
  value,
  label,
  className = "",
}: {
  value: string | PresentationStatus;
  label?: string;
  className?: string;
}) {
  const normalized = normalizeStatus(value);
  const display = label ?? formatStatusText(value);

  switch (normalized) {
    case "healthy":
      return (
        <span className={`status healthy inline-flex items-center gap-1.5 px-3 py-1 bg-status-ready/10 text-status-ready rounded-full border border-status-ready/20 font-label-sm text-label-sm ${className}`}>
          <Icon name="check_circle" className="statusIcon text-[14px] text-status-ready" />
          <span className="status healthy">{display}</span>
        </span>
      );
    case "failed":
      return (
        <span className={`status failed inline-flex items-center gap-1.5 px-3 py-1 bg-status-failed/10 text-status-failed rounded-full border border-status-failed/20 font-label-sm text-label-sm ${className}`}>
          <Icon name="error" className="statusIcon text-[14px] text-status-failed" />
          <span className="status failed">{display}</span>
        </span>
      );
    case "in_progress":
      return (
        <span className={`status in_progress inline-flex items-center gap-1.5 px-3 py-1 bg-status-progress/10 text-status-progress rounded-full border border-status-progress/20 font-label-sm text-label-sm ${className}`}>
          <Icon name="sync" className="statusIcon text-[14px] text-status-progress animate-spin" />
          <span className="status in_progress">{display}</span>
        </span>
      );
    case "degraded":
      return (
        <span className={`status degraded inline-flex items-center gap-1.5 px-3 py-1 bg-status-warning/10 text-status-warning rounded-full border border-status-warning/20 font-label-sm text-label-sm ${className}`}>
          <Icon name="warning" className="statusIcon text-[14px] text-status-warning" />
          <span className="status degraded">{display}</span>
        </span>
      );
    default:
      return (
        <span className={`status unknown inline-flex items-center gap-1.5 px-3 py-1 bg-surface-container-highest text-on-surface-variant rounded-full border border-outline-variant/30 font-label-sm text-label-sm ${className}`}>
          <span className="statusIcon w-1.5 h-1.5 rounded-full bg-status-unknown"></span>
          <span className="status unknown">{display}</span>
        </span>
      );
  }
}

function formatStatusText(val: string | PresentationStatus): string {
  const str = String(val);
  if (str === "healthy") return "Healthy";
  if (str === "failed") return "Failed";
  if (str === "in_progress") return "In Progress";
  if (str === "degraded") return "Degraded";
  if (str === "unavailable") return "Unavailable";
  if (str === "unknown") return "Unknown";
  return str.replaceAll("_", " ");
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "danger" | "warning" | "ghost" | "outline";
  size?: "sm" | "md" | "lg";
  ref?: Ref<HTMLButtonElement>;
}

export function Button({
  variant = "primary",
  size = "md",
  className = "",
  children,
  ref,
  ...props
}: ButtonProps) {
  const sizeClasses =
    size === "sm"
      ? "px-3 py-1.5 text-xs"
      : size === "lg"
        ? "px-6 py-3 text-base"
        : "px-4 py-2 text-sm font-medium";

  let variantClasses = "";
  switch (variant) {
    case "primary":
      variantClasses = "bg-primary text-on-primary hover:bg-primary-fixed transition-colors shadow-sm font-semibold disabled:opacity-50 disabled:cursor-not-allowed";
      break;
    case "secondary":
      variantClasses = "bg-surface-container-high text-on-surface hover:bg-surface-container-highest transition-colors shadow-sm disabled:opacity-50 disabled:cursor-not-allowed";
      break;
    case "danger":
      variantClasses = "bg-error-container/20 text-error hover:bg-error-container/30 transition-colors shadow-sm disabled:opacity-50 disabled:cursor-not-allowed";
      break;
    case "warning":
      variantClasses = "bg-status-warning/10 text-status-warning hover:bg-status-warning/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed";
      break;
    case "ghost":
      variantClasses = "text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest transition-colors disabled:opacity-50 disabled:cursor-not-allowed";
      break;
    case "outline":
      variantClasses = "border border-outline-variant/30 text-on-surface hover:bg-surface-container-high transition-colors disabled:opacity-50 disabled:cursor-not-allowed";
      break;
  }

  return (
    <button
      ref={ref}
      className={`rounded-lg inline-flex items-center justify-center gap-2 cursor-pointer font-body-md transition-all ${sizeClasses} ${variantClasses} ${className}`}
      {...props}
    >
      {children}
    </button>
  );
}

export function IconButton({
  icon,
  title,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  icon: string;
  title?: string;
}) {
  return (
    <button
      title={title}
      aria-label={title}
      className={`iconButton min-w-[40px] min-h-[40px] flex items-center justify-center p-2 rounded-full text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed ${className}`}
      {...props}
    >
      <Icon name={icon} className="text-[20px]" />
    </button>
  );
}

export function Card({
  className = "",
  children,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={`bg-surface-container rounded-xl p-6 shadow-sm border border-outline-variant/20 ${className}`}
      {...props}
    >
      {children}
    </div>
  );
}

export function Surface({
  title,
  children,
  className = "",
}: {
  title?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`bg-surface-container rounded-xl p-6 shadow-sm border border-outline-variant/20 space-y-4 ${className}`}>
      {title ? <h2 className="font-headline-md text-headline-md text-on-surface">{title}</h2> : null}
      {children}
    </section>
  );
}

export function PageHeader({
  title,
  description,
  eyebrow,
  action,
  icon,
}: {
  title: string;
  description?: string;
  eyebrow?: string;
  action?: ReactNode;
  icon?: string;
}) {
  return (
    <div className="pageHeader flex flex-col sm:flex-row sm:items-center justify-between pb-margin-desktop gap-4">
      <div className="flex items-center gap-4">
        {icon ? (
          <div className="p-3 bg-surface-container-high rounded-xl shadow-sm flex items-center justify-center">
            <Icon name={icon} className="text-primary text-[28px]" />
          </div>
        ) : null}
        <div className="flex flex-col">
          {eyebrow ? (
            <span className="font-label-sm text-label-sm text-on-surface-variant uppercase tracking-wider mb-1">
              {eyebrow}
            </span>
          ) : null}
          <h1 className="font-headline-lg text-headline-lg text-on-surface">{title}</h1>
          {description ? (
            <span className="font-body-md text-body-md text-on-surface-variant mt-1">{description}</span>
          ) : null}
        </div>
      </div>
      {action ? <div className="flex items-center gap-3 shrink-0">{action}</div> : null}
    </div>
  );
}

export function StatePanel({
  title,
  text,
  retry,
}: {
  title: string;
  text: string;
  retry?: () => void;
}) {
  return (
    <div className="bg-surface-container-low border border-dashed border-outline-variant/40 rounded-xl p-12 flex flex-col items-center justify-center text-center max-w-xl mx-auto my-12">
      <div className="w-12 h-12 rounded-full bg-surface-container-high flex items-center justify-center mb-4 text-on-surface-variant">
        <Icon name="info" className="text-[24px]" />
      </div>
      <h2 className="font-headline-md text-headline-md text-on-surface mb-2">{title}</h2>
      <p className="font-body-md text-body-md text-on-surface-variant mb-6 max-w-md">{text}</p>
      {retry ? (
        <Button onClick={retry} variant="secondary">
          <Icon name="refresh" className="text-[18px]" />
          Retry
        </Button>
      ) : null}
    </div>
  );
}

export function Empty({
  title = "No data yet",
  text,
  action,
}: {
  title?: string;
  text: string;
  action?: ReactNode;
}) {
  return (
    <div className="bg-surface-container-low border border-dashed border-outline-variant/30 rounded-xl p-10 flex flex-col items-center justify-center text-center">
      <Icon name="inbox" className="text-[36px] text-on-surface-variant/40 mb-3" />
      <strong className="font-headline-md text-base text-on-surface mb-1">{title}</strong>
      <p className="font-body-md text-sm text-on-surface-variant max-w-sm mb-4">{text}</p>
      {action}
    </div>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={`w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-body-md text-sm text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50 disabled:opacity-50 disabled:cursor-not-allowed ${props.className || ""}`}
      {...props}
    />
  );
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className="relative w-full">
      <select
        className={`w-full bg-surface-container-highest border border-outline-variant/30 text-on-surface font-body-md text-sm rounded-lg py-3 pl-3 pr-10 appearance-none focus:outline-none focus:border-primary/50 transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed ${props.className || ""}`}
        {...props}
      >
        {props.children}
      </select>
      <Icon
        name="expand_more"
        className="absolute right-3 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[20px]"
      />
    </div>
  );
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={`w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-body-md text-sm text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50 disabled:opacity-50 disabled:cursor-not-allowed ${props.className || ""}`}
      {...props}
    />
  );
}
