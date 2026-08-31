import { Button, Icon } from "@/components/ui/primitives";
import type { PublicHostnameAllocation, PublicHostnameQuota } from "@/lib/contracts/registry";

const labels: Record<PublicHostnameAllocation["status"], string> = {
  reserved: "Reserved", provisioning: "Provisioning", active: "Active", release_pending: "Release pending", failed: "Publication failed", released: "Released",
};

export function PublicHostnameQuotaPanel({ busy, canMutate, onAction, projectID, quota }: { busy: string; canMutate: boolean; onAction: (allocation: PublicHostnameAllocation, action: "release" | "retry") => void; projectID: string; quota: PublicHostnameQuota | null }) {
  if (!quota) return <section aria-label="Public hostname quota" aria-busy="true" className="h-16 animate-pulse border border-outline-variant/20 bg-surface-container-low" />;
  const held = [...new Map([...quota.allocations, ...(quota.project_allocations || [])].map((allocation) => [allocation.id, allocation])).values()].filter((allocation) => allocation.status !== "released");
  return <section aria-labelledby="public-hostname-quota-title" className="border border-outline-variant/30 bg-surface-container p-4">
    <div className="flex flex-wrap items-start justify-between gap-3"><div><p className="text-xs font-medium uppercase tracking-wider text-secondary">Public hostname quota</p><h2 className="mt-1 text-base font-semibold" id="public-hostname-quota-title">{quota.used}/{quota.limit} public hostnames used</h2></div><p className="text-sm text-on-surface-variant">{quota.remaining} remaining across your account</p></div>
    {held.length === 0 ? <p className="mt-3 text-sm text-on-surface-variant" role="status">No public hostnames are currently held.</p> : <ul aria-live="polite" className="mt-3 divide-y divide-outline-variant/20">{held.map((allocation) => {
      const ownedHere = allocation.project_id === projectID;
      const working = busy === `hostname-${allocation.id}`;
      return <li className="flex flex-col gap-3 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between" key={allocation.id}><div className="min-w-0"><p className="break-all font-mono text-sm">{allocation.hostname}</p><p className={allocation.status === "failed" || allocation.status === "release_pending" ? "mt-1 text-xs text-error" : "mt-1 text-xs text-on-surface-variant"}>{labels[allocation.status]}{!ownedHere ? " · another project" : ""}</p>{allocation.publication_error && <p className="mt-1 text-xs text-error">{allocation.publication_error}</p>}</div>{ownedHere && canMutate && <div className="flex gap-2">{allocation.status === "failed" && <Button disabled={working} onClick={() => onAction(allocation, "retry")} size="sm" variant="secondary"><Icon name="refresh" />Retry publication</Button>}<Button disabled={working || allocation.status === "release_pending"} onClick={() => onAction(allocation, "release")} size="sm" variant="outline">Release</Button></div>}</li>;
    })}</ul>}
  </section>;
}
