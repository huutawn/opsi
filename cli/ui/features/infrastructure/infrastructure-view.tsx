"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import { ApplicationWizard } from "@/features/applications/application-wizard";
import type { ConsoleController } from "@/features/console/types";
import { PlacementDialog } from "@/features/infrastructure/placement-dialog";
import { useInfrastructureData } from "@/features/infrastructure/data";
import { LiveTopologyCanvas, TopologyDesignCanvas } from "@/features/infrastructure/topology-canvas";
import { DeploymentReview } from "@/features/infrastructure/deployment-review";
import { deploymentPhase, liveDeploymentHealth } from "@/features/infrastructure/deployment-review-model";
import { LocalClient } from "@/lib/api/local-client";
import type { TimelineEvent } from "@/lib/contracts/registry";
import { bootstrapLifecycleStatus, bootstrapProgress, capacityLabel, currentEnvironment, serverLifecycle, topologyOnboarding, type CanvasDraft, type ServerLifecycle, type TopologyOnboardingState } from "@/lib/presentation/infrastructure/model";

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
  const environment = currentEnvironment(data.facts, console.route.environment ?? "");

  return <div className="infrastructurePage">
    {console.route.tab !== "topology" ? <div className="destinationToolbar"><p>{error || "Cloud topology facts remain visible when Agent runtime data is unavailable."}</p><button data-review-trigger={console.route.tab === "bootstrap" ? "bootstrap" : undefined} onClick={(event) => { if (console.route.tab === "bootstrap") { bootstrapTrigger.current = event.currentTarget; setBootstrapOpen(true); } else { placementTrigger.current = event.currentTarget; setPlacementOpen(true); } }} type="button">{console.route.tab === "bootstrap" ? "Connect Server" : "Plan placement"}</button></div> : null}
    {console.route.tab === "topology" ? <TopologyTab bindings={data.bindings} builds={data.builds} console={console} environment={environment} error={error} facts={data.facts} key={`${projectID}:${environment?.id ?? "unresolved"}`} mode={mode} onAddService={(trigger) => { serviceTrigger.current = trigger; setServiceOpen(true); }} onConnectServer={(trigger) => { bootstrapTrigger.current = trigger; setBootstrapOpen(true); }} onMode={(next) => console.navigate({ topologyMode: next })} onPlanPlacement={(trigger) => { placementTrigger.current = trigger; setPlacementOpen(true); }} onReload={load} policies={data.policies} repositories={data.repositories} topology={data.topology} /> : null}
    {console.route.tab === "runtimes" ? <RuntimesTab console={console} facts={data.facts} topology={data.topology} /> : null}
    {console.route.tab === "nodes" ? <NodesTab console={console} facts={data.facts} /> : null}
    {console.route.tab === "bootstrap" ? <BootstrapTab console={console} facts={data.facts} onReload={load} /> : null}
    {placementOpen ? <PlacementDialog console={console} data={{ facts: data.facts, topology: data.topology, repositories: data.repositories, bindings: data.bindings, builds: data.builds, policies: data.policies }} onApplied={() => { void console.actions.load(); void load(); }} onClose={() => { setPlacementOpen(false); window.requestAnimationFrame(() => placementTrigger.current?.focus()); }} /> : null}
    {bootstrapOpen ? <BootstrapDialog console={console} onClose={() => { setBootstrapOpen(false); window.requestAnimationFrame(() => bootstrapTrigger.current?.focus()); }} onCreated={load} /> : null}
    {serviceOpen ? <ApplicationWizard console={console} onClose={() => { setServiceOpen(false); window.requestAnimationFrame(() => serviceTrigger.current?.focus()); }} onCreated={async () => { console.navigate({ topologyMode: "design" }); await load(); }} /> : null}
  </div>;
}

