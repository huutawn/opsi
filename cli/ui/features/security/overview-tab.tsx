"use client";

import { useMemo } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { deriveSecuritySummary } from "@/lib/presentation/security/model";

export function OverviewTab({ console }: { console: ConsoleController }) {
  const summary = useMemo(
    () => deriveSecuritySummary(console.state.audit, console.session, undefined, console.state.support),
    [console.state.audit, console.session, console.state.support],
  );

  const session = console.session;
  const project = console.state.project;
  const agentOk = session?.agent_connected === "ok";
  const cloudOk = session?.cloud_connected === "ok";

  return (
    <div className="securityStack">
      <section className="securitySection" aria-labelledby="sec-overview-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Security & Audit Center</p>
            <h2 id="sec-overview-title">Security Overview</h2>
            <p>Factual authority boundaries, recent audit highlights, and credential safety across this project.</p>
          </div>
          <StatusBadge value={console.state.status === "ready" ? "ready" : "unavailable"} />
        </div>

        <div className="statusStrip" style={{ marginTop: 16 }}>
          <div>
            <span className="label">Loaded Audit Events</span>
            <strong>{summary.totalLoadedEvents}</strong>
            <small>Bounded local history</small>
          </div>
          <div>
            <span className="label">Denied Operations</span>
            <strong style={{ color: summary.deniedEventsCount > 0 ? "var(--bad)" : "inherit" }}>
              {summary.deniedEventsCount}
            </strong>
            <small>{summary.deniedEventsCount > 0 ? "Rejected by authorization" : "None in loaded history"}</small>
          </div>
          <div>
            <span className="label">High-Impact Operations</span>
            <strong style={{ color: summary.highImpactEventsCount > 0 ? "var(--warn)" : "inherit" }}>
              {summary.highImpactEventsCount}
            </strong>
            <small>Destructive, rollback, or revoke</small>
          </div>
          <div>
            <span className="label">Access Role</span>
            <strong>{session?.role ? session.role.toUpperCase() : "AUTHENTICATED"}</strong>
            <small>{session?.org_id ? `Org ${session.org_id}` : "Project scope"}</small>
          </div>
        </div>
      </section>

      <section className="securitySection" aria-labelledby="sec-highlights-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Loaded history</p>
            <h2 id="sec-highlights-title">Security Highlights</h2>
            <p>Recent denied authorization events and high-impact operations requiring operational awareness.</p>
          </div>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(320px, 1fr))", gap: 16, marginTop: 14 }}>
          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14, display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <span>Recent Denied Actions</span>
              <span className="categoryPill">{summary.recentDeniedEvents.length}</span>
            </h3>
            {summary.recentDeniedEvents.length ? (
              <div style={{ display: "grid", gap: 8 }}>
                {summary.recentDeniedEvents.map((event) => (
                  <button
                    key={event.id}
                    type="button"
                    onClick={() => console.navigate({ view: "security", tab: "audit" })}
                    style={{
                      display: "grid",
                      textAlign: "left",
                      gap: 4,
                      padding: 10,
                      border: "1px solid var(--line)",
                      borderRadius: 4,
                      background: "var(--surface)",
                      cursor: "pointer",
                    }}
                  >
                    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                      <strong style={{ fontSize: 12 }}>{event.action}</strong>
                      <StatusBadge value={event.outcome} />
                    </div>
                    <span style={{ fontSize: 11, color: "var(--muted)" }}>
                      {event.actor.label} ({event.actor.identifier}) · {event.formattedTime}
                    </span>
                    <span style={{ fontSize: 11, color: "var(--ink)" }}>{event.targetDisplay}</span>
                  </button>
                ))}
              </div>
            ) : (
              <p style={{ margin: 0, fontSize: 12, color: "var(--muted)" }}>
                No authorization denials in loaded audit history.
              </p>
            )}
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14, display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <span>High-Impact Operations</span>
              <span className="categoryPill">{summary.recentHighImpactEvents.length}</span>
            </h3>
            {summary.recentHighImpactEvents.length ? (
              <div style={{ display: "grid", gap: 8 }}>
                {summary.recentHighImpactEvents.map((event) => (
                  <button
                    key={event.id}
                    type="button"
                    onClick={() => console.navigate({ view: "security", tab: "audit" })}
                    style={{
                      display: "grid",
                      textAlign: "left",
                      gap: 4,
                      padding: 10,
                      border: "1px solid var(--line)",
                      borderRadius: 4,
                      background: "var(--surface)",
                      cursor: "pointer",
                    }}
                  >
                    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                      <strong style={{ fontSize: 12 }}>{event.action}</strong>
                      <StatusBadge value={event.outcome} />
                    </div>
                    <span style={{ fontSize: 11, color: "var(--muted)" }}>
                      {event.actor.label} · {event.formattedTime}
                    </span>
                    <small style={{ fontSize: 11, color: "var(--warn)" }}>{event.impactReason}</small>
                  </button>
                ))}
              </div>
            ) : (
              <p style={{ margin: 0, fontSize: 12, color: "var(--muted)" }}>
                No high-impact destructive or rollback actions in loaded audit history.
              </p>
            )}
          </div>
        </div>
      </section>

      <section className="securitySection" aria-labelledby="sec-authorities-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Authority & Identity Context</p>
            <h2 id="sec-authorities-title">Active Security Authorities</h2>
            <p>Current authentication boundaries, scoped role safeguards, and connectivity state.</p>
          </div>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(320px, 1fr))", gap: 16, marginTop: 14 }}>
          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Authentication & RBAC Boundary</h3>
            <dl className="reviewFacts">
              <div>
                <dt>Signed in user</dt>
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
              <div>
                <dt>Cloud control plane</dt>
                <dd><StatusBadge value={cloudOk ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>Node agent state</dt>
                <dd><StatusBadge value={agentOk ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>OS keychain PAT</dt>
                <dd>Configured in OS credential store</dd>
              </div>
            </dl>
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>PostgreSQL Scoped Role Safeguards</h3>
            <p style={{ fontSize: 12, color: "var(--muted)", margin: "0 0 12px" }}>
              Workload database credentials operate with least-privilege PostgreSQL roles:
            </p>
            <div style={{ display: "grid", gap: 6 }}>
              {summary.scopedRoleSafety.map((attr) => (
                <div
                  key={attr.attribute}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "6px 8px",
                    background: "var(--surface)",
                    border: "1px solid var(--line)",
                    borderRadius: 4,
                    fontSize: 11,
                  }}
                >
                  <code style={{ fontWeight: "bold" }}>{attr.attribute}</code>
                  <span style={{ color: "var(--muted)" }}>{attr.description}</span>
                </div>
              ))}
            </div>
            <p className="capabilityNote" style={{ marginTop: 10 }}>
              Superuser and database-creation privileges are never assigned to workload service roles.
            </p>
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Source & Registry Security</h3>
            <dl className="reviewFacts">
              <div>
                <dt>GitHub App integration</dt>
                <dd>Read-only ephemeral token minting on demand</dd>
              </div>
              <div>
                <dt>GitHub PAT storage</dt>
                <dd>Zero persistent repository tokens stored</dd>
              </div>
              <div>
                <dt>Container registry auth</dt>
                <dd>Pinned node-agent execution credentials</dd>
              </div>
              <div>
                <dt>Workload isolation</dt>
                <dd>Independent Kubernetes namespace per project</dd>
              </div>
            </dl>
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Break-Glass & Safety Policy</h3>
            {summary.breakGlassFacts ? (
              <dl className="reviewFacts">
                <div>
                  <dt>Time-limited access</dt>
                  <dd>{summary.breakGlassFacts.time_limited ? "Enforced" : "Disabled"}</dd>
                </div>
                <div>
                  <dt>Approval required</dt>
                  <dd>{summary.breakGlassFacts.approval_required ? "Yes (Peer approval)" : "No"}</dd>
                </div>
                <div>
                  <dt>Reason required</dt>
                  <dd>{summary.breakGlassFacts.reason_required ? "Yes (Durable audit justification)" : "No"}</dd>
                </div>
                <div>
                  <dt>Durable audit record</dt>
                  <dd>{summary.breakGlassFacts.audited ? "Enforced (Append-only audit trail)" : "Disabled"}</dd>
                </div>
                <div>
                  <dt>Owner notification</dt>
                  <dd>{summary.breakGlassFacts.owner_notification || "Required"}</dd>
                </div>
                <div>
                  <dt>Secret reveal by default</dt>
                  <dd>{summary.breakGlassFacts.secret_reveal_by_default ? "Allowed" : "Disabled (Explicit reveal review)"}</dd>
                </div>
              </dl>
            ) : (
              <Empty text="Break-glass policy facts not reported by support telemetry." />
            )}
          </div>
        </div>
      </section>
    </div>
  );
}
