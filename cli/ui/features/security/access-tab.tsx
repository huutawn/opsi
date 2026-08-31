"use client";

import { useMemo } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { AuditList } from "@/features/security/audit-tab";

export function AccessTab({ console }: { console: ConsoleController }) {
  const session = console.session;
  const project = console.state.project;
  const nodes = console.state.nodes ?? [];
  const sessions = console.state.sessions ?? [];
  const services = console.state.services.filter((item) => item.type === "application");

  const accessAuditEvents = useMemo(() => {
    return (console.state.audit ?? []).filter(
      (item) =>
        item.resource_type === "secret" ||
        item.action.startsWith("AUTH_") ||
        item.action.startsWith("PAT_") ||
        item.action.startsWith("RBAC_") ||
        item.action.startsWith("SECRET_") ||
        item.action.startsWith("RESOURCE_BINDING_") ||
        item.action.startsWith("AGENT_") ||
        item.action.startsWith("ADMIN_BOOTSTRAP")
    );
  }, [console.state.audit]);

  return (
    <div className="space-y-6">
      <div className="border-b border-outline-variant/20 pb-4">
        <h2 className="font-headline-lg text-xl font-bold text-on-surface">Access & Identities</h2>
        <p className="text-xs text-on-surface-variant mt-1">Factual authenticated identities, project access scopes, machine/agent authorities, and credential status.</p>
      </div>

      {/* Session and Authority Header Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <div className="flex items-center gap-2">
              <Icon name="person" className="text-primary text-[20px]" />
              <h3 className="font-headline-md text-base font-bold text-on-surface">Authenticated Session</h3>
            </div>
            <StatusBadge value={session?.authenticated ? "healthy" : "unavailable"} />
          </div>

          <dl className="grid grid-cols-2 gap-4 text-xs">
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">User Identity</dt>
              <dd className="font-body-md text-on-surface font-semibold mt-0.5">{session?.user_id || "Human operator"}</dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Role Scope</dt>
              <dd className="font-body-md text-primary font-bold mt-0.5">{session?.role ? session.role.toUpperCase() : "OPERATOR"}</dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Organization</dt>
              <dd className="font-code-md text-on-surface mt-0.5">{session?.org_id || "default"}</dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Project Target</dt>
              <dd className="font-body-md text-on-surface mt-0.5">{project?.name || "None"} ({project?.id || "none"})</dd>
            </div>
          </dl>
        </div>

        <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
          <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
            <div className="flex items-center gap-2">
              <Icon name="hub" className="text-primary text-[20px]" />
              <h3 className="font-headline-md text-base font-bold text-on-surface">Authority Connections</h3>
            </div>
            <span className="font-label-sm text-xs text-status-ready font-semibold">Verified</span>
          </div>

          <dl className="grid grid-cols-2 gap-4 text-xs">
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Cloud Control Plane</dt>
              <dd className="mt-0.5"><StatusBadge value={session?.cloud_connected === "ok" ? "healthy" : "unavailable"} /></dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Node Agent Link</dt>
              <dd className="mt-0.5"><StatusBadge value={session?.agent_connected === "ok" ? "healthy" : "unavailable"} /></dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Local PAT Store</dt>
              <dd className="font-body-md text-on-surface mt-0.5">OS Native Keychain</dd>
            </div>
            <div>
              <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Policy Enforcement</dt>
              <dd className="font-body-md text-on-surface mt-0.5">Read-only Center (Zero secret exposure)</dd>
            </div>
          </dl>
        </div>
      </div>

      {/* Connected Nodes & Machine Authorities */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
          <div className="flex items-center gap-2">
            <Icon name="dns" className="text-primary text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Connected Nodes & Machine Authorities</h3>
          </div>
          <span className="text-xs text-on-surface-variant font-code-md">{nodes.length} nodes</span>
        </div>

        {nodes.length ? (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {nodes.map((node) => (
              <div
                key={node.id}
                className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 flex items-center justify-between gap-3"
              >
                <div>
                  <strong className="font-body-md text-sm text-on-surface block">{node.name || node.id}</strong>
                  <span className="text-xs text-on-surface-variant">
                    Role: {node.role} {node.last_seen_at ? `• Seen ${node.last_seen_at}` : ""}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <StatusBadge value={node.status} />
                  <Button
                    onClick={() => console.navigate({ view: "observability", tab: "servers", server: node.id })}
                    size="sm"
                    variant="outline"
                  >
                    Open Server →
                  </Button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No connected node identities reported in active inventory." title="No Nodes Connected" />
        )}
      </div>

      {/* Server Bootstrap Sessions */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
          <div className="flex items-center gap-2">
            <Icon name="terminal" className="text-primary text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Server Bootstrap Sessions</h3>
          </div>
          <span className="text-xs text-on-surface-variant font-code-md">{sessions.length} sessions</span>
        </div>

        {sessions.length ? (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {sessions.map((sess) => (
              <div
                key={sess.id}
                className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 flex items-center justify-between gap-3"
              >
                <div>
                  <strong className="font-body-md text-sm text-on-surface block">{sess.public_host || sess.id}</strong>
                  <span className="text-xs text-on-surface-variant">
                    Role: {sess.role} • Attempts: {sess.attempt_count}/{sess.max_attempts}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <StatusBadge value={sess.status} />
                  <Button
                    onClick={() => console.navigate({ view: "observability", tab: "servers", session: sess.id })}
                    size="sm"
                    variant="outline"
                  >
                    Open Session →
                  </Button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No active or pending server bootstrap sessions." title="No Bootstrap Sessions" />
        )}
      </div>

      {/* Application Credential Status */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
          <div className="flex items-center gap-2">
            <Icon name="layers" className="text-primary text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Application Credential Status</h3>
          </div>
        </div>

        {services.length ? (
          <div className="space-y-2">
            {services.map((svc) => (
              <div
                key={svc.id}
                className="p-4 bg-surface-container rounded-xl border border-outline-variant/20 flex items-center justify-between gap-3"
              >
                <div>
                  <strong className="font-body-md text-sm text-on-surface block">{svc.name}</strong>
                  <span className="text-xs text-on-surface-variant">Type: {svc.type} • Replicas: {svc.replicas ?? 1}</span>
                </div>
                <div className="flex items-center gap-2">
                  <StatusBadge value={svc.status} />
                  <Button
                    onClick={() => console.navigate({ view: "observability", tab: "applications", service: svc.id })}
                    size="sm"
                    variant="outline"
                  >
                    Open Application →
                  </Button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No application services configured." title="No Services" />
        )}
      </div>

      {/* Access & Credential Audit Timeline */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-4">
        <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
          <div className="flex items-center gap-2">
            <Icon name="history" className="text-primary text-[20px]" />
            <h3 className="font-headline-md text-base font-bold text-on-surface">Access & Credential Audit</h3>
          </div>
          <span className="text-xs text-on-surface-variant">{accessAuditEvents.length} events</span>
        </div>

        {accessAuditEvents.length ? (
          <AuditList rows={accessAuditEvents} />
        ) : (
          <Empty text="No access or credential audit events in loaded history." title="No Access Events" />
        )}
      </div>
    </div>
  );
}
