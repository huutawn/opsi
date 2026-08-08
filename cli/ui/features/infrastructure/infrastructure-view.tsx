"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import { ApplicationWizard } from "@/features/applications/application-wizard";
import type { ConsoleController } from "@/features/console/types";
import { PlacementDialog } from "@/features/infrastructure/placement-dialog";
import { useInfrastructureData } from "@/features/infrastructure/data";
import { TopologyDesignCanvas } from "@/features/infrastructure/topology-canvas";
import { DeploymentReview } from "@/features/infrastructure/deployment-review";
import { deploymentStage } from "@/features/infrastructure/deployment-review-model";
import { LocalClient } from "@/lib/api/local-client";
import type { TimelineEvent } from "@/lib/contracts/registry";
import { bootstrapProgress, capacityLabel, serverLifecycle, topologyOnboarding, type CanvasDraft, type ServerLifecycle, type TopologyOnboardingState } from "@/lib/presentation/infrastructure/model";

export function InfrastructureView({ console }: { console: ConsoleController }) {
  const { data, source, error, load } = useInfrastructureData(console);
  const mode = console.route.topologyMode === "live" ? "live" : "design";
  const [placementOpen, setPlacementOpen] = useState(false);
  const [bootstrapOpen, setBootstrapOpen] = useState(false);
  const [serviceOpen, setServiceOpen] = useState(false);
  const placementTrigger = useRef<HTMLButtonElement>(null);
  const bootstrapTrigger = useRef<HTMLButtonElement>(null);
  const serviceTrigger = useRef<HTMLButtonElement>(null);
  const projectID = console.state.project?.id ?? "";
  useEffect(() => {
    // Project-scoped dialogs never survive a context change.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setPlacementOpen(false);
    setBootstrapOpen(false);
    setServiceOpen(false);
  }, [projectID]);
  if (!console.state.project) return <Empty text="Select a project first." />;
  if (source === "loading" && !data.facts) return <Empty title="Loading infrastructure…" text="Reading factual topology and runtime inventory from Local API." />;
  if (!data.facts) return <Empty title="Infrastructure unavailable" text={error || "Local API did not return topology facts."} action={<button onClick={() => void load()} type="button">Retry</button>} />;

  return <div className="infrastructurePage">
    {console.route.tab !== "topology" ? <div className="destinationToolbar"><p>{error || "Cloud topology facts remain visible when Agent runtime data is unavailable."}</p><button data-review-trigger={console.route.tab === "bootstrap" ? "bootstrap" : undefined} onClick={(event) => { if (console.route.tab === "bootstrap") { bootstrapTrigger.current = event.currentTarget; setBootstrapOpen(true); } else { placementTrigger.current = event.currentTarget; setPlacementOpen(true); } }} type="button">{console.route.tab === "bootstrap" ? "Add server" : "Plan placement"}</button></div> : null}
    {console.route.tab === "topology" ? <TopologyTab bindings={data.bindings} builds={data.builds} console={console} error={error} facts={data.facts} key={projectID} mode={mode} onAddService={(trigger) => { serviceTrigger.current = trigger; setServiceOpen(true); }} onConnectServer={(trigger) => { bootstrapTrigger.current = trigger; setBootstrapOpen(true); }} onMode={(next) => console.navigate({ topologyMode: next })} onPlanPlacement={(trigger) => { placementTrigger.current = trigger; setPlacementOpen(true); }} onReload={load} policies={data.policies} repositories={data.repositories} topology={data.topology} /> : null}
    {console.route.tab === "runtimes" ? <RuntimesTab console={console} facts={data.facts} topology={data.topology} /> : null}
    {console.route.tab === "nodes" ? <NodesTab console={console} facts={data.facts} /> : null}
    {console.route.tab === "bootstrap" ? <BootstrapTab console={console} onReload={load} /> : null}
    {placementOpen ? <PlacementDialog console={console} data={{ facts: data.facts, topology: data.topology, repositories: data.repositories, bindings: data.bindings, builds: data.builds, policies: data.policies }} onApplied={() => { void console.actions.load(); void load(); }} onClose={() => { setPlacementOpen(false); window.requestAnimationFrame(() => placementTrigger.current?.focus()); }} /> : null}
    {bootstrapOpen ? <BootstrapDialog console={console} onClose={() => { setBootstrapOpen(false); window.requestAnimationFrame(() => bootstrapTrigger.current?.focus()); }} onCreated={load} /> : null}
    {serviceOpen ? <ApplicationWizard console={console} onClose={() => { setServiceOpen(false); window.requestAnimationFrame(() => serviceTrigger.current?.focus()); }} onCreated={async () => { console.navigate({ topologyMode: "design" }); await load(); }} /> : null}
  </div>;
}

