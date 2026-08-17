"use client";

import { useMemo } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
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
        item.action.startsWith("ADMIN_BOOTSTRAP"),
    );
  }, [console.state.audit]);

  return (
    <div className="securityStack">
      <section className="securitySection" aria-labelledby="access-id-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Identity & Authority Context</p>
            <h2 id="access-id-title">Access & Identities</h2>
            <p>Factual authenticated identities, project access scopes, machine/agent authorities, and credential status.</p>
          </div>
          <StatusBadge value={console.session?.authenticated ? "ready" : "unavailable"} />
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(300px, 1fr))", gap: 16, marginTop: 14 }}>
          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Authenticated Session</h3>
            <dl className="reviewFacts">
              <div>
                <dt>Signed in actor</dt>
                <dd>{session?.user_id || "Human actor"}</dd>
              </div>
              <div>
                <dt>Assigned role</dt>
                <dd><b>{session?.role ? session.role.toUpperCase() : "OPERATOR"}</b></dd>
              </div>
              <div>
                <dt>Organization scope</dt>
                <dd><code>{session?.org_id || "default"}</code></dd>
              </div>
              <div>
                <dt>Project scope</dt>
                <dd>{project?.name || "None"} (<code>{project?.id || "none"}</code>)</dd>
              </div>
            </dl>
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Authority Connections</h3>
            <dl className="reviewFacts">
              <div>
                <dt>Cloud control plane</dt>
                <dd><StatusBadge value={session?.cloud_connected === "ok" ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>Node agent state</dt>
                <dd><StatusBadge value={session?.agent_connected === "ok" ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>OS keychain PAT</dt>
                <dd>Stored securely in OS Keychain</dd>
              </div>
              <div>
                <dt>Surface policy</dt>
                <dd>Read-only access center; zero credentials exposed</dd>
              </div>
            </dl>
          </div>
        </div>
      </section>

      <section className="securitySection" aria-labelledby="machine-auth-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Execution identities</p>
            <h2 id="machine-auth-title">Connected Nodes & Machine Authorities</h2>
            <p>Active runtime agents executing workloads with scoped machine credentials.</p>
          </div>
          <span className="categoryPill">{nodes.length} node(s)</span>
        </div>

        {nodes.length ? (
          <div style={{ display: "grid", gap: 8, marginTop: 14 }}>
            {nodes.map((node) => (
              <div
                key={node.id}
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 12,
                  padding: "10px 14px",
                  border: "1px solid var(--line)",
                  borderRadius: 6,
                  background: "var(--surface)",
                }}
              >
                <div>
                  <strong style={{ fontSize: 13 }}>{node.name || node.id}</strong>
                  <div style={{ fontSize: 11, color: "var(--muted)", marginTop: 2 }}>
                    Role: <b>{node.role}</b> {node.last_seen_at ? `· Last seen ${node.last_seen_at}` : ""}
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                  <StatusBadge value={node.status} />
                  <button
                    type="button"
                    className="secondary"
                    onClick={() => console.navigate({ view: "infrastructure", tab: "servers", server: node.id })}
                    style={{ fontSize: 11, padding: "4px 8px" }}
                  >
                    Open Server &rarr;
                  </button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No connected nodes or machine identities reported." />
        )}
      </section>

      <section className="securitySection" aria-labelledby="bootstrap-auth-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Bootstrap identities</p>
            <h2 id="bootstrap-auth-title">Server Bootstrap Sessions</h2>
            <p>Ephemeral bootstrap credential records and node joining status.</p>
          </div>
          <span className="categoryPill">{sessions.length} session(s)</span>
        </div>

        {sessions.length ? (
          <div style={{ display: "grid", gap: 8, marginTop: 14 }}>
            {sessions.map((sess) => (
              <div
                key={sess.id}
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 12,
                  padding: "10px 14px",
                  border: "1px solid var(--line)",
                  borderRadius: 6,
                  background: "var(--surface)",
                }}
              >
                <div>
                  <code style={{ fontSize: 12 }}>{sess.id}</code>
                  <div style={{ fontSize: 11, color: "var(--muted)", marginTop: 2 }}>
                    Host: {sess.public_host || "unspecified"} · Role: <b>{sess.role}</b>
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                  <StatusBadge value={sess.status} />
                  <button
                    type="button"
                    className="secondary"
                    onClick={() => console.navigate({ view: "infrastructure", tab: "servers", session: sess.id })}
                    style={{ fontSize: 11, padding: "4px 8px" }}
                  >
                    Open Bootstrap &rarr;
                  </button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No active or recent bootstrap sessions." />
        )}
      </section>

      <section className="securitySection" aria-labelledby="service-binding-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Workload Credential Scope</p>
            <h2 id="service-binding-title">Application Credential Status</h2>
            <p>Application workload permissions operate with least-privilege PostgreSQL roles. Credential mutation is managed canonically in Infrastructure and Delivery.</p>
          </div>
        </div>

        {services.length ? (
          <div style={{ display: "grid", gap: 8, marginTop: 14 }}>
            {services.map((svc) => (
              <div
                key={svc.id}
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 12,
                  padding: "10px 14px",
                  border: "1px solid var(--line)",
                  borderRadius: 6,
                  background: "var(--surface)",
                }}
              >
                <div>
                  <strong style={{ fontSize: 13 }}>{svc.name}</strong>
                  <div style={{ fontSize: 11, color: "var(--muted)", marginTop: 2 }}>
                    Type: <b>{svc.type}</b> · Replicas: {svc.replicas ?? 1} · Source: {svc.source_type || "managed"}
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                  <StatusBadge value={svc.status} />
                  <button
                    type="button"
                    className="secondary"
                    onClick={() => console.navigate({ view: "services", service: svc.id })}
                    style={{ fontSize: 11, padding: "4px 8px" }}
                  >
                    Open Application &rarr;
                  </button>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <Empty text="No application services configured." />
        )}
      </section>

      <section className="securitySection" aria-labelledby="access-audit-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Loaded history</p>
            <h2 id="access-audit-title">Access & Credential Audit</h2>
            <p>Chronological record of authentication events, role checks, and credential lifecycles.</p>
          </div>
          <span className="categoryPill">{accessAuditEvents.length} event(s)</span>
        </div>
        {accessAuditEvents.length ? (
          <AuditList rows={accessAuditEvents} />
        ) : (
          <Empty text="No access or credential audit events were returned in loaded history." />
        )}
      </section>
    </div>
  );
}
