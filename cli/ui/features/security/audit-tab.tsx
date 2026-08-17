"use client";

import { useMemo, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { AuditEvent } from "@/lib/contracts/registry";
import {
  deriveAuditRow,
  safeAuditMetadata,
  type AuditRow,
} from "@/lib/presentation/security/model";

export function AuditTab({ console }: { console: ConsoleController }) {
  const [query, setQuery] = useState("");
  const [outcomeFilter, setOutcomeFilter] = useState("");
  const [categoryFilter, setCategoryFilter] = useState("");
  const [actorFilter, setActorFilter] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [selectedID, setSelectedID] = useState("");

  const rows = useMemo(() => {
    const rawEvents = console.state.audit ?? [];
    const derived = rawEvents.map(deriveAuditRow);
    const needle = query.trim().toLowerCase();

    return derived.filter((item) => {
      const haystack = [
        item.id,
        item.action,
        item.actionLabel,
        item.category,
        item.categoryLabel,
        item.actor.label,
        item.actor.identifier,
        item.targetType,
        item.targetID,
        item.targetDisplay,
        item.requestID,
        item.outcome,
      ]
        .filter(Boolean)
        .join(" ")
        .toLowerCase();

      const created = Date.parse(item.timestamp);

      const matchesQuery = !needle || haystack.includes(needle);
      const matchesOutcome = !outcomeFilter || item.outcome === outcomeFilter;
      const matchesCategory = !categoryFilter || item.category === categoryFilter;
      const matchesActor =
        !actorFilter ||
        (actorFilter === "human" && item.actor.isHuman) ||
        (actorFilter === "machine" && item.actor.isMachine) ||
        item.actor.kind === actorFilter;
      const matchesFrom = !from || created >= Date.parse(`${from}T00:00:00`);
      const matchesTo = !to || created < Date.parse(`${to}T00:00:00`) + 86_400_000;

      return matchesQuery && matchesOutcome && matchesCategory && matchesActor && matchesFrom && matchesTo;
    });
  }, [console.state.audit, query, outcomeFilter, categoryFilter, actorFilter, from, to]);

  const selectedRow = rows.find((item) => item.id === selectedID) ?? rows[0];

  return (
    <div className="securityStack">
      <section className="securitySection" aria-labelledby="audit-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Loaded history</p>
            <h2 id="audit-title">Audit</h2>
            <p>Chronological audit log across control-plane, infrastructure, and delivery authorities.</p>
          </div>
          <StatusBadge value={console.state.status === "ready" ? "ready" : "unavailable"} />
        </div>

        <div className="filterBar" style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))", gap: 10 }}>
          <label>
            Search audit trail
            <input
              className="field"
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search actor, action, resource, ID…"
              value={query}
            />
          </label>
          <label>
            Outcome
            <select
              className="select"
              onChange={(event) => setOutcomeFilter(event.target.value)}
              value={outcomeFilter}
            >
              <option value="">All outcomes</option>
              <option value="succeeded">Succeeded</option>
              <option value="requested">Requested</option>
              <option value="denied">Denied / Rejected</option>
              <option value="failed">Failed</option>
            </select>
          </label>
          <label>
            Category
            <select
              className="select"
              onChange={(event) => setCategoryFilter(event.target.value)}
              value={categoryFilter}
            >
              <option value="">All categories</option>
              <option value="access">Access & Auth</option>
              <option value="server">Server & Node</option>
              <option value="application">Application & Source</option>
              <option value="build">Build & Provenance</option>
              <option value="deployment">Deployment & Rollback</option>
              <option value="managed_resource">Managed Resource</option>
              <option value="dr_backup_restore">Backup & Restore</option>
              <option value="cutover">Cutover & Migration</option>
              <option value="storage">Persistent Storage</option>
              <option value="security">Security & Secrets</option>
            </select>
          </label>
          <label>
            Actor type
            <select
              className="select"
              onChange={(event) => setActorFilter(event.target.value)}
              value={actorFilter}
            >
              <option value="">All actors</option>
              <option value="human">Human actor</option>
              <option value="machine">Machine actor</option>
              <option value="agent">Agent</option>
              <option value="worker">Worker automation</option>
              <option value="github_actions">GitHub Actions</option>
            </select>
          </label>
          <label>
            From date
            <input
              className="field"
              onChange={(event) => setFrom(event.target.value)}
              type="date"
              value={from}
            />
          </label>
          <label>
            To date
            <input
              className="field"
              onChange={(event) => setTo(event.target.value)}
              type="date"
              value={to}
            />
          </label>
        </div>

        {console.state.status !== "ready" && !console.state.audit.length ? (
          <p className="truthCallout">Audit history unavailable; this is not an empty result.</p>
        ) : rows.length ? (
          <div className="auditExplorer">
            <div className="auditList">
              <p className="muted" style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                <span>{rows.length} loaded event(s)</span>
                <small style={{ color: "var(--muted)" }}>Bounded local history</small>
              </p>
              {rows.map((item) => (
                <button
                  key={item.id}
                  aria-pressed={selectedRow?.id === item.id}
                  className={`auditRow audit-${item.outcome}`}
                  onClick={() => setSelectedID(item.id)}
                  type="button"
                >
                  <time title={item.timestamp}>{item.formattedTime}</time>
                  <span>
                    <b>{item.actor.label}</b>
                    {item.actor.identifier && item.actor.identifier !== "authenticated user" ? (
                      <small style={{ display: "block", color: "var(--muted)" }}>{item.actor.identifier}</small>
                    ) : null}
                  </span>
                  <div>
                    <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
                      <span className="categoryPill">{item.categoryLabel}</span>
                      {item.isHighImpact ? (
                        <span style={{ fontSize: 9, fontWeight: 700, padding: "2px 4px", borderRadius: 3, background: "var(--warn)", color: "#000" }}>
                          HIGH IMPACT
                        </span>
                      ) : null}
                    </div>
                    <b style={{ display: "block", marginTop: 2 }}>{item.action}</b>
                  </div>
                  <span title={item.targetDisplay}>{item.targetDisplay}</span>
                  <StatusBadge value={item.outcome} />
                </button>
              ))}
            </div>

            <AuditDetail
              console={console}
              item={selectedRow}
            />
          </div>
        ) : (
          <Empty
            text={
              console.state.audit.length
                ? "No loaded audit events match these filters."
                : "No audit events were returned."
            }
          />
        )}
      </section>
    </div>
  );
}

