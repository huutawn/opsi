"use client";

import { useRef, useState } from "react";
import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { ServiceRecord } from "@/lib/contracts/registry";
import { formatTimestamp, serviceRows, statusLabel } from "@/lib/presentation/project";

export function ServicesView({ console }: { console: ConsoleController }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [health, setHealth] = useState("all");
  const rows = serviceRows({ services: console.state.services, telemetry: console.state.foundation.telemetry, telemetrySource: console.state.foundation.sources.telemetry, deployments: console.state.deployments, placement: console.state.foundation.placement, topology: console.state.foundation.topology }).filter((row) => {
    const matchesQuery = `${row.service.name} ${row.service.id}`.toLowerCase().includes(query.trim().toLowerCase());
    return matchesQuery && (health === "all" || row.health === health);
  });
  const selected = console.state.serviceDetail;

  return (
    <section className="page servicesPage">
      <PageHeader eyebrow="Project workloads" title="Services" description="See what is running, where it is bound, and which factual source reported its health." action={<button className="primary" onClick={() => dialog.current?.showModal()} type="button">Add service</button>} />
      <div className="serviceFilters" role="search"><label><span>Search services</span><input autoComplete="off" className="field" name="service_search" onChange={(event) => setQuery(event.target.value)} placeholder="Search by service name…" type="search" value={query} /></label><label><span>Health</span><select className="select" name="health_filter" onChange={(event) => setHealth(event.target.value)} value={health}><option value="all">All health states</option><option value="healthy">Healthy</option><option value="degraded">Degraded</option><option value="failed">Failed</option><option value="unknown">Unknown</option><option value="unavailable">Unavailable</option></select></label></div>
      {console.state.services.length === 0 ? <Empty action={<button className="primary" onClick={() => dialog.current?.showModal()} type="button">Add service</button>} title="No services yet" text="The service catalog is empty. Add a factual service identity to continue; no dependencies are assumed." /> : rows.length === 0 ? <Empty title="No matching services" text="Clear the search or health filter to see this project’s service catalog." /> : <div className="serviceList">{rows.map((row) => <button className="serviceRow" key={row.service.id} onClick={() => console.setServiceDetail(row.service)} type="button"><span className="serviceIdentity"><strong title={row.service.name}>{row.service.name}</strong><small>{row.service.type} · {row.environment || "Environment not reported"} · {row.runtime || "Runtime not reported"}</small></span><span data-label="Readiness">{row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired}` : "Not reported"}</span><span data-label="Health"><StatusBadge label={statusLabel(row.health)} value={row.health} /></span><span data-label="Release"><code>{row.release ? short(row.release) : "Not reported"}</code></span><span aria-hidden="true" className="rowArrow">→</span></button>)}</div>}
      {selected ? <ServiceDetail console={console} service={selected} /> : null}
      <dialog aria-labelledby="addServiceTitle" className="nativeDialog" ref={dialog}><form method="dialog"><button aria-label="Close add service dialog" className="iconButton dialogClose" type="submit"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></form><p className="eyebrow">Project catalog</p><h2 id="addServiceTitle">Add service</h2><p>Review creates service identity only. Immutable BuildRecords remain the runtime release source.</p><form className="form" onSubmit={(event) => { dialog.current?.close(); void console.actions.createService(event); }}><label>Name<input autoComplete="off" className="field" name="name" placeholder="api…" required /></label><label>Type<select className="select" name="type" defaultValue="application"><option value="application">Application</option><option value="managed">Managed dependency</option><option value="external">External dependency</option></select></label><label>Container port<input className="field" min="1" name="container_port" type="number" /></label><label>Health path<input autoComplete="off" className="field" name="health_path" placeholder="/health…" /></label><label>Replicas<input className="field" defaultValue="1" min="1" name="replicas" type="number" /></label><div className="modalActions span2"><button onClick={() => dialog.current?.close()} type="button">Cancel</button><button className="primary" disabled={console.state.busy === "service"} type="submit">Review service</button></div></form></dialog>
    </section>
  );
}

function ServiceDetail({ console, service }: { console: ConsoleController; service: ServiceRecord }) {
  const row = serviceRows({ services: [service], telemetry: console.state.foundation.telemetry, telemetrySource: console.state.foundation.sources.telemetry, deployments: console.state.deployments, placement: console.state.foundation.placement, topology: console.state.foundation.topology })[0];
  const recent = console.state.deployments.filter((deployment) => deployment.service_id === service.id).slice(0, 4);
  return <aside aria-labelledby="serviceDetailTitle" className="detailDrawer"><div className="detailHeader"><div><p className="eyebrow">Service detail</p><h2 id="serviceDetailTitle">{service.name}</h2></div><button aria-label="Close service detail" className="iconButton" onClick={() => console.setServiceDetail(null)} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></div><dl className="detailFacts"><div><dt>Health</dt><dd><StatusBadge label={statusLabel(row.health)} value={row.health} /></dd></div><div><dt>Readiness</dt><dd>{row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired} ready` : "Not reported"}</dd></div><div><dt>Environment</dt><dd>{row.environment || "Not reported"}</dd></div><div><dt>Runtime</dt><dd>{row.runtime || "Not reported"}</dd></div><div><dt>Canonical ID</dt><dd><code>{service.id}</code></dd></div><div><dt>Current release</dt><dd><code className="longValue">{row.release || "Not reported"}</code></dd></div><div><dt>Repository</dt><dd>{service.repo_url || "Not reported"}</dd></div><div><dt>Resource shape</dt><dd>{service.replicas !== undefined ? `${service.replicas} replica${service.replicas === 1 ? "" : "s"}` : "Not reported"}{service.container_port ? ` · port ${service.container_port}` : ""}</dd></div></dl><section><h3>Recent deployments</h3>{recent.length ? <div className="deploymentList">{recent.map((deployment) => <div className="deploymentRow" key={deployment.id}><span><strong>{deployment.id}</strong><small>{formatTimestamp(deployment.created_at)}</small></span><StatusBadge value={deployment.rollout_state ?? deployment.status} /></div>)}</div> : <p className="muted">No deployment history reported.</p>}</section><section><h3>Dependencies</h3><p className="muted">Dependency metadata is not reported for this service.</p></section></aside>;
}

function short(value: string) {
  if (value.startsWith("sha256:") && value.length > 20) return `sha256:${value.slice(7, 15)}…`;
  return value.length > 22 ? `${value.slice(0, 22)}…` : value;
}