function TopologyTab({ bindings, builds, console, environment, error, facts, mode, onAddService, onConnectServer, onMode, onPlanPlacement, onReload, policies, repositories, topology }: { bindings: ReturnType<typeof useInfrastructureData>["data"]["bindings"]; builds: ReturnType<typeof useInfrastructureData>["data"]["builds"]; console: ConsoleController; environment?: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>["environments"][number]; error: string; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; mode: "design" | "live"; onAddService: (trigger: HTMLButtonElement) => void; onConnectServer: (trigger: HTMLButtonElement) => void; onMode: (mode: "design" | "live") => void; onPlanPlacement: (trigger: HTMLButtonElement) => void; onReload: () => Promise<void>; policies: ReturnType<typeof useInfrastructureData>["data"]["policies"]; repositories: ReturnType<typeof useInfrastructureData>["data"]["repositories"]; topology: ReturnType<typeof useInfrastructureData>["data"]["topology"] }) {
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
    {mode === "design" ? <>
      {error ? <p className="truthCallout" role="alert">{error}</p> : null}
      {!topology ? <div className="truthCallout"><b>No topology plan</b><p>Infrastructure facts are shown without service placement edges. Service inventory is not used to fabricate assignments.</p></div> : null}
      <TopologyDesignCanvas bindings={bindings} builds={builds} console={console} draft={draft} facts={facts} onDraft={setDraft} onReload={onReload} repositories={repositories} topology={topology} />
      <div className="designSupportingFacts"><TopologyOnboarding action={act} state={onboarding} /><ServerLifecycleCard console={console} lifecycle={lifecycle} /></div>
    </> : <>{error ? <p className="truthCallout" role="alert">{error}</p> : null}<LiveTopology console={console} environment={environment} facts={facts} lifecycle={lifecycle} onConnectServer={onConnectServer} onReload={onReload} /></>}
    {topology ? <div className="designDeploymentReview" hidden={mode !== "design"}><DeploymentReview builds={builds} console={console} environmentID={environment?.id ?? ""} environmentName={environment?.name ?? "No current environment"} facts={facts} onLive={() => onMode("live")} policies={policies} topology={topology} /></div> : null}
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
    {session?.id === console.state.bootstrapCommandSessionID && console.state.bootstrapCommand ? <BootstrapCommand command={console.state.bootstrapCommand} /> : session?.status === "waiting" && session.auth_method === "command" ? <p className="notice" role="status">Waiting for the one-time command to connect. This browser only shows the command when it is issued.</p> : null}
    {recent.length ? <><div className="sectionHeading lifecycleEventsHeading"><div><h4>Recent bootstrap events</h4><p>Latest five factual events for this session.</p></div><span>{events.length} total</span></div><EventTimeline events={recent} /></> : null}
    {session ? <details className="lifecycleDetails"><summary>Open full bootstrap details</summary><dl className="evidenceGrid">{reportedDetails.map(([label, value]) => <Fact key={label} label={label} value={value} />)}</dl>{events.length > 5 ? <EventTimeline events={events} /> : null}</details> : null}
  </section>;
}

function EventTimeline({ events }: { events: TimelineEvent[] }) {
  return <ol className="eventTimeline">{events.map((event) => <li key={event.id}><span aria-hidden="true" /><div><b>{event.step}</b><p>{event.message_redacted}</p><small>{event.created_at}</small></div></li>)}</ol>;
}

