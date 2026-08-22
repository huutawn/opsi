"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Empty, Icon, StatusBadge } from "@/components/ui/primitives";
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

export function TopologyTab({ bindings, builds, console, environment, error, facts, mode, onAddService, onConnectServer, onMode, onPlanPlacement, onReload, policies, repositories, topology }: { bindings: ReturnType<typeof useInfrastructureData>["data"]["bindings"]; builds: ReturnType<typeof useInfrastructureData>["data"]["builds"]; console: ConsoleController; environment?: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>["environments"][number]; error: string; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>; mode: "design" | "live"; onAddService: (trigger: HTMLButtonElement) => void; onConnectServer: (trigger: HTMLButtonElement) => void; onMode: (mode: "design" | "live") => void; onPlanPlacement: (trigger: HTMLButtonElement) => void; onReload: () => Promise<void>; policies: ReturnType<typeof useInfrastructureData>["data"]["policies"]; repositories: ReturnType<typeof useInfrastructureData>["data"]["repositories"]; topology: ReturnType<typeof useInfrastructureData>["data"]["topology"] }) {
  const [draft, setDraft] = useState<CanvasDraft>({});
  const [unpublishedChanges, setUnpublishedChanges] = useState(0);
  const lifecycle = serverLifecycle(facts, console.state.sessions);
  const onboarding = topologyOnboarding(facts, topology, console.state.sessions);
  const hasPlacedApplication = Boolean(
    topology?.assignments.some((assignment) => facts.services.some((service) => service.key === assignment.service_key))
  );
  useEffect(() => {
    if (mode === "live") setUnpublishedChanges(0);
  }, [mode]);
  function act(event: React.MouseEvent<HTMLButtonElement>) {
    if (onboarding.kind === "connect") onConnectServer(event.currentTarget);
    else if (onboarding.kind === "bootstrap") console.navigate({ view: "infrastructure", tab: "bootstrap", session: onboarding.sessionID ?? "" });
    else if (onboarding.kind === "retry" && onboarding.sessionID) console.actions.retryBootstrap(onboarding.sessionID, onReload);
    else if (onboarding.kind === "application") onAddService(event.currentTarget);
    else if (onboarding.kind === "placement") onPlanPlacement(event.currentTarget);
    else { onMode("design"); window.requestAnimationFrame(() => document.getElementById("topology-heading")?.focus()); }
  }
  return (
    <section className="topologyWorkspace" aria-labelledby="topology-heading">
      <div className="flex flex-wrap items-center justify-between gap-4 pb-2 border-b border-outline-variant/15">
        <div>
          <h2 className="font-headline-lg text-2xl font-bold text-on-surface" id="topology-heading" tabIndex={-1}>
            Topology
          </h2>
          <p className="font-body-md text-xs text-on-surface-variant mt-1">
            {mode === "design" ? "Interactive application placement and configuration canvas" : "Real-time observed runtime and deployment infrastructure"}
          </p>
        </div>
        <div className="flex items-center gap-3">
          <span className="px-2.5 py-1 rounded-full text-xs font-code-md bg-surface-container text-on-surface-variant border border-outline-variant/20">
            {mode === "design" ? (topology ? `Plan r${topology.revision}` : "No Plan") : "Observed State"}
          </span>
          <div aria-label="Topology view mode" className="topologyMode flex bg-surface-container-highest p-1 rounded-full border border-outline-variant/20 shadow-inner" role="group">
            <button
              aria-pressed={mode === "design"}
              className={`px-4 py-2 min-h-[40px] min-w-[40px] rounded-full text-xs font-semibold transition-all cursor-pointer ${
                mode === "design" ? "bg-surface-bright text-on-surface shadow-md" : "text-on-surface-variant hover:text-on-surface"
              }`}
              onClick={() => onMode("design")}
              type="button"
            >
              Design
            </button>
            <button
              aria-pressed={mode === "live"}
              className={`px-4 py-2 min-h-[40px] min-w-[40px] rounded-full text-xs font-semibold transition-all flex items-center gap-1.5 cursor-pointer ${
                mode === "live" ? "bg-surface-bright text-on-surface shadow-md" : "text-on-surface-variant hover:text-on-surface"
              }`}
              onClick={() => onMode("live")}
              type="button"
            >
              <span className="w-1.5 h-1.5 rounded-full bg-status-ready animate-pulse" />
              Live
            </button>
          </div>
        </div>
      </div>

      {error ? <p className="topologyError p-3 bg-error-container/20 text-error border border-error/30 rounded-xl text-xs" role="alert">{error}</p> : null}
      <TopologyContextBar
        action={act}
        console={console}
        lifecycle={lifecycle}
        onDetails={() => console.navigate({ view: "infrastructure", tab: "bootstrap", session: lifecycle.session?.id ?? "" })}
        state={onboarding}
      />

      {mode === "design" ? (
        <>
          <TopologyDesignCanvas bindings={bindings} builds={builds} console={console} draft={draft} facts={facts} onDraft={setDraft} onReload={onReload} onUnpublishedChanges={setUnpublishedChanges} repositories={repositories} topology={topology} />
        </>
      ) : (
        <>
          <LiveTopology console={console} environment={environment} facts={facts} />
        </>
      )}
      {mode === "design" && (hasPlacedApplication || unpublishedChanges > 0) && topology ? (
        <DeploymentReview builds={builds} console={console} environmentID={environment?.id ?? ""} environmentName={environment?.name ?? "Default"} facts={facts} onLive={() => onMode("live")} policies={policies} topology={topology} />
      ) : mode === "design" ? (
        <p className="topologyDeploymentHint" role="status">
          Deployment review will appear after an application is placed or a canvas change is waiting to be published.
        </p>
      ) : null}
    </section>
  );
}

