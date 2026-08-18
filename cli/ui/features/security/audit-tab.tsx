"use client";

import { useMemo, useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { AuditEvent } from "@/lib/contracts/registry";
import {
  deriveAuditRow,
  safeAuditMetadata,
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
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <p className="font-label-sm text-xs text-primary uppercase tracking-wider">Cryptographic Log</p>
          <h2 className="font-headline-md text-xl font-bold text-on-surface">Audit</h2>
        </div>
        <span className="text-xs text-on-surface-variant font-code-md">
          {rows.length} loaded event(s)
        </span>
      </div>

      {/* Filter Bar */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 shadow-sm flex flex-col md:flex-row items-center gap-4 flex-wrap">
        <div className="relative flex-1 min-w-[240px] w-full">
          <Icon name="search" className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant text-[20px] pointer-events-none" />
          <input
            aria-label="Search audit trail"
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg py-2.5 pl-10 pr-4 text-xs font-body-md text-on-surface focus:outline-none focus:border-primary/50"
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search audit trail…"
            value={query}
          />
        </div>

        <div className="flex flex-wrap items-center gap-3 w-full md:w-auto">
          <select
            aria-label="Outcome"
            className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2.5 px-3 focus:outline-none focus:border-primary/50 cursor-pointer"
            onChange={(event) => setOutcomeFilter(event.target.value)}
            value={outcomeFilter}
          >
            <option value="">All Outcomes</option>
            <option value="success">Success / Succeeded</option>
            <option value="succeeded">Succeeded</option>
            <option value="requested">Requested</option>
            <option value="denied">Denied</option>
            <option value="failed">Failed</option>
          </select>

          <select
            aria-label="Category"
            className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2.5 px-3 focus:outline-none focus:border-primary/50 cursor-pointer"
            onChange={(event) => setCategoryFilter(event.target.value)}
            value={categoryFilter}
          >
            <option value="">All Categories</option>
            <option value="access">Access & Auth</option>
            <option value="server">Server & Node</option>
            <option value="application">Application & Source</option>
            <option value="build">Build & Provenance</option>
            <option value="deployment">Deployment & Rollback</option>
            <option value="managed_resource">Managed Resource</option>
            <option value="dr_backup_restore">Backup & Restore</option>
            <option value="storage">Persistent Storage</option>
            <option value="security">Security & Secrets</option>
          </select>

          <select
            aria-label="Actor Type"
            className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2.5 px-3 focus:outline-none focus:border-primary/50 cursor-pointer"
            onChange={(event) => setActorFilter(event.target.value)}
            value={actorFilter}
          >
            <option value="">All Actors</option>
            <option value="human">Human</option>
            <option value="machine">Machine</option>
            <option value="agent">Agent</option>
          </select>

          <input
            aria-label="From date"
            className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2 px-2.5 focus:outline-none focus:border-primary/50"
            onChange={(event) => setFrom(event.target.value)}
            type="date"
            value={from}
          />
          <input
            aria-label="To date"
            className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs rounded-lg py-2 px-2.5 focus:outline-none focus:border-primary/50"
            onChange={(event) => setTo(event.target.value)}
            type="date"
            value={to}
          />
        </div>
      </div>

      {/* Audit Master-Detail Layout */}
      {rows.length ? (
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-start">
          {/* Left Column: Event List */}
          <div className="auditList lg:col-span-6 bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 flex flex-col gap-3 shadow-sm">
            <div className="flex items-center justify-between border-b border-outline-variant/20 pb-3">
              <span className="font-headline-md text-sm font-semibold text-on-surface">Audit History</span>
              <span className="text-xs text-on-surface-variant font-code-md">Bounded store</span>
            </div>

            <div className="flex flex-col gap-2 max-h-[640px] overflow-y-auto">
              {rows.map((item) => {
                const isSelected = selectedRow?.id === item.id;
                const actorTypeTag = item.actor.isHuman ? "Human actor" : "Machine actor";
                return (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => setSelectedID(item.id)}
                    className={`p-3.5 rounded-xl border text-left cursor-pointer transition-all flex flex-col gap-2 ${
                      isSelected
                        ? "bg-primary-container/80 border-primary text-on-surface shadow-sm"
                        : "bg-surface-container border-outline-variant/20 hover:bg-surface-container-high"
                    }`}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2 min-w-0">
                        <span className="font-label-sm text-[10px] text-primary uppercase bg-primary/10 px-2 py-0.5 rounded border border-primary/20 shrink-0">
                          {item.categoryLabel}
                        </span>
                        <strong className="font-body-md text-sm text-on-surface truncate">{item.action}</strong>
                      </div>
                      <span className={`status text-xs font-semibold px-2 py-0.5 rounded ${
                        item.outcome === "succeeded"
                          ? "bg-status-ready/10 text-status-ready"
                          : item.outcome === "denied" || item.outcome === "failed"
                          ? "bg-status-failed/10 text-status-failed"
                          : "bg-status-progress/10 text-status-progress"
                      }`}>
                        {item.outcome}
                      </span>
                    </div>

                    <div className="flex items-center justify-between text-[11px] font-code-md text-on-surface-variant">
                      <span>{actorTypeTag}: {item.actor.label} ({item.actor.identifier})</span>
                      <span>{item.formattedTime}</span>
                    </div>

                    {item.isHighImpact ? (
                      <div className="flex items-center gap-2">
                        <span className="font-label-sm text-[10px] text-status-warning bg-status-warning/10 px-2 py-0.5 rounded border border-status-warning/20">
                          HIGH IMPACT
                        </span>
                        {item.impactReason ? (
                          <span className="text-[11px] text-status-warning truncate">{item.impactReason}</span>
                        ) : null}
                      </div>
                    ) : null}

                    <div className="text-[11px] font-code-md text-on-surface-variant/80 truncate">
                      Target: {item.targetDisplay}
                    </div>
                  </button>
                );
              })}
            </div>
          </div>

          {/* Right Column: Event Detail Inspector */}
          <div className="auditDetail lg:col-span-6 bg-surface-container-low border border-outline-variant/20 rounded-xl p-6 shadow-sm space-y-5">
            {selectedRow ? (
              <>
                <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
                  <div>
                    <span className="font-label-sm text-xs text-primary uppercase tracking-wider block">{selectedRow.categoryLabel}</span>
                    <h3 className="font-headline-md text-lg font-bold text-on-surface mt-0.5">{selectedRow.action}</h3>
                  </div>
                  <StatusBadge value={selectedRow.outcome} />
                </div>

                <dl className="grid grid-cols-2 gap-4 text-xs">
                  <div>
                    <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Audit Event ID</dt>
                    <dd className="font-code-md text-on-surface font-semibold truncate mt-0.5">{selectedRow.id}</dd>
                  </div>
                  <div>
                    <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Timestamp</dt>
                    <dd className="font-body-md text-on-surface mt-0.5">{selectedRow.formattedTime}</dd>
                  </div>
                  <div>
                    <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Actor</dt>
                    <dd className="font-body-md text-on-surface mt-0.5">{selectedRow.actor.label} ({selectedRow.actor.identifier})</dd>
                  </div>
                  <div>
                    <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Target</dt>
                    <dd className="font-code-md text-on-surface mt-0.5">{selectedRow.targetType}/{selectedRow.targetID || "unspecified"}</dd>
                  </div>
                  {selectedRow.requestID ? (
                    <div className="col-span-2">
                      <dt className="font-label-sm text-[10px] text-on-surface-variant uppercase">Correlation ID</dt>
                      <dd className="font-code-md text-on-surface mt-0.5">{selectedRow.requestID}</dd>
                    </div>
                  ) : null}
                </dl>

                {selectedRow.isHighImpact ? (
                  <div className="bg-status-warning/10 border border-status-warning/30 p-3 rounded-xl text-status-warning text-xs flex items-center gap-2">
                    <Icon name="warning" className="text-[18px] shrink-0" />
                    <div>
                      <span className="font-bold mr-2">HIGH IMPACT</span>
                      <span>{selectedRow.impactReason}</span>
                    </div>
                  </div>
                ) : null}

                {/* Safe Metadata Entries */}
                <div className="space-y-2 pt-2 border-t border-outline-variant/10">
                  <span className="font-label-sm text-[10px] text-on-surface-variant uppercase">Safe Redacted Metadata</span>
                  {safeAuditMetadata(selectedRow.metadata).length > 0 ? (
                    <div className="bg-surface-container p-3 rounded-lg border border-outline-variant/20 space-y-1 font-code-md text-xs">
                      {safeAuditMetadata(selectedRow.metadata).map(([k, v]) => (
                        <div key={k} className="flex justify-between">
                          <span className="text-on-surface-variant">{k}:</span>
                          <span className="text-on-surface">{v}</span>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <span className="text-xs text-on-surface-variant block">No metadata reported.</span>
                  )}
                </div>

                {selectedRow.crossLink ? (
                  <div className="pt-2">
                    <Button
                      onClick={() => console.navigate(selectedRow.crossLink!.route)}
                      variant="outline"
                      className="w-full"
                    >
                      {selectedRow.crossLink.label} →
                    </Button>
                  </div>
                ) : null}
              </>
            ) : (
              <Empty text="Select an event from the audit trail to inspect details." title="No Event Selected" />
            )}
          </div>
        </div>
      ) : (console.state.audit ?? []).length === 0 ? (
        <Empty text="No audit events were returned." title="No Events" />
      ) : (
        <Empty text="No loaded audit events match these filters." title="No Events Found" />
      )}
    </div>
  );
}

export function AuditList({ rows }: { rows: AuditEvent[] }) {
  const derived = rows.map(deriveAuditRow);
  return (
    <div className="space-y-2">
      {derived.map((item) => (
        <div key={item.id} className="p-3.5 bg-surface-container rounded-xl border border-outline-variant/20 flex items-center justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <span className="font-label-sm text-[10px] text-primary uppercase bg-primary/10 px-2 py-0.5 rounded border border-primary/20 shrink-0">
              {item.categoryLabel}
            </span>
            <strong className="font-body-md text-xs text-on-surface truncate">{item.action}</strong>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-[11px] font-code-md text-on-surface-variant">{item.formattedTime}</span>
            <StatusBadge value={item.outcome} />
          </div>
        </div>
      ))}
    </div>
  );
}