function LiveTopology({ console, environment, facts, lifecycle, onConnectServer, onReload }: { console: ConsoleController; environment?: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>["environments"][number]; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; lifecycle: ServerLifecycle; onConnectServer: (trigger: HTMLButtonElement) => void; onReload: () => Promise<void> }) {
  if (!environment) return <div className="truthCallout" role="alert"><b>Choose the current environment</b><p>Live deployment facts are blocked because this project has multiple environments and none is selected.</p></div>;
  const runtimes = facts.runtimes.filter((runtime) => runtime.environment_id === environment.id);
  const connections = console.state.services.flatMap((service) => (service.configuration?.bindings ?? []).map((binding) => ({ source: service.name, target: binding.target_service_key, kind: binding.kind, route: binding.path || binding.env_prefix || "" })));
  const exposures = console.state.deployments.filter((deployment) => deployment.environment_id === environment.id && deployment.exposure_spec);
  const lifecycleAction = lifecycle.status === "Failed" ? "Retry bootstrap" : ["Waiting", "Connecting", "Bootstrapping"].includes(lifecycle.status) ? "Inspect progress" : lifecycle.status === "Unknown" && !lifecycle.session ? "Connect Server" : "Refresh factual state";
  function act(event: React.MouseEvent<HTMLButtonElement>) {
    if (lifecycleAction === "Connect Server") onConnectServer(event.currentTarget);
    else if (lifecycleAction === "Retry bootstrap" && lifecycle.session) console.actions.retryBootstrap(lifecycle.session.id, onReload);
    else if (lifecycleAction === "Inspect progress" && lifecycle.session) { void console.actions.loadBootstrapEvents(lifecycle.session.id); window.requestAnimationFrame(() => document.getElementById("server-lifecycle-heading")?.focus()); }
    else void onReload();
  }
  return <div className="liveTopology" data-mode="live">
    <section className="liveOverview" aria-labelledby="live-overview-heading">
      <div><p className="liveContext">{console.state.project?.name} / {environment.name} / Topology</p><p className="eyebrow">Factual runtime workspace</p><h3 id="live-overview-heading">Environment and runtime overview</h3><p>Observed runtime, node, Agent, workload, exposure, and rollback facts. Design draft placement is never used here.</p></div>
      <div className="liveOverviewStatus"><StatusBadge label={lifecycle.status} value={lifecycle.status === "Connecting" ? "bootstrapping" : lifecycle.status} /><span>{runtimes.length} runtimes · {facts.nodes.filter((node) => runtimes.some((runtime) => runtime.id === node.runtime_id)).length} nodes</span><button className="primary" onClick={act} type="button">{lifecycleAction}</button></div>
    </section>
    <ServerLifecycleCard console={console} lifecycle={lifecycle} />
    <LiveTopologyCanvas console={console} environment={environment} facts={facts} />
    <section className="liveConnections" aria-labelledby="live-connections-heading"><div className="sectionHeading"><div><p className="eyebrow">Applied configuration</p><h3 id="live-connections-heading">Connections and exposure</h3><p>Routes below are present only when the Local API reports applied service configuration or ExposureSpec facts.</p></div><span>{connections.length + exposures.length} facts</span></div><div className="liveConnectionGrid"><div><h4>Service connections</h4>{connections.length ? <ul className="compactList">{connections.map((connection, index) => <li key={`${connection.source}:${connection.target}:${index}`}><strong>{connection.source} → {connection.target}</strong><small>{connection.kind}{connection.route ? ` · ${connection.route}` : " · route not reported"}</small></li>)}</ul> : <p className="muted">No applied connections reported.</p>}</div><div><h4>Exposure</h4>{exposures.length ? <ul className="compactList">{exposures.map((deployment) => <li key={deployment.id}><strong>{deployment.exposure_spec?.hostname}{deployment.exposure_spec?.path}</strong><small>{deployment.service_id} · {deployment.rollout_state || deployment.status}</small></li>)}</ul> : <p className="muted">No factual public exposure reported.</p>}</div></div></section>
    <LiveDeploymentBoard console={console} environmentID={environment.id} environmentName={environment.name} />
  </div>;
}

function LiveDeploymentBoard({ console, environmentID, environmentName }: { console: ConsoleController; environmentID: string; environmentName: string }) {
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
        await Promise.all((response.deployments ?? []).filter((job) => job.environment_id === environmentID).map(async (job) => { const result = await client.deploymentEvents(projectID, job.id); next[job.id] = result.events ?? []; }));
        if (!disposed) { setEvents(next); await reload.current(); }
      } catch { /* retain last Cloud state */ }
      if (!disposed) timer = window.setTimeout(poll, 4000);
    }
    void poll();
    return () => { disposed = true; window.clearTimeout(timer); };
  }, [client, environmentID, projectID]);
  const deployments = console.state.deployments.filter((deployment) => deployment.environment_id === environmentID).sort((a, b) => b.created_at.localeCompare(a.created_at));
  return <section><div className="sectionHeading"><div><p className="eyebrow">Current environment · {environmentName}</p><h3>Deployment live state</h3><p>Workload, route, and rollback jobs are separate durable facts. Refresh restores every active phase.</p></div><span>{deployments.length} jobs</span></div>{deployments.length ? <ul className="liveDeploymentList">{deployments.map((deployment) => {
    const service = console.state.services.find((item) => item.id === deployment.service_id);
    const digest = deployment.current_digest || deployment.terminal_result?.current_digest || deployment.desired_digest || deployment.snapshot?.image.digest;
    const revision = deployment.snapshot?.authority.service_configuration_revision;
    const jobEvents = [...(events[deployment.id] ?? [])].sort((a, b) => b.created_at.localeCompare(a.created_at));
    const endpoint = deployment.exposure_spec ? `${deployment.exposure_spec.hostname}${deployment.exposure_spec.path}` : "Internal only";
    const blocked = deployment.rollback_blocked_reason || "Cloud did not report a previous known-good deployment.";
    const failure = deployment.failure_message_redacted || deployment.terminal_result?.failure_message_redacted;
    return <li data-deployment-state={deployment.rollout_state || deployment.status} key={deployment.id}><div className="liveDeploymentHeading"><span><small>{deploymentPhase(deployment)}</small><strong>{service?.name || deployment.service_id}</strong><code>{deployment.id}</code></span><StatusBadge label={liveDeploymentHealth(deployment)} value={deployment.rollout_state ?? deployment.status} /></div><dl className="liveDeploymentFacts"><Fact label="Image digest" value={digest || "Not reported"} /><Fact label="Revision" value={revision === undefined ? "Not reported" : `configuration r${revision} · topology r${deployment.snapshot?.authority.topology_revision ?? "?"}`} /><Fact label="Endpoint" value={endpoint} /><Fact label="Route path" value={deployment.exposure_spec?.path || "Not public"} /></dl>{failure ? <p className="deploymentFailure" role="status"><b>{deployment.failure_code || deployment.terminal_result?.failure_code || "Deployment failed"}</b> · {failure}</p> : null}{jobEvents.length ? <EventTimeline events={jobEvents.slice(0, 3)} /> : <p className="muted">No deployment event reported.</p>}<div className="liveDeploymentActions"><button disabled={!deployment.rollback_eligible || console.state.busy === `rollback-${deployment.id}`} onClick={() => console.actions.rollback(deployment.id)} type="button">{console.state.busy === `rollback-${deployment.id}` ? "Rolling back…" : "Rollback"}</button>{deployment.rollback_eligible ? <small>Restore {deployment.previous_digest || deployment.terminal_result?.previous_digest || "previous known-good digest"}</small> : <small>{blocked}</small>}</div></li>;
  })}</ul> : <p className="muted">No DeploymentJob has been submitted in {environmentName}.</p>}</section>;
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