function TopologyContextBar({ action, console, lifecycle, onDetails, state }: { action: (event: React.MouseEvent<HTMLButtonElement>) => void; console: ConsoleController; lifecycle: ServerLifecycle; onDetails: () => void; state: TopologyOnboardingState }) {
  const nodeRecord = console.state.nodes.find((node) => node.id === lifecycle.node?.id);
  const host = lifecycle.session?.public_host || nodeRecord?.public_host || lifecycle.node?.id || lifecycle.runtime?.name || "No server registered";
  const heartbeat = lifecycle.agent?.last_seen_at || lifecycle.node?.last_seen_at || "Not reported";
  const diagnostic = lifecycle.status === "Failed"
    ? lifecycle.session?.last_failure_message_redacted || lifecycle.session?.last_failure_code || state.description
    : state.progress
      ? state.progress.percent === null ? state.progress.label : `${state.progress.percent}% · ${state.progress.label}`
      : null;
  return (
    <section
      aria-label="Topology context"
      className="topologyContextBar"
      data-state={state.kind}
    >
      <div className="topologyNextStep">
        <div>
          <p className="text-[11px] font-code-md text-primary uppercase font-bold tracking-wider">Next step</p>
          <h3 className="font-headline-md text-base font-bold text-on-surface mt-1" id="topology-next-step">{state.title}</h3>
          <p className="font-body-md text-xs text-on-surface-variant mt-1 leading-relaxed">{state.description}</p>
        </div>
        <Button className="topologyPrimaryAction" onClick={action} size="sm" variant="primary">
          <Icon name={state.kind === "application" ? "add" : state.kind === "placement" ? "account_tree" : state.kind === "retry" ? "refresh" : "arrow_forward"} className="text-[16px]" />
          {state.action}
        </Button>
      </div>
      <div className="topologyServerSummary">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-[11px] font-code-md text-on-surface-variant uppercase font-bold tracking-wider">Registered server</p>
            <p className="font-code-md text-sm font-semibold text-on-surface truncate mt-1" title={host}>{host}</p>
          </div>
          <StatusBadge label={`Server ${lifecycle.status}`} value={lifecycle.status === "Ready" ? "healthy" : lifecycle.status === "Failed" || lifecycle.status === "Offline" ? "failed" : "unknown"} />
        </div>
        <p className="text-xs text-on-surface-variant mt-2">
          Registered Agent: <span className="font-code-md text-on-surface">{lifecycle.agent ? `${lifecycle.agent.id.slice(0, 8)} · ${lifecycle.agent.status}` : "Not reported"}</span>
          <span aria-hidden="true"> · </span>
          Last heartbeat: <span className="font-code-md text-on-surface">{heartbeat}</span>
        </p>
        {diagnostic ? <p className="topologyServerDiagnostic" role={lifecycle.status === "Failed" ? "alert" : "status"}>{diagnostic}</p> : null}
        <button className="topologyDetailsLink" onClick={onDetails} type="button">View server details</button>
      </div>
    </section>
  );
}

