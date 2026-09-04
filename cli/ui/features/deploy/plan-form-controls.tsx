import type { ReactNode } from "react";

export function PlanField({ children, label }: { children: ReactNode; label: string }) {
  return (
    <label className="grid content-start gap-1 text-sm">
      <span className="font-medium">{label}</span>
      {children}
    </label>
  );
}

export function PlanCheck({ checked, label, onChange }: { checked: boolean; label: string; onChange: (checked: boolean) => void }) {
  return (
    <label className="flex min-h-10 cursor-pointer items-center gap-2 text-sm">
      <input
        checked={checked}
        className="h-4 w-4 accent-primary"
        onChange={(event) => onChange(event.target.checked)}
        type="checkbox"
      />
      {label}
    </label>
  );
}

export const planSelectClass = "min-h-10 w-full border border-outline-variant/40 bg-surface-container-lowest px-3 text-sm text-on-surface focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary";