function BootstrapTab({ console, facts, onReload }: { console: ConsoleController; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; onReload: () => Promise<void> }) {
  const selected = console.state.sessions.find((item) => item.id === console.route.session) ?? console.state.sessions[0];
  const lifecycle = serverLifecycle(facts, console.state.sessions);
  const progress = bootstrapProgress(selected?.checkpoint, selected ? console.state.bootstrapEvents.length : 0);
  return <div className="bootstrapLayout"><section className="bootstrapInventory"><div className="sectionHeading"><div><p className="eyebrow">Connection history</p><h2>Bootstrap sessions</h2><p>Durable, project-scoped Cloud sessions. Credentials and plaintext commands are never reconstructed here.</p></div></div>{console.state.sessions.length ? <ul>{console.state.sessions.map((session) => { const state = bootstrapLifecycleStatus(session.status); return <li key={session.id}><button aria-pressed={selected?.id === session.id} onClick={() => { console.navigate({ session: session.id }); void console.actions.loadBootstrapEvents(session.id); }} type="button"><span><strong>{session.public_host || session.id}</strong><small>{session.role} · attempt {session.attempt_count ?? "?"}/{session.max_attempts ?? "?"}</small><small>{session.id}</small></span><StatusBadge label={state === "Unknown" ? session.status : state} value={session.status} /></button></li>; })}</ul> : <Empty title="No bootstrap sessions" text="Use Connect Server to generate the first one-time bootstrap command." />}</section><section className="bootstrapDetail"><div className="detailHeading"><div><p className="eyebrow">Factual server lifecycle</p><h2>{lifecycle.runtime?.name || lifecycle.session?.public_host || "Connect a server"}</h2></div><StatusBadge label={lifecycle.status} value={lifecycle.status === "Connecting" ? "bootstrapping" : lifecycle.status} /></div><ol aria-label="Server connection progress" className="bootstrapLifecycleRail">{["Waiting", "Connecting", "Bootstrapping", "Ready"].map((state) => <li aria-current={lifecycle.status === state ? "step" : undefined} data-state={lifecycle.status === state ? "current" : "idle"} key={state}><StatusBadge label={state} value={state === "Connecting" || state === "Bootstrapping" ? "bootstrapping" : state} /></li>)}</ol><ServerLifecycleCard console={console} lifecycle={lifecycle} />{selected ? <section className="bootstrapSessionDetail" aria-labelledby="selected-bootstrap-heading"><div className="sectionHeading"><div><p className="eyebrow">Selected session</p><h3 id="selected-bootstrap-heading">{selected.id}</h3></div><StatusBadge value={selected.status} /></div><dl className="evidenceGrid"><Fact label="Attempt" value={`${selected.attempt_count ?? "Unknown"}/${selected.max_attempts ?? "Unknown"}`} /><Fact label="Progress" value={progress.percent === null ? progress.label : `${progress.percent}% · ${progress.label}`} /><Fact label="Next step" value={selected.checkpoint ? `Step ${selected.checkpoint.next_step_index}` : "Not reported"} /><Fact label="Failure" value={selected.last_failure_message_redacted || selected.last_failure_code || "None reported"} /></dl><EventTimeline events={console.state.bootstrapEventsSessionID === selected.id ? console.state.bootstrapEvents : []} />{['failed', 'dead_letter'].includes(selected.status) ? <button className="primary" onClick={() => console.actions.retryBootstrap(selected.id, onReload)} type="button">Retry bootstrap</button> : null}</section> : null}</section></div>;
}