function ServerLifecycleCard({ console, lifecycle }: { console: ConsoleController; lifecycle: ServerLifecycle }) {
  const session = lifecycle.session;
  const nodeRecord = console.state.nodes.find((node) => node.id === lifecycle.node?.id);
  const publicHost = session?.public_host || nodeRecord?.public_host;
  const events = console.state.bootstrapEventsSessionID === session?.id ? [...console.state.bootstrapEvents].sort((a, b) => b.created_at.localeCompare(a.created_at)) : [];
  const recent = events.slice(0, 5);
  const progress = session ? bootstrapProgress(session.checkpoint, events.length) : null;
  const facts: Array<[string, string] | null> = [
    publicHost ? ["Host", publicHost] : null,
    lifecycle.runtime ? ["Runtime", `${lifecycle.runtime.name} · ${lifecycle.runtime.status}`] : null,
    lifecycle.node ? ["Node", `${lifecycle.node.id} · ${lifecycle.node.status}`] : null,
    lifecycle.agent ? ["Agent", `${lifecycle.agent.id.slice(0, 8)} · ${lifecycle.agent.status}`] : null,
    session ? ["Bootstrap", `${session.status} · ${session.created_at.slice(0, 10)}`] : null,
    progress ? ["Progress", progress.percent === null ? progress.label : `${progress.percent}% · ${progress.label}`] : null,
    session?.last_failure_code ? ["Error", session.last_failure_code] : null,
  ];
  const reportedFacts = facts.filter((fact): fact is [string, string] => fact !== null);
  return (
    <section className="serverLifecycle p-5 bg-surface-container-low/90 backdrop-blur-md rounded-2xl border border-outline-variant/15 space-y-4 shadow-sm" aria-labelledby="server-lifecycle-heading">
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="text-[11px] font-code-md text-primary uppercase font-bold tracking-wider">Server Status</p>
          <h3 className="font-headline-md text-base font-bold text-on-surface mt-1" id="server-lifecycle-heading" tabIndex={-1}>
            {publicHost || lifecycle.runtime?.name || session?.id || "Active Server"}
          </h3>
        </div>
        <StatusBadge label={lifecycle.status} value={lifecycle.status === "Connecting" ? "in_progress" : lifecycle.status === "Ready" ? "healthy" : "unknown"} />
      </div>
      {reportedFacts.length ? (
        <dl className="grid grid-cols-2 gap-2 text-xs">
          {reportedFacts.map(([label, value]) => (
            <div key={label} className="bg-surface-container/60 p-2 rounded-lg border border-outline-variant/10">
              <dt className="text-on-surface-variant text-[11px]">{label}</dt>
              <dd className="font-code-md text-on-surface font-semibold truncate mt-0.5">{value}</dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className="text-xs text-on-surface-variant">No server identity or bootstrap facts reported.</p>
      )}
      {session?.id === console.state.bootstrapCommandSessionID && console.state.bootstrapCommand ? (
        <BootstrapCommand command={console.state.bootstrapCommand} />
      ) : session && session.status === "waiting" ? (
        <p className="text-xs text-on-surface-variant bg-surface-container/40 p-3 rounded-xl border border-outline-variant/10" role="status">
          This browser only shows the command when it is issued. Waiting for server to report progress.
        </p>
      ) : null}
      {lifecycle.status !== "Ready" && recent.length ? (
        <>
          <div className="flex items-center justify-between text-[11px] font-label-sm text-on-surface-variant pt-2 border-t border-outline-variant/10">
            <span className="uppercase font-semibold">Recent Events</span>
            <span>{events.length} total</span>
          </div>
          <ol className="eventTimeline space-y-2 text-xs">
            {recent.map((event) => (
              <li key={event.id} className="flex items-start gap-2 bg-surface-container/40 p-2 rounded-lg border border-outline-variant/10">
                <span className="w-2 h-2 rounded-full bg-primary mt-1 shrink-0" />
                <div className="min-w-0 flex-1">
                  <b className="text-on-surface font-medium block truncate">{event.step}</b>
                  <p className="text-on-surface-variant text-[11px] truncate">{event.message_redacted}</p>
                </div>
              </li>
            ))}
          </ol>
          {events.length > 5 ? (
            <button
              className="text-primary hover:underline text-[11px] font-medium cursor-pointer block"
              onClick={() => {
                if (session) {
                  console.navigate({ view: "infrastructure", tab: "bootstrap", session: session.id });
                }
              }}
              type="button"
            >
              Open full bootstrap details
            </button>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

function EventTimeline({ events }: { events: TimelineEvent[] }) {
  return (
    <ol className="eventTimeline space-y-2 text-xs">
      {events.map((event) => (
        <li key={event.id} className="flex items-start gap-2 bg-surface-container/40 p-2 rounded-lg border border-outline-variant/10">
          <span className="w-2 h-2 rounded-full bg-primary mt-1 shrink-0" />
          <div className="min-w-0 flex-1">
            <b className="text-on-surface font-medium block truncate">{event.step}</b>
            <p className="text-on-surface-variant text-[11px] truncate">{event.message_redacted}</p>
          </div>
        </li>
      ))}
    </ol>
  );
}

function LiveTopology({ console, environment, facts }: { console: ConsoleController; environment?: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]>["environments"][number]; facts: NonNullable<ReturnType<typeof useInfrastructureData>["data"]["facts"]> }) {
  if (!environment) return <div className="p-4 bg-surface-container-low rounded-2xl border border-outline-variant/15 text-xs text-on-surface-variant">Choose the current environment to view live facts.</div>;
  const connections = console.state.services.flatMap((service) => (service.configuration?.bindings ?? []).map((binding) => ({ source: service.name, target: binding.target_service_key, kind: binding.kind, route: binding.path || binding.env_prefix || "" })));
  const exposures = console.state.deployments.filter((deployment) => deployment.environment_id === environment.id && deployment.exposure_spec);
  return (
    <div className="space-y-6">
      <LiveTopologyCanvas console={console} environment={environment} facts={facts} />

      <section aria-label="Connections and exposure" className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <div className="p-5 bg-surface-container-low/90 backdrop-blur-md rounded-2xl border border-outline-variant/15 space-y-3 shadow-sm">
          <h4 className="font-headline-md text-sm font-bold text-on-surface">Service Connections</h4>
          {connections.length ? (
            <ul className="space-y-2 text-xs">
              {connections.map((connection, index) => (
                <li key={`${connection.source}:${connection.target}:${index}`} className="flex items-center justify-between bg-surface-container/60 p-2.5 rounded-xl border border-outline-variant/10">
                  <strong className="text-on-surface font-code-md">{connection.source} → {connection.target}</strong>
                  <span className="text-on-surface-variant text-[11px]">{connection.kind}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-xs text-on-surface-variant">No active service connections.</p>
          )}
        </div>

        <div className="p-5 bg-surface-container-low/90 backdrop-blur-md rounded-2xl border border-outline-variant/15 space-y-3 shadow-sm">
          <h4 className="font-headline-md text-sm font-bold text-on-surface">Public Exposures</h4>
          {exposures.length ? (
            <ul className="space-y-2 text-xs">
              {exposures.map((deployment) => (
                <li key={deployment.id} className="flex items-center justify-between bg-surface-container/60 p-2.5 rounded-xl border border-outline-variant/10">
                  <strong className="text-on-surface font-code-md">{deployment.exposure_spec?.hostname}{deployment.exposure_spec?.path}</strong>
                  <span className="text-on-surface-variant text-[11px]">{deployment.service_id}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-xs text-on-surface-variant">No public exposures configured.</p>
          )}
        </div>
      </section>

      <LiveDeploymentBoard console={console} environmentID={environment.id} environmentName={environment.name} />
    </div>
  );
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
  useEffect(() => {
    if (selected && console.state.bootstrapEventsSessionID !== selected.id) {
      void console.actions.loadBootstrapEvents(selected.id);
    }
  }, [console.actions, console.state.bootstrapEventsSessionID, selected]);
  return <div className="bootstrapLayout"><section className="bootstrapInventory"><div className="sectionHeading"><div><p className="eyebrow">Connection history</p><h2>Bootstrap sessions</h2><p>Durable, project-scoped Cloud sessions. Credentials and plaintext commands are never reconstructed here.</p></div></div>{console.state.sessions.length ? <ul>{console.state.sessions.map((session) => { const state = bootstrapLifecycleStatus(session.status); return <li key={session.id}><button aria-pressed={selected?.id === session.id} onClick={() => { console.navigate({ session: session.id }); void console.actions.loadBootstrapEvents(session.id); }} type="button"><span><strong>{session.public_host || session.id}</strong><small>{session.role} · attempt {session.attempt_count ?? "?"}/{session.max_attempts ?? "?"}</small><small>{session.id}</small></span><StatusBadge label={state === "Unknown" ? session.status : state} value={session.status} /></button></li>; })}</ul> : <Empty title="No bootstrap sessions" text="Use Connect Server to generate the first one-time bootstrap command." />}</section><section className="bootstrapDetail"><div className="detailHeading"><div><p className="eyebrow">Factual server lifecycle</p><h2>{lifecycle.runtime?.name || lifecycle.session?.public_host || "Connect a server"}</h2></div><StatusBadge label={lifecycle.status} value={lifecycle.status === "Connecting" ? "bootstrapping" : lifecycle.status} /></div><ol aria-label="Server connection progress" className="bootstrapLifecycleRail">{["Waiting", "Connecting", "Bootstrapping", "Ready"].map((state) => <li aria-current={lifecycle.status === state ? "step" : undefined} data-state={lifecycle.status === state ? "current" : "idle"} key={state}><StatusBadge label={state} value={state === "Connecting" || state === "Bootstrapping" ? "bootstrapping" : state} /></li>)}</ol><ServerLifecycleCard console={console} lifecycle={lifecycle} />{selected ? <section className="bootstrapSessionDetail" aria-labelledby="selected-bootstrap-heading"><div className="sectionHeading"><div><p className="eyebrow">Selected session</p><h3 id="selected-bootstrap-heading">{selected.id}</h3></div><StatusBadge value={selected.status} /></div><dl className="evidenceGrid"><Fact label="Attempt" value={`${selected.attempt_count ?? "Unknown"}/${selected.max_attempts ?? "Unknown"}`} /><Fact label="Progress" value={progress.percent === null ? progress.label : `${progress.percent}% · ${progress.label}`} /><Fact label="Next step" value={selected.checkpoint ? `Step ${selected.checkpoint.next_step_index}` : "Not reported"} /><Fact label="Failure" value={selected.last_failure_message_redacted || selected.last_failure_code || "None reported"} /></dl><EventTimeline events={console.state.bootstrapEventsSessionID === selected.id ? console.state.bootstrapEvents : []} />{['failed', 'dead_letter'].includes(selected.status) ? <button className="primary" onClick={() => console.actions.retryBootstrap(selected.id, onReload)} type="button">Retry bootstrap</button> : null}</section> : null}</section></div>;
}

export function BootstrapDialog({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated: () => Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [method, setMethod] = useState("command");
  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);
  return (
    <dialog
      aria-describedby="bootstrap-description"
      aria-labelledby="bootstrap-title"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
            Topology · Connect Server
          </span>
          <h2 id="bootstrap-title" className="font-headline-md text-xl font-bold text-on-surface">
            Connect Server
          </h2>
          <p id="bootstrap-description" className="font-body-md text-xs text-on-surface-variant mt-1">
            Generate a one-time command, run it on the VPS, then follow progress until Ready.
          </p>
        </div>
        <button
          aria-label="Close connect server dialog"
          autoFocus
          className="p-2 min-h-[40px] min-w-[40px] flex items-center justify-center text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      <ol aria-label="Connect Server steps" className="grid grid-cols-3 gap-2 bg-surface-container p-3 rounded-xl border border-outline-variant/15 text-xs">
        <li className="flex items-center gap-2 text-primary font-bold">
          <span className="w-5 h-5 rounded-full bg-primary text-on-primary flex items-center justify-center text-[11px]">1</span>
          <span>Generate command</span>
        </li>
        <li className="flex items-center gap-2 text-on-surface-variant">
          <span className="w-5 h-5 rounded-full bg-surface-container-highest flex items-center justify-center text-[11px]">2</span>
          <span>Run on VPS</span>
        </li>
        <li className="flex items-center gap-2 text-on-surface-variant">
          <span className="w-5 h-5 rounded-full bg-surface-container-highest flex items-center justify-center text-[11px]">3</span>
          <span>Wait for Ready</span>
        </li>
      </ol>

      <form className="space-y-4" onSubmit={(event) => { void console.actions.addServer(event, onCreated); onClose(); }}>
        <div className="space-y-1.5">
          <label className="text-xs font-label-sm text-on-surface-variant block">Role</label>
          <select aria-label="Role" className="field min-h-[40px]" defaultValue="first_server" name="role" required>
            <option value="first_server">First server</option>
            <option value="worker">Worker</option>
          </select>
        </div>

        <div className="space-y-1.5">
          <label className="text-xs font-label-sm text-on-surface-variant block">Server IP or hostname</label>
          <input aria-label="Server IP or hostname" autoComplete="off" className="field min-h-[40px]" name="public_host" placeholder="203.0.113.10" required spellCheck={false} />
        </div>

        <fieldset className="space-y-3 bg-surface-container/60 p-4 rounded-xl border border-outline-variant/15">
          <legend className="text-xs font-label-sm font-bold text-on-surface uppercase tracking-wider px-1">Bootstrap Method</legend>
          <label className="flex items-start gap-3 p-3 bg-surface-container-high rounded-xl border border-outline-variant/20 cursor-pointer">
            <input aria-label="Run bootstrap command" checked={method === "command"} className="mt-1" name="auth_method" onChange={() => setMethod("command")} type="radio" value="command" />
            <div>
              <strong className="text-xs text-on-surface block font-semibold">Run bootstrap command</strong>
              <small className="text-[11px] text-on-surface-variant block mt-0.5">Recommended. Scoped one-time execution on the server.</small>
            </div>
          </label>

          <details className="text-xs text-on-surface-variant space-y-3 pt-2">
            <summary className="cursor-pointer font-medium hover:text-on-surface min-h-[40px] min-w-[40px] flex items-center">Advanced: Bootstrap over SSH</summary>
            <div className="space-y-3 pt-2">
              <label className="flex items-start gap-3 p-3 bg-surface-container-high rounded-xl border border-outline-variant/20 cursor-pointer">
                <input aria-label="SSH password" checked={method === "password"} className="mt-1" name="auth_method" onChange={() => setMethod("password")} type="radio" value="password" />
                <div>
                  <strong className="text-xs text-on-surface block font-semibold">SSH Password</strong>
                  <small className="text-[11px] text-on-surface-variant block mt-0.5">Requested again only after mutation review.</small>
                </div>
              </label>
              <label className="flex items-start gap-3 p-3 bg-surface-container-high rounded-xl border border-outline-variant/20 cursor-pointer">
                <input aria-label="SSH private key" checked={method === "private_key"} className="mt-1" name="auth_method" onChange={() => setMethod("private_key")} type="radio" value="private_key" />
                <div>
                  <strong className="text-xs text-on-surface block font-semibold">SSH Private Key</strong>
                  <small className="text-[11px] text-on-surface-variant block mt-0.5">Uses verified known_hosts workflow.</small>
                </div>
              </label>
              {method !== "command" ? (
                <div className="grid grid-cols-2 gap-3 pt-2">
                  <div className="space-y-1">
                    <label className="text-[11px] font-label-sm text-on-surface-variant block">SSH port</label>
                    <input aria-label="SSH port" className="field" defaultValue="22" inputMode="numeric" max="65535" min="1" name="ssh_port" required type="number" />
                  </div>
                  <div className="space-y-1">
                    <label className="text-[11px] font-label-sm text-on-surface-variant block">SSH username</label>
                    <input aria-label="SSH username" autoComplete="username" className="field" defaultValue="root" name="ssh_username" required spellCheck={false} />
                  </div>
                </div>
              ) : null}
            </div>
          </details>
        </fieldset>

        <p className="text-[11px] text-on-surface-variant bg-surface-container/40 p-3 rounded-xl border border-outline-variant/10">
          The issued token expires and is scoped to this reviewed session.
        </p>

        <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
          <Button className="min-h-[40px] min-w-[40px]" onClick={onClose} size="sm" type="button" variant="outline">
            Cancel
          </Button>
          <Button className="min-h-[40px] min-w-[40px]" size="sm" type="submit" variant="primary">
            Generate bootstrap command
          </Button>
        </div>
      </form>
    </dialog>
  );
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