export function AuditList({ rows }: { rows: AuditEvent[] }) {
  const derived = rows.map(deriveAuditRow);
  return (
    <div className="auditList">
      {derived.map((item) => (
        <div className="auditRow" key={item.id}>
          <time title={item.timestamp}>{item.formattedTime}</time>
          <span>
            <b>{item.actor.label}</b>
            {item.actor.identifier ? <small style={{ display: "block", color: "var(--muted)" }}>{item.actor.identifier}</small> : null}
          </span>
          <div>
            <span className="categoryPill">{item.categoryLabel}</span>
            <b>{item.action}</b>
          </div>
          <span>{item.targetDisplay}</span>
          <StatusBadge value={item.outcome} />
        </div>
      ))}
    </div>
  );
}

function AuditDetail({ console, item }: { console: ConsoleController; item?: AuditRow }) {
  if (!item) {
    return <Empty title="Select an audit event" text="Choose a loaded event to inspect bounded evidence." />;
  }

  const rawMetadata = boundedMetadata(item.metadata);

  return (
    <aside className="auditDetail" aria-label="Audit event detail">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 12, marginBottom: 12 }}>
        <div>
          <p className="eyebrow">{item.categoryLabel}</p>
          <h3 style={{ margin: "2px 0 0" }}>{item.action}</h3>
        </div>
        <div style={{ display: "flex", gap: 6, alignItems: "center" }}>
          {item.isHighImpact ? (
            <span style={{ fontSize: 9, fontWeight: 700, padding: "3px 6px", borderRadius: 3, background: "var(--warn)", color: "#000" }}>
              HIGH IMPACT
            </span>
          ) : null}
          <StatusBadge value={item.outcome} />
        </div>
      </div>

      {item.isHighImpact && item.impactReason ? (
        <div style={{ padding: "8px 10px", margin: "0 0 14px", border: "1px solid var(--warn)", background: "var(--surface)", borderRadius: 4, fontSize: 11, color: "var(--ink)" }}>
          <strong style={{ color: "var(--warn)" }}>Attention: </strong>
          {item.impactReason}
        </div>
      ) : null}

      <dl className="reviewFacts">
        <div>
          <dt>Audit ID</dt>
          <dd><code>{item.id}</code></dd>
        </div>
        <div>
          <dt>Actor type</dt>
          <dd><b>{item.actor.label}</b></dd>
        </div>
        <div>
          <dt>Authenticated actor</dt>
          <dd>{item.actor.identifier || "Not reported"}</dd>
        </div>
        <div>
          <dt>Resource</dt>
          <dd><code>{item.targetType}/{item.targetID || "unspecified"}</code></dd>
        </div>
        <div>
          <dt>Request/correlation ID</dt>
          <dd>{item.requestID ? <code>{item.requestID}</code> : "Not reported"}</dd>
        </div>
        <div>
          <dt>Timestamp</dt>
          <dd>{item.formattedTime} <small style={{ color: "var(--muted)", display: "block" }}>{item.timestamp}</small></dd>
        </div>
        <div>
          <dt>Result</dt>
          <dd><StatusBadge value={item.outcome} /></dd>
        </div>
      </dl>

      {item.crossLink ? (
        <div style={{ margin: "16px 0", padding: "12px", border: "1px solid var(--line)", background: "var(--surface)", borderRadius: 6 }}>
          <p style={{ margin: "0 0 8px", fontSize: 11, color: "var(--muted)", fontWeight: "bold" }}>
            RELATED SURFACE
          </p>
          <button
            type="button"
            className="secondary"
            onClick={() => console.navigate(item.crossLink!.route)}
            style={{ width: "100%", justifyContent: "center" }}
          >
            {item.crossLink.label} &rarr;
          </button>
        </div>
      ) : null}

      <h4 style={{ marginTop: 16, marginBottom: 8, fontSize: 13 }}>Redacted metadata</h4>
      {item.safeMetadataEntries.length ? (
        <dl className="metadataList">
          {item.safeMetadataEntries.map(([key, value]) => (
            <div key={key}>
              <dt><code>{key}</code></dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      ) : rawMetadata.length ? (
        <dl className="metadataList">
          {rawMetadata.map(([key, value]) => (
            <div key={key}>
              <dt><code>{key}</code></dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className="muted" style={{ fontSize: 12 }}>No redacted metadata reported.</p>
      )}
    </aside>
  );
}

function boundedMetadata(value?: Record<string, unknown>): Array<[string, string]> {
  return safeAuditMetadata(value);
}