function TopologyTab({ bindings, builds, console, error, facts, mode, onAddService, onConnectServer, onMode, onPlanPlacement, onReload, policies, repositories, topology }: { bindings: ReturnType<typeof useInfrastructureData>["data"]["bindings"]; builds: ReturnType<typeof useInfrastructureData>["data"]["builds"]; console: ConsoleController; error: string; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; mode: "design" | "live"; onAddService: (trigger: HTMLButtonElement) => void; onConnectServer: (trigger: HTMLButtonElement) => void; onMode: (mode: "design" | "live") => void; onPlanPlacement: (trigger: HTMLButtonElement) => void; onReload: () => Promise<void>; policies: ReturnType<typeof useInfrastructureData>["data"]["policies"]; repositories: ReturnType<typeof useInfrastructureData>["data"]["repositories"]; topology: ReturnType<typeof useInfrastructureData>["data"]["topology"] }) {
  const [draft, setDraft] = useState<CanvasDraft>({});
  const lifecycle = serverLifecycle(facts, console.state.sessions);
  const onboarding = topologyOnboarding(facts, topology, console.state.sessions);
  function act(event: React.MouseEvent<HTMLButtonElement>) {
    if (onboarding.kind === "connect") onConnectServer(event.currentTarget);
    else if (onboarding.kind === "bootstrap") { if (onboarding.sessionID) void console.actions.loadBootstrapEvents(onboarding.sessionID); window.requestAnimationFrame(() => document.getElementById("server-lifecycle-heading")?.focus()); }
    else if (onboarding.kind === "retry" && onboarding.sessionID) console.actions.retryBootstrap(onboarding.sessionID, onReload);
    else if (onboarding.kind === "application") onAddService(event.currentTarget);
    else if (onboarding.kind === "placement") onPlanPlacement(event.currentTarget);
    else { onMode("design"); window.requestAnimationFrame(() => document.getElementById("topology-heading")?.focus()); }
  }
  return <section aria-labelledby="topology-heading">
    <div className="sectionHeading topologyHeading"><div><h2 id="topology-heading" tabIndex={-1}>Topology</h2><p>{mode === "design" ? "TopologyPlan assignments and services still waiting for placement." : "Runtime, node, Agent, and deployment facts reported by existing sources."}</p></div><div className="topologyControls"><span className="sourceTag">{mode === "design" ? topology ? `TopologyPlan r${topology.revision}` : "No TopologyPlan" : "Live facts"}</span><div aria-label="Topology view" className="topologyMode" role="group"><button aria-pressed={mode === "design"} onClick={() => onMode("design")} type="button">Design</button><button aria-pressed={mode === "live"} onClick={() => onMode("live")} type="button">Live</button></div></div></div>
    <TopologyOnboarding action={act} state={onboarding} />
    <ServerLifecycleCard console={console} lifecycle={lifecycle} />
    {error ? <p className="truthCallout" role="alert">{error}</p> : null}
    {topology ? <div hidden={mode !== "design"}><DeploymentReview builds={builds} console={console} facts={facts} onLive={() => onMode("live")} policies={policies} topology={topology} /></div> : null}
    {mode === "design" ? <>
      {!topology ? <div className="truthCallout"><b>No topology plan</b><p>Infrastructure facts are shown without service placement edges. Service inventory is not used to fabricate assignments.</p></div> : null}
      <TopologyDesignCanvas bindings={bindings} builds={builds} console={console} draft={draft} facts={facts} onDraft={setDraft} onReload={onReload} repositories={repositories} topology={topology} />
    </> : <LiveTopology console={console} facts={facts} />}
  </section>;
}

function TopologyOnboarding({ action, state }: { action: (event: React.MouseEvent<HTMLButtonElement>) => void; state: TopologyOnboardingState }) {
  return <section className="topologyOnboarding" data-state={state.kind} aria-labelledby="topology-next-step"><div><p className="eyebrow">Next step</p><h3 id="topology-next-step">{state.title}</h3><p>{state.description}</p>{state.progress ? <div className="bootstrapProgress" role="status"><span>{state.progress.percent === null ? state.progress.label : `${state.progress.percent}% · ${state.progress.label}`}</span>{state.progress.percent !== null ? <progress max="100" value={state.progress.percent} /> : null}</div> : null}</div><button className="primary" onClick={action} type="button">{state.action}</button></section>;
}

