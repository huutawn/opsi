"use client";

import { useEffect, useRef, useState } from "react";
import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { ServiceRecord } from "@/lib/contracts/registry";
import { formatTimestamp, serviceRows, statusLabel } from "@/lib/presentation/project";

export function ServicesView({ console }: { console: ConsoleController }) {
  const addTrigger = useRef<HTMLButtonElement>(null);
  const [adding, setAdding] = useState(false);
  const [query, setQuery] = useState("");
  const [health, setHealth] = useState("all");
  const rows = serviceRows({ services: console.state.services, telemetry: console.state.foundation.telemetry, telemetrySource: console.state.foundation.sources.telemetry, deployments: console.state.deployments, placement: console.state.foundation.placement, topology: console.state.foundation.topology }).filter((row) => {
    const matchesQuery = `${row.service.name} ${row.service.id}`.toLowerCase().includes(query.trim().toLowerCase());
    return matchesQuery && (health === "all" || row.health === health);
  });
  const selected = console.state.serviceDetail;

  return (
    <section className="page servicesPage">
      <PageHeader eyebrow="Project workloads" title="Services" description="See what is running, where it is bound, and which factual source reported its health." action={<button className="primary" onClick={(event) => { addTrigger.current = event.currentTarget; setAdding(true); }} type="button">Add service</button>} />
      <div className="serviceFilters" role="search"><label><span>Search services</span><input autoComplete="off" className="field" name="service_search" onChange={(event) => setQuery(event.target.value)} placeholder="Search by service name…" type="search" value={query} /></label><label><span>Health</span><select className="select" name="health_filter" onChange={(event) => setHealth(event.target.value)} value={health}><option value="all">All health states</option><option value="healthy">Healthy</option><option value="degraded">Degraded</option><option value="failed">Failed</option><option value="unknown">Unknown</option><option value="unavailable">Unavailable</option></select></label></div>
      {console.state.services.length === 0 ? <Empty action={<button className="primary" onClick={(event) => { addTrigger.current = event.currentTarget; setAdding(true); }} type="button">Add service</button>} title="No services yet" text="The service catalog is empty. Add a factual service identity to continue; no dependencies are assumed." /> : rows.length === 0 ? <Empty title="No matching services" text="Clear the search or health filter to see this project’s service catalog." /> : <div className="serviceList">{rows.map((row) => <button className="serviceRow" data-service-id={row.service.id} key={row.service.id} onClick={() => console.setServiceDetail(row.service)} type="button"><span className="serviceIdentity"><strong title={row.service.name}>{row.service.name}</strong><small>{row.service.type} · {row.environment || "Environment not reported"} · {row.runtime || "Runtime not reported"}</small></span><span data-label="Readiness">{row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired}` : "Not reported"}</span><span data-label="Health"><StatusBadge label={statusLabel(row.health)} value={row.health} /></span><span data-label="Release"><code>{row.release ? short(row.release) : "Not reported"}</code></span><span aria-hidden="true" className="rowArrow">→</span></button>)}</div>}
      {selected ? <ServiceDetail console={console} service={selected} /> : null}
      {adding ? <AddServiceDialog console={console} onClose={() => { setAdding(false); window.requestAnimationFrame(() => addTrigger.current?.focus()); }} /> : null}
    </section>
  );
}

export function AddServiceDialog({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated?: () => void | Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);
  return <dialog aria-labelledby="addServiceTitle" className="nativeDialog" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}><button aria-label="Close add service dialog" autoFocus className="iconButton dialogClose" onClick={onClose} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button><p className="eyebrow">Project catalog</p><h2 id="addServiceTitle">Add service</h2><p>Review creates service identity only. Immutable BuildRecords remain the runtime release source.</p><form className="form" onSubmit={(event) => { void console.actions.createService(event, onCreated); onClose(); }}><label>Name<input autoComplete="off" className="field" name="name" placeholder="api…" required /></label><label>Type<select className="select" name="type" defaultValue="application"><option value="application">Application</option><option value="managed">Managed dependency</option><option value="external">External dependency</option></select></label><label>Container port<input className="field" min="1" name="container_port" type="number" /></label><label>Health path<input autoComplete="off" className="field" name="health_path" placeholder="/health…" /></label><label>Replicas<input className="field" defaultValue="1" min="1" name="replicas" type="number" /></label><div className="modalActions span2"><button onClick={onClose} type="button">Cancel</button><button className="primary" disabled={console.state.busy === "service"} type="submit">Review service</button></div></form></dialog>;
}

function ServiceDetail({ console, service }: { console: ConsoleController; service: ServiceRecord }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const row = serviceRows({ services: [service], telemetry: console.state.foundation.telemetry, telemetrySource: console.state.foundation.sources.telemetry, deployments: console.state.deployments, placement: console.state.foundation.placement, topology: console.state.foundation.topology })[0];
  const recent = console.state.deployments.filter((deployment) => deployment.service_id === service.id).slice(0, 4);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);
  function close() {
    dialog.current?.close();
    console.setServiceDetail(null);
    window.requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-service-id="${CSS.escape(service.id)}"]`)?.focus());
  }
  return <dialog aria-describedby="serviceDetailDescription" aria-labelledby="serviceDetailTitle" className="detailDrawer" onCancel={(event) => { event.preventDefault(); close(); }} ref={dialog}><div className="detailHeader"><div><p className="eyebrow">Service detail</p><h2 id="serviceDetailTitle">{service.name}</h2><p id="serviceDetailDescription">Factual service, runtime, delivery, dependency, and configuration evidence.</p></div><button aria-label="Close service detail" autoFocus className="iconButton" onClick={close} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></div>
    <section><h3>Summary</h3><dl className="detailFacts"><div><dt>Health</dt><dd><StatusBadge label={statusLabel(row.health)} value={row.health} /></dd></div><div><dt>Readiness</dt><dd>{row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired} ready` : "Not reported"}</dd></div><div><dt>Canonical ID</dt><dd><code>{service.id}</code></dd></div><div><dt>Type</dt><dd>{service.type || "Not reported"}</dd></div></dl></section>
    <section><h3>Runtime</h3><dl className="detailFacts"><div><dt>Environment</dt><dd>{row.environment || "Not reported"}</dd></div><div><dt>Runtime</dt><dd>{row.runtime || "Not reported"}</dd></div><div><dt>Current release</dt><dd><code className="longValue">{row.release || "Not reported"}</code></dd></div></dl></section>
    <section><h3>Delivery</h3>{recent.length ? <div className="deploymentList">{recent.map((deployment) => <div className="deploymentRow" key={deployment.id}><span><strong>{deployment.id}</strong><small>{formatTimestamp(deployment.created_at)}</small></span><StatusBadge value={deployment.rollout_state ?? deployment.status} /></div>)}</div> : <p className="muted">Not reported by Local API.</p>}</section>
    <section><h3>Dependencies</h3><p className="muted">Not reported by Local API.</p></section>
    <section><h3>Configuration</h3><dl className="detailFacts"><div><dt>Repository</dt><dd>{service.repo_url || "Not reported"}</dd></div><div><dt>Source type</dt><dd>{service.source_type || "Not reported"}</dd></div><div><dt>Replicas</dt><dd>{service.replicas ?? "Not reported"}</dd></div><div><dt>Container port</dt><dd>{service.container_port ?? "Not reported"}</dd></div><div><dt>Health path</dt><dd>{service.health_path || "Not reported"}</dd></div><div><dt>Namespace</dt><dd>{service.namespace || "Not reported"}</dd></div></dl></section>
  </dialog>;
}

function short(value: string) {
  if (value.startsWith("sha256:") && value.length > 20) return `sha256:${value.slice(7, 15)}…`;
  return value.length > 22 ? `${value.slice(0, 22)}…` : value;
}
