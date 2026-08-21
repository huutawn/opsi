"use client";

import { useMemo } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { deriveSecuritySummary } from "@/lib/presentation/security/model";

export function OverviewTab({ console }: { console: ConsoleController }) {
  const summary = useMemo(
    () => deriveSecuritySummary(console.state.audit, console.session, undefined, console.state.support),
    [console.state.audit, console.session, console.state.support]
  );

  const session = console.session;
  const project = console.state.project;
  const agentOk = session?.agent_connected === "ok";
  const cloudOk = session?.cloud_connected === "ok";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Authority & Credential Safeguards</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Security Overview</h2>
        </div>
      </div>

      {/* 4 Summary Stat Cards */}
      <div className="statusStrip grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-5 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">Loaded Audit Events</span>
          <div className="font-headline-lg text-2xl font-bold text-on-surface">{summary.totalLoadedEvents}</div>
          <span className="text-[11px] text-on-surface-variant">Bounded local history</span>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-5 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">Denied Operations</span>
          <div className={`font-headline-lg text-2xl font-bold ${summary.deniedEventsCount > 0 ? "text-error" : "text-status-ready"}`}>
            {summary.deniedEventsCount}
          </div>
          <span className="text-[11px] text-on-surface-variant">
            {summary.deniedEventsCount > 0 ? "Rejected by authorization" : "Zero unauthorized attempts"}
          </span>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-5 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">High-Impact Operations</span>
          <div className={`font-headline-lg text-2xl font-bold ${summary.highImpactEventsCount > 0 ? "text-status-warning" : "text-on-surface"}`}>
            {summary.highImpactEventsCount}
          </div>
          <span className="text-[11px] text-on-surface-variant">Rollback, node removal, storage destroy</span>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-5 shadow-sm space-y-2">
          <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">RBAC Scope</span>
          <div className="font-headline-lg text-2xl font-bold text-primary">
            {session?.role ? session.role.toUpperCase() : "OPERATOR"}
          </div>
          <span className="text-[11px] text-on-surface-variant truncate block">
            {session?.org_id ? `Org ${session.org_id}` : "Project scope"}
          </span>
        </div>
      </div>

      {/* 2-Column Split: Access Boundaries (Left) & Mutation Audit Timeline (Right) */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        {/* Left Column: Authority & Credential Safeguards */}
        <div className="lg:col-span-7 space-y-6">
          {/* Authenticated Boundary */}
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
              <div className="flex items-center gap-2">
                <Icon name="verified_user" className="text-primary text-[20px]" />
                <h3 className="font-headline-md text-base font-bold text-on-surface">Authenticated Session</h3>
              </div>
              <StatusBadge value={cloudOk ? "healthy" : "unavailable"} label={cloudOk ? "Cloud Ready" : "Cloud Offline"} />
            </div>

            <dl className="grid grid-cols-2 gap-4 text-xs">
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Signed-In Identity</dt>
                <dd className="font-body-md text-on-surface font-semibold mt-0.5">{session?.user_id || "Human operator"}</dd>
              </div>
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Role Permissions</dt>
                <dd className="font-body-md text-primary font-bold mt-0.5">{session?.role ? session.role.toUpperCase() : "OPERATOR"}</dd>
              </div>
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Organization Scope</dt>
                <dd className="font-code-md text-on-surface mt-0.5">{session?.org_id || "default"}</dd>
              </div>
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Project Scope</dt>
                <dd className="font-body-md text-on-surface mt-0.5">{project?.name || "None"} ({project?.id || "none"})</dd>
              </div>
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Node Agent Connectivity</dt>
                <dd className="mt-0.5">
                  <StatusBadge value={agentOk ? "healthy" : "unavailable"} label={agentOk ? "Agent Connected" : "Disconnected"} />
                </dd>
              </div>
              <div>
                <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Credential Store</dt>
                <dd className="font-body-md text-on-surface mt-0.5">OS Native Keychain (Zero browser storage)</dd>
              </div>
            </dl>
          </div>

          {/* Database Role Safeguards */}
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
              <div className="flex items-center gap-2">
                <Icon name="database" className="text-primary text-[20px]" />
                <h3 className="font-headline-md text-base font-bold text-on-surface">PostgreSQL Scoped Role Safeguards</h3>
              </div>
              <span className="font-label-sm text-[10px] text-status-ready bg-status-ready/10 px-2 py-0.5 rounded border border-status-ready/20">
                Least Privilege
              </span>
            </div>

            <p className="text-xs text-on-surface-variant">
              Application database roles are provisioned with strictly scoped capabilities. Superuser and database creation privileges are never granted.
            </p>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              {summary.scopedRoleSafety.map((attr) => (
                <div key={attr.attribute} className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 flex flex-col justify-between">
                  <code className="font-code-md text-xs text-primary font-bold">{attr.attribute}</code>
                  <span className="text-[11px] text-on-surface-variant mt-1">{attr.description}</span>
                </div>
              ))}
            </div>
          </div>

          {/* Break-Glass Policy */}
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
            <div className="flex items-center gap-2 border-b border-outline-variant/20 pb-3">
              <Icon name="security" className="text-primary text-[20px]" />
              <h3 className="font-headline-md text-base font-bold text-on-surface">Break-Glass & Safety Policy</h3>
            </div>

            {summary.breakGlassFacts ? (
              <dl className="grid grid-cols-2 gap-4 text-xs">
                <div>
                  <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Time-Limited Access</dt>
                  <dd className="font-body-md text-on-surface mt-0.5">{summary.breakGlassFacts.time_limited ? "Enforced" : "Disabled"}</dd>
                </div>
                <div>
                  <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Peer Approval Required</dt>
                  <dd className="font-body-md text-on-surface mt-0.5">{summary.breakGlassFacts.approval_required ? "Yes" : "No"}</dd>
                </div>
                <div>
                  <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Durable Audit Trail</dt>
                  <dd className="font-body-md text-status-ready mt-0.5">Append-Only Cryptographic Record</dd>
                </div>
                <div>
                  <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Secret Reveal Policy</dt>
                  <dd className="font-body-md text-on-surface mt-0.5">Explicit Double Confirmation with TTL</dd>
                </div>
              </dl>
            ) : (
              <p className="text-xs text-on-surface-variant">Break-glass policy facts loaded from local edge authority.</p>
            )}
          </div>
        </div>

        {/* Right Column: Recent Denied Actions and High Impact Operations */}
        <div className="lg:col-span-5 space-y-6">
          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
              <div className="flex items-center gap-2">
                <Icon name="block" className="text-error text-[20px]" />
                <h3 className="font-headline-md text-base font-bold text-on-surface">Recent Denied Actions</h3>
              </div>
              <Button onClick={() => console.navigate({ view: "security", tab: "audit" })} size="sm" variant="outline">
                Audit Trail →
              </Button>
            </div>

            {summary.recentDeniedEvents.length ? (
              <div className="space-y-2">
                {summary.recentDeniedEvents.map((evt) => (
                  <div
                    key={evt.id}
                    onClick={() => console.navigate({ view: "security", tab: "audit" })}
                    className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 hover:bg-surface-container-high transition-all cursor-pointer flex items-center justify-between gap-3"
                  >
                    <div className="min-w-0">
                      <strong className="font-body-md text-xs text-on-surface block truncate">{evt.action}</strong>
                      <span className="text-[11px] text-on-surface-variant font-code-md">{evt.actor.label} • {evt.formattedTime}</span>
                    </div>
                    <StatusBadge value="denied" />
                  </div>
                ))}
              </div>
            ) : (
              <Empty text="No unauthorized operations detected in recent audit history." title="Zero Denied Operations" />
            )}
          </div>

          <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
              <div className="flex items-center gap-2">
                <Icon name="warning" className="text-status-warning text-[20px]" />
                <h3 className="font-headline-md text-base font-bold text-on-surface">High-Impact Operations</h3>
              </div>
            </div>

            {summary.recentHighImpactEvents.length ? (
              <div className="space-y-2">
                {summary.recentHighImpactEvents.map((evt) => (
                  <div
                    key={evt.id}
                    onClick={() => console.navigate({ view: "security", tab: "audit" })}
                    className="p-3 bg-surface-container rounded-lg border border-outline-variant/20 hover:bg-surface-container-high transition-all cursor-pointer flex items-center justify-between gap-3"
                  >
                    <div className="min-w-0">
                      <strong className="font-body-md text-xs text-on-surface block truncate">{evt.action}</strong>
                      <span className="text-[11px] text-on-surface-variant font-code-md">{evt.actor.label} • {evt.impactReason || evt.formattedTime}</span>
                    </div>
                    <span className="font-label-sm text-[10px] text-status-warning bg-status-warning/10 px-2 py-0.5 rounded border border-status-warning/20 shrink-0">
                      HIGH IMPACT
                    </span>
                  </div>
                ))}
              </div>
            ) : (
              <Empty text="No high-impact destructive changes recorded." title="No High-Impact Actions" />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