function ServerLifecycleCard({ console, lifecycle }: { console: ConsoleController; lifecycle: ServerLifecycle }) {
  const session = lifecycle.session;
  const nodeRecord = console.state.nodes.find((node) => node.id === lifecycle.node?.id);
  const publicHost = session?.public_host || nodeRecord?.public_host;
  const events = console.state.bootstrapEventsSessionID === session?.id ? [...console.state.bootstrapEvents].sort((a, b) => b.created_at.localeCompare(a.created_at)) : [];
  const recent = events.slice(0, 5);
  const progress = session ? bootstrapProgress(session.checkpoint, events.length) : null;
  const facts: Array<[string, string] | null> = [
    publicHost ? ["Public host", publicHost] : null,
    lifecycle.runtime ? ["Runtime", `${lifecycle.runtime.name} · ${lifecycle.runtime.type} · ${lifecycle.runtime.status}`] : null,
    lifecycle.node ? ["Node", `${lifecycle.node.id} · ${lifecycle.node.status}`] : null,
    lifecycle.agent ? ["Agent", `${lifecycle.agent.id} · ${lifecycle.agent.status}${nodeRecord?.agent_version ? ` · ${nodeRecord.agent_version}` : ""}`] : null,
    session ? ["Bootstrap status", `${session.status} · ${session.created_at}`] : null,
    progress ? ["Bootstrap progress", progress.percent === null ? progress.label : `${progress.percent}% · ${progress.label}`] : null,
    session?.last_failure_code ? ["Failure code", session.last_failure_code] : null,
    session?.last_failure_message_redacted ? ["Failure", session.last_failure_message_redacted] : null,
  ];
  const reportedFacts = facts.filter((fact): fact is [string, string] => fact !== null);
  const detailFacts: Array<[string, string] | null> = session ? [
    ["Session", session.id],
    ["Role", session.role],
    session.attempt_count !== undefined ? ["Attempt", session.max_attempts === undefined ? String(session.attempt_count) : `${session.attempt_count}/${session.max_attempts}`] : null,
    session.checkpoint ? ["Next step", String(session.checkpoint.next_step_index)] : null,
  ] : [];
  const reportedDetails = detailFacts.filter((fact): fact is [string, string] => fact !== null);
  return <section className="serverLifecycle" aria-labelledby="server-lifecycle-heading" data-state={lifecycle.status.toLowerCase()}>
    <div className="detailHeading"><div><p className="eyebrow">Server lifecycle</p><h3 id="server-lifecycle-heading" tabIndex={-1}>{publicHost || lifecycle.runtime?.name || session?.id || "Server facts"}</h3></div><StatusBadge label={lifecycle.status} value={lifecycle.status === "Connecting" ? "bootstrapping" : lifecycle.status} /></div>
    {reportedFacts.length ? <dl className="evidenceGrid">{reportedFacts.map(([label, value]) => <Fact key={label} label={label} value={value} />)}</dl> : <p className="muted">No server identity or bootstrap facts have been reported.</p>}
    {recent.length ? <><div className="sectionHeading lifecycleEventsHeading"><div><h4>Recent bootstrap events</h4><p>Latest five factual events for this session.</p></div><span>{events.length} total</span></div><EventTimeline events={recent} /></> : null}
    {session ? <details className="lifecycleDetails"><summary>Open full bootstrap details</summary><dl className="evidenceGrid">{reportedDetails.map(([label, value]) => <Fact key={label} label={label} value={value} />)}</dl>{events.length > 5 ? <EventTimeline events={events} /> : null}</details> : null}
  </section>;
}

function EventTimeline({ events }: { events: TimelineEvent[] }) {
  return <ol className="eventTimeline">{events.map((event) => <li key={event.id}><span aria-hidden="true" /><div><b>{event.step}</b><p>{event.message_redacted}</p><small>{event.created_at}</small></div></li>)}</ol>;
}