function BootstrapDialog({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated: () => Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [method, setMethod] = useState("command");
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);
  return <dialog aria-describedby="bootstrap-description" aria-labelledby="bootstrap-title" className="connectServerDialog placementDialog" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}><div className="dialogHeading"><div><p className="eyebrow">Topology · Connect Server</p><h2 id="bootstrap-title">Connect Server</h2><p id="bootstrap-description">Generate a one-time command, run it on the VPS, then follow factual progress until Ready or Failed.</p></div><button aria-label="Close connect server dialog" autoFocus className="iconButton" onClick={onClose} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></div><ol aria-label="Connect Server steps" className="connectServerSteps"><li aria-current="step"><b>1</b><span>Generate command</span></li><li><b>2</b><span>Run on VPS</span></li><li><b>3</b><span>Wait for Ready</span></li></ol><form className="form connectServerForm" onSubmit={(event) => { void console.actions.addServer(event, onCreated); onClose(); }}><label>Role<select className="select" defaultValue="first_server" name="role" required><option value="first_server">First server</option><option value="worker">Worker</option></select></label><label>Server IP or hostname<input autoComplete="off" className="field" name="public_host" placeholder="203.0.113.10" required spellCheck={false} /></label><fieldset className="bootstrapMethod span2"><legend>Bootstrap command</legend><label className="methodChoice commandMethod"><input checked={method === "command"} name="auth_method" onChange={() => setMethod("command")} type="radio" value="command" /><span><strong>Run bootstrap command</strong><small>Recommended. No SSH private key, SSH password, Cloud PAT, or global Worker token is requested by this browser flow.</small></span></label><details><summary>Advanced: Bootstrap over SSH</summary><div className="advancedBootstrap"><p>Use the existing verified SSH bootstrap only when command execution on the VPS is not available.</p><label className="methodChoice"><input checked={method === "password"} name="auth_method" onChange={() => setMethod("password")} type="radio" value="password" /><span><strong>SSH password</strong><small>Requested again only after mutation review.</small></span></label><label className="methodChoice"><input checked={method === "private_key"} name="auth_method" onChange={() => setMethod("private_key")} type="radio" value="private_key" /><span><strong>SSH private key</strong><small>Uses the existing verified known_hosts workflow.</small></span></label>{method !== "command" ? <div className="sshBootstrapFields"><label>SSH port<input className="field" defaultValue="22" inputMode="numeric" max="65535" min="1" name="ssh_port" required type="number" /></label><label>SSH username<input autoComplete="username" className="field" defaultValue="root" name="ssh_username" required spellCheck={false} /></label></div> : null}</div></details></fieldset><p className="notice span2">The issued token expires, is scoped to this reviewed project/environment/runtime/node session, and can claim the exact session once. Refresh cannot reconstruct plaintext after issuance.</p><div className="dialogActions span2"><button onClick={onClose} type="button">Cancel</button><button className="primary" type="submit">Generate bootstrap command</button></div></form></dialog>;
}

function BootstrapCommand({ command }: { command: string }) {
  const [copyState, setCopyState] = useState("Copy command");
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopyState("Copied");
    } catch {
      setCopyState("Copy failed");
    }
  }
  return <section aria-labelledby="bootstrap-command-title" className="bootstrapCommand"><div><div><p className="eyebrow">One-time scoped command</p><h4 id="bootstrap-command-title">Copy, then run on the VPS as root</h4></div><button className="primary" onClick={() => void copy()} type="button">{copyState}</button></div><code>{command}</code><p role="status">Waiting for server. This plaintext is available only in the issuing browser memory; refresh restores lifecycle facts, not the command.</p></section>;
}

function Fact({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function Related({ title, values }: { title: string; values: string[] }) { return <section><h3>{title}</h3>{values.length ? <ul className="compactList">{values.map((value) => <li key={value}>{value}</li>)}</ul> : <p className="muted">None reported.</p>}</section>; }
function runtimeCapacity(facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>, runtimeID: string) { const nodes = facts.nodes.filter((item) => item.runtime_id === runtimeID); return nodes.length && nodes.every((item) => item.cpu_cores !== undefined && item.memory_mb !== undefined) ? nodes.map((item) => capacityLabel(item.cpu_cores, item.memory_mb)).join(" · ") : "Unknown"; }