function LiveTopology({ console, facts }: { console: ConsoleController; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]> }) {
  const connections = console.state.services.flatMap((service) => (service.configuration?.bindings ?? []).map((binding) => ({ source: service.name, target: binding.target_service_key, kind: binding.kind, route: binding.path || binding.env_prefix || "" })));
  return <div className="liveTopology"><section><div className="sectionHeading"><div><h3>Runtime and Agent facts</h3><p>Only identities returned by Local API are shown.</p></div><span>{facts.runtimes.length} runtimes</span></div>{facts.runtimes.length ? <div className="liveRuntimeList">{facts.runtimes.map((runtime) => <article key={runtime.id}><div><strong>{runtime.name}</strong><StatusBadge value={runtime.status} /></div><p>{facts.environments.find((environment) => environment.id === runtime.environment_id)?.name || runtime.environment_id} · <code>{runtime.id}</code></p><dl><Fact label="Nodes" value={facts.nodes.filter((node) => node.runtime_id === runtime.id).map((node) => `${node.id} (${node.status})`).join(", ") || "None reported"} /><Fact label="Agents" value={facts.agents.filter((agent) => agent.runtime_id === runtime.id).map((agent) => `${agent.id} (${agent.status})`).join(", ") || "None reported"} /></dl></article>)}</div> : <p className="muted">No runtime facts reported.</p>}</section><section><div className="sectionHeading"><div><h3>Applied connections</h3><p>Factual service configuration only; this is not deployment state.</p></div><span>{connections.length} connections</span></div>{connections.length ? <ul className="compactList">{connections.map((connection, index) => <li key={`${connection.source}:${connection.target}:${index}`}><strong>{connection.source} → {connection.target}</strong><small>{connection.kind}{connection.route ? ` · ${connection.route}` : ""}</small></li>)}</ul> : <p className="muted">No applied connections reported.</p>}</section><LiveDeploymentBoard console={console} /></div>;
}

function LiveDeploymentBoard({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const reload = useRef(console.actions.load);
  const projectID = console.state.project?.id ?? "";
  const [events, setEvents] = useState<Record<string, TimelineEvent[]>>({});
  useEffect(() => { reload.current = console.actions.load; }, [console.actions.load]);
  useEffect(() => {
    if (!projectID) return;
    let disposed = false;
    let timer = 0;
    async function poll() {
      if (disposed || document.hidden) { timer = window.setTimeout(poll, 4000); return; }
      try {
        const response = await client.deployments(projectID);
        const next: Record<string, TimelineEvent[]> = {};
        await Promise.all((response.deployments ?? []).map(async (job) => { const result = await client.deploymentEvents(projectID, job.id); next[job.id] = result.events ?? []; }));
        if (!disposed) { setEvents(next); await reload.current(); }
      } catch { /* retain last Cloud state */ }
      if (!disposed) timer = window.setTimeout(poll, 4000);
    }
    void poll();
    return () => { disposed = true; window.clearTimeout(timer); };
  }, [client, projectID]);
  const deployments = console.state.deployments;
  return <section><div className="sectionHeading"><div><h3>Deployment live state</h3><p>Durable DeploymentJobs and Cloud events are polled; refresh restores this list.</p></div><span>{deployments.length} jobs</span></div>{deployments.length ? <ul className="liveDeploymentList">{deployments.map((deployment) => { const latest = [...(events[deployment.id] ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at))[0]; return <li key={deployment.id}><span><strong>{deployment.service_id}</strong><code>{deployment.id}</code><small>{deployment.node_id || "node pending"} · {deployment.agent_id || "Agent pending"}</small><small>{latest?.message_redacted || "No event reported"}</small></span><StatusBadge label={deploymentStage(deployment)} value={deployment.rollout_state ?? deployment.status} /></li>; })}</ul> : <p className="muted">No DeploymentJob has been submitted.</p>}</section>;
}

function RuntimesTab({ console, facts, topology }: { console: ConsoleController; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; topology: ReturnType<typeof useInfrastructureData>["data"]["topology"] }) {
  const selected = facts.runtimes.find((item) => item.id === console.route.runtime) ?? facts.runtimes[0];
  return <div className="masterDetail infrastructureMaster"><section className="masterList"><div className="sectionHeading"><h2>Runtimes</h2><span>{facts.runtimes.length} factual records</span></div>{facts.runtimes.length ? <ul>{facts.runtimes.map((runtime) => { const nodes = facts.nodes.filter((item) => item.runtime_id === runtime.id); const agents = facts.agents.filter((item) => item.runtime_id === runtime.id && item.status === "active"); const assignments = topology?.assignments.filter((item) => item.runtime_id === runtime.id) ?? []; return <li key={runtime.id}><button aria-pressed={selected?.id === runtime.id} onClick={() => console.navigate({ runtime: runtime.id })} type="button"><span><strong>{runtime.name}</strong><small>{facts.environments.find((item) => item.id === runtime.environment_id)?.name || runtime.environment_id} · {runtime.type}</small><small>{nodes.length} nodes · {agents.length} active Agents · {assignments.length} services</small></span><StatusBadge value={runtime.status} /></button></li>; })}</ul> : <Empty text="No runtime inventory exists." />}</section><section className="detailPanel">{selected ? <><div className="detailHeading"><div><p className="eyebrow">Runtime detail</p><h2>{selected.name}</h2></div><StatusBadge value={selected.status} /></div><dl className="evidenceGrid"><Fact label="Canonical ID" value={selected.id} /><Fact label="Environment" value={facts.environments.find((item) => item.id === selected.environment_id)?.name || selected.environment_id} /><Fact label="Type" value={selected.type} /><Fact label="Capacity coverage" value={runtimeCapacity(facts, selected.id)} /></dl><Related title="Nodes" values={facts.nodes.filter((item) => item.runtime_id === selected.id).map((item) => `${item.id} · ${item.status}`)} /><Related title="Agents" values={facts.agents.filter((item) => item.runtime_id === selected.id).map((item) => `${item.id} · ${item.status} · ${item.last_seen_at || "last seen unknown"}`)} /><Related title="Assignments" values={(topology?.assignments ?? []).filter((item) => item.runtime_id === selected.id).map((item) => `${item.service_key} · ${item.replicas} replicas`)} /></> : <Empty text="Select a runtime." />}</section></div>;
}

function NodesTab({ console, facts }: { console: ConsoleController; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]> }) {
  const selected = console.state.nodes.find((item) => item.id === console.route.node) ?? console.state.nodes[0];
  const unavailable = console.session?.agent_connected !== "ok";
  return <div className="masterDetail infrastructureMaster"><section className="masterList"><div className="sectionHeading"><h2>Nodes</h2><span>{console.state.nodes.length} records</span></div>{unavailable ? <p className="truthCallout">Agent source unavailable. Inventory remains visible; node mutations are disabled.</p> : null}<ul>{console.state.nodes.map((node) => <li key={node.id}><button aria-pressed={selected?.id === node.id} onClick={() => { console.navigate({ node: node.id }); void console.actions.diagnostics(node.id); }} type="button"><span><strong>{node.name}</strong><small>{facts.runtimes.find((runtime) => facts.nodes.some((item) => item.id === node.id && item.runtime_id === runtime.id))?.name || "Runtime unresolved"} · {node.role}</small><small>{node.agent_version || "Agent version unavailable"} · {node.last_seen_at || "last seen unknown"}</small></span><StatusBadge value={node.status} /></button></li>)}</ul></section><section className="detailPanel">{selected ? <><div className="detailHeading"><div><p className="eyebrow">Node detail</p><h2>{selected.name}</h2></div><StatusBadge value={selected.status} /></div><dl className="evidenceGrid"><Fact label="Canonical ID" value={selected.id} /><Fact label="Provider / region" value={[selected.provider, selected.region].filter(Boolean).join(" / ") || "Not reported"} /><Fact label="Capacity" value={`${capacityLabel(selected.cpu_cores, selected.memory_mb)} · disk ${selected.disk_total_gb === undefined ? "Unknown" : `${selected.disk_total_gb} GiB`}`} /><Fact label="K3s" value={selected.k3s_status || selected.k3s_role || "Not reported"} /><Fact label="Agent" value={[selected.agent_id, selected.agent_version].filter(Boolean).join(" · ") || "Unavailable"} /><Fact label="Heartbeat" value={selected.last_seen_at || "Not reported"} /></dl><Related title="Diagnostics" values={(console.state.nodeDetail?.open_bootstrap_events ?? []).map((item) => `${item.step}: ${item.message_redacted}`)} /><Related title="Recent deployment jobs" values={(console.state.nodeDetail?.recent_deployment_jobs ?? []).map((item) => `${item.id} · ${item.status}`)} /><div className="detailActions"><button disabled={unavailable} onClick={() => console.actions.nodeAction(selected.id, "offline")} type="button">Mark offline</button><button disabled={unavailable} onClick={() => console.actions.nodeAction(selected.id, "drain")} type="button">Drain</button><button className="danger" disabled={unavailable} onClick={() => console.actions.nodeAction(selected.id, "remove")} type="button">Remove…</button></div></> : <Empty text="No node inventory exists." />}</section></div>;
}

function BootstrapTab({ console, onReload }: { console: ConsoleController; onReload: () => Promise<void> }) {
  const selected = console.state.sessions.find((item) => item.id === console.route.session) ?? console.state.sessions[0];
  const progress = bootstrapProgress(selected?.checkpoint, selected ? console.state.bootstrapEvents.length : 0);
  return <div className="bootstrapLayout"><section className="bootstrapInventory"><div className="sectionHeading"><div><h2>Bootstrap sessions</h2><p>Durable Cloud worker sessions; no public DNS assumption.</p></div></div>{console.state.sessions.length ? <ul>{console.state.sessions.map((session) => <li key={session.id}><button aria-pressed={selected?.id === session.id} onClick={() => { console.navigate({ session: session.id }); void console.actions.loadBootstrapEvents(session.id); }} type="button"><span><strong>{session.public_host || session.id}</strong><small>{session.role} · attempt {session.attempt_count ?? "?"}/{session.max_attempts ?? "?"}</small></span><StatusBadge value={session.status} /></button></li>)}</ul> : <Empty title="No bootstrap sessions" text="Use Add server to review connection input and create a Local API bootstrap session." />}</section><section className="bootstrapDetail">{selected ? <><div className="detailHeading"><div><p className="eyebrow">Selected session</p><h2>{selected.id}</h2></div><StatusBadge value={selected.status} /></div><dl className="evidenceGrid"><Fact label="Attempt" value={`${selected.attempt_count ?? "Unknown"}/${selected.max_attempts ?? "Unknown"}`} /><Fact label="Progress" value={progress.percent === null ? progress.label : `${progress.percent}% · ${progress.label}`} /><Fact label="Next step" value={selected.checkpoint ? `Step ${selected.checkpoint.next_step_index}` : "Not reported"} /><Fact label="Failure" value={selected.last_failure_message_redacted || selected.last_failure_code || "None reported"} /></dl><EventTimeline events={console.state.bootstrapEventsSessionID === selected.id ? console.state.bootstrapEvents : []} /><button disabled={!['failed', 'dead_letter'].includes(selected.status)} onClick={() => console.actions.retryBootstrap(selected.id, onReload)} type="button">Retry eligible session</button></> : <Empty text="Select a bootstrap session." />}</section></div>;
}

function BootstrapDialog({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated: () => Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);
  return <dialog aria-describedby="bootstrap-description" aria-labelledby="bootstrap-title" className="placementDialog" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}><div className="dialogHeading"><div><p className="eyebrow">Bootstrap action</p><h2 id="bootstrap-title">Add server</h2><p id="bootstrap-description">Review non-sensitive target facts first. The one-time credential is requested only at final confirmation.</p></div><button aria-label="Close add server dialog" autoFocus className="iconButton" onClick={onClose} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></div><form className="form placementForm" onSubmit={(event) => { void console.actions.addServer(event, onCreated); onClose(); }}><label>Role<select className="select" name="role" required><option value="">Choose role…</option><option value="first_server">First server</option><option value="worker">Worker</option></select></label><label>SSH host or IP<input autoComplete="off" className="field" name="public_host" placeholder="203.0.113.10…" required /></label><label>SSH port<input className="field" inputMode="numeric" min="1" name="ssh_port" required type="number" /></label><label>SSH username<input autoComplete="username" className="field" name="ssh_username" required spellCheck={false} /></label><label>Authentication<select className="select" name="auth_method" required><option value="">Choose method…</option><option value="password">Password</option><option value="private_key">Private key</option></select></label><p className="notice span2">No credential is collected on this step.</p><div className="dialogActions span2"><button onClick={onClose} type="button">Cancel</button><button type="submit">Review bootstrap request</button></div></form></dialog>;
}

function Fact({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function Related({ title, values }: { title: string; values: string[] }) { return <section><h3>{title}</h3>{values.length ? <ul className="compactList">{values.map((value) => <li key={value}>{value}</li>)}</ul> : <p className="muted">None reported.</p>}</section>; }
function runtimeCapacity(facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>, runtimeID: string) { const nodes = facts.nodes.filter((item) => item.runtime_id === runtimeID); return nodes.length && nodes.every((item) => item.cpu_cores !== undefined && item.memory_mb !== undefined) ? nodes.map((item) => capacityLabel(item.cpu_cores, item.memory_mb)).join(" · ") : "Unknown"; }
