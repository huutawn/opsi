"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { ApplicationWizard } from "@/features/applications/application-wizard";
import { BuildJobFacts, BuildRecordFacts, Fact } from "@/features/applications/build-facts";
import type { ConsoleController } from "@/features/console/types";
import { useServicesData } from "@/features/services/data";
import { acceptedDigest, applicationFacts, buildState, deploymentState, exactSourceSHA, placementLabel, type ApplicationFacts } from "@/features/services/model";
import type { ServiceRecord } from "@/lib/contracts/registry";
import { currentEnvironment } from "@/lib/presentation/infrastructure/model";
import { terminalBuild } from "@/lib/presentation/build";

type DetailTab = "overview" | "source" | "builds" | "runtime";

export function ServicesView({ console }: { console: ConsoleController }) {
  const data = useServicesData(console);
  const addTrigger = useRef<HTMLButtonElement>(null);
  const [wizard, setWizard] = useState<ServiceRecord | true | null>(null);
  const [query, setQuery] = useState("");
  const [placement, setPlacement] = useState("all");
  const [build, setBuild] = useState("all");
  const [deployment, setDeployment] = useState("all");
  const [detailTab, setDetailTab] = useState<DetailTab>("overview");
  const environment = currentEnvironment(data.placement, console.route.environment ?? "");
  const applications = useMemo(() => applicationFacts({
    services: console.state.services,
    bindings: data.bindings,
    installations: data.installations,
    repositories: data.repositories,
    topology: data.topology,
    placement: data.placement,
    buildJobs: data.buildJobs,
    buildRecords: data.builds,
    deployments: data.deployments,
    exposures: data.exposures,
    environmentID: environment?.id,
  }), [console.state.services, data.bindings, data.buildJobs, data.builds, data.deployments, data.exposures, data.installations, data.placement, data.repositories, data.topology, environment?.id]);
  const filtered = applications.filter((facts) => {
    const search = [facts.service.name, facts.service.id, facts.repository?.full_name, facts.binding?.selected_ref, exactSourceSHA(facts)].filter(Boolean).join(" ").toLowerCase();
    return search.includes(query.trim().toLowerCase())
      && (placement === "all" || (placement === "placed") === Boolean(facts.assignments.length))
      && (build === "all" || buildState(facts) === build)
      && (deployment === "all" || deploymentState(facts) === deployment);
  });
  const selected = applications.find((facts) => facts.service.id === console.state.serviceDetail?.id);

  function openDetail(facts: ApplicationFacts, tab: DetailTab = "overview") {
    setDetailTab(tab);
    console.setServiceDetail(facts.service);
  }

  function closeWizard() {
    setWizard(null);
    window.requestAnimationFrame(() => addTrigger.current?.focus());
  }

  return <section className="page servicesPage">
    <PageHeader eyebrow="Project applications" title="Services" description="Source, build, topology, and deployment facts remain separate so an accepted build can be ready while its Application is still Unplaced." action={<button className="primary" onClick={(event) => { addTrigger.current = event.currentTarget; setWizard(true); }} ref={addTrigger} type="button">Add Application</button>} />
    {data.sourceError || data.buildError || data.buildJobsError || data.deploymentError ? <p className="truthCallout" role="status">{[data.sourceError, data.buildError, data.buildJobsError, data.deploymentError].filter(Boolean).join(" ")}</p> : null}
    <div className="serviceFilters" role="search">
      <label><span>Search Applications</span><input autoComplete="off" className="field" onChange={(event) => setQuery(event.target.value)} placeholder="Name, repository, ref, SHA…" type="search" value={query} /></label>
      <label><span>Placement</span><select className="select" onChange={(event) => setPlacement(event.target.value)} value={placement}><option value="all">All placements</option><option value="placed">Placed</option><option value="unplaced">Unplaced</option></select></label>
      <label><span>Build state</span><select className="select" onChange={(event) => setBuild(event.target.value)} value={build}><option value="all">All build states</option><option value="not_built">Not built yet</option>{["pending", "ready", "running", "succeeded", "failed", "cancelled"].map((value) => <option key={value} value={value}>{label(value)}</option>)}</select></label>
      <label><span>Deployment state</span><select className="select" onChange={(event) => setDeployment(event.target.value)} value={deployment}><option value="all">All deployment states</option><option value="not_deployed">Not deployed</option>{["queued", "leased", "pulling", "applying", "waiting_ready", "succeeded", "failed", "cancelled"].map((value) => <option key={value} value={value}>{label(value)}</option>)}</select></label>
    </div>
    {!data.hasLoaded && applications.length === 0 ? <Empty title="Loading Applications…" text="Reading source, build, topology, and deployment facts from Local API." /> : applications.length === 0 ? <Empty action={<button className="primary" onClick={(event) => { addTrigger.current = event.currentTarget; setWizard(true); }} type="button">Add Application</button>} title="No Applications yet" text="Create the first factual Application identity. No build, placement, or deployment starts automatically." /> : filtered.length === 0 ? <Empty title="No matching Applications" text="Clear one or more local presentation filters." /> : <div className="applicationCatalog">{filtered.map((facts) => <ApplicationCard console={console} facts={facts} key={facts.service.id} onBuild={() => reviewBuild(console, data.createBuild, facts.service)} onOpen={(tab) => openDetail(facts, tab)} />)}</div>}
    {selected ? <ApplicationDetail console={console} facts={selected} initialTab={detailTab} key={`${selected.service.id}:${detailTab}`} onBuild={() => reviewBuild(console, data.createBuild, selected.service)} onResume={() => { console.setServiceDetail(null); setWizard(selected.service); }} /> : null}
    {wizard ? <ApplicationWizard console={console} onClose={closeWizard} onCreated={async () => { await Promise.all([data.loadBuildJobs(), data.loadBuilds()]); }} resumeService={wizard === true ? undefined : wizard} /> : null}
  </section>;
}

function ApplicationCard({ console, facts, onBuild, onOpen }: { console: ConsoleController; facts: ApplicationFacts; onBuild: () => void; onOpen: (tab?: DetailTab) => void }) {
  const buildStatus = buildState(facts);
  const deployStatus = deploymentState(facts);
  const digest = acceptedDigest(facts);
  const sha = exactSourceSHA(facts);
  return <article className="applicationCard" data-build-state={buildStatus} data-placement={facts.assignments.length ? "placed" : "unplaced"}>
    <header><div><p className="eyebrow">Application</p><h2>{facts.service.name}</h2><code>{facts.service.id}</code></div><StatusBadge label={facts.assignments.length ? "Placed" : "Unplaced"} value={facts.assignments.length ? "healthy" : "unknown"} /></header>
    <dl className="applicationCardFacts"><Fact label="Placement" value={placementLabel(facts)} /><Fact label="Repository" value={facts.repository?.full_name || (facts.binding ? `Repository ${facts.binding.repository_id}` : "Source binding incomplete")} /><Fact label="Selected ref" value={facts.binding?.selected_ref} mono /><Fact label="Latest exact SHA" value={sha} mono /><div><dt>Latest build</dt><dd><StatusBadge label={buildStatus === "not_built" ? "Not built yet" : label(buildStatus)} value={buildStatus === "succeeded" ? "healthy" : buildStatus === "failed" ? "failed" : ["pending", "ready", "running"].includes(buildStatus) ? "in_progress" : "unknown"} /></dd></div><div><dt>Latest deployment</dt><dd><StatusBadge label={deployStatus === "not_deployed" ? "Not deployed" : label(deployStatus)} value={deployStatus === "succeeded" ? "healthy" : deployStatus === "failed" ? "failed" : deployStatus === "not_deployed" ? "unknown" : "in_progress"} /></dd></div>{digest ? <Fact label="Accepted image digest" value={digest} mono /> : null}<Fact label="Exposure" value={exposure(facts)} /></dl>
    <footer><button className="primary" data-application-id={facts.service.id} onClick={() => onOpen()} type="button">Open</button><button disabled={!facts.binding || Boolean(facts.latestBuildJob && !terminalBuild(facts.latestBuildJob))} onClick={onBuild} type="button">Build</button><button onClick={() => onOpen("source")} type="button">Inspect source</button><button onClick={() => viewDeployment(console, facts)} type="button">{deploymentAction(facts)}</button></footer>
  </article>;
}

function ApplicationDetail({ console, facts, initialTab, onBuild, onResume }: { console: ConsoleController; facts: ApplicationFacts; initialTab: DetailTab; onBuild: () => void; onResume: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [tab, setTab] = useState(initialTab);
  useEffect(() => { const element = dialog.current; element?.showModal(); return () => { if (element?.open) element.close(); }; }, []);
  function close() {
    dialog.current?.close();
    console.setServiceDetail(null);
    window.requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-application-id="${CSS.escape(facts.service.id)}"]`)?.focus());
  }
  return <dialog aria-describedby="applicationDetailDescription" aria-labelledby="applicationDetailTitle" className="detailDrawer applicationDetail" onCancel={(event) => { event.preventDefault(); close(); }} ref={dialog}>
    <header className="detailHeader"><div><p className="eyebrow">Application detail</p><h2 id="applicationDetailTitle">{facts.service.name}</h2><p id="applicationDetailDescription">Factual source, build, topology, and deployment evidence.</p></div><button aria-label="Close Application detail" autoFocus className="iconButton" onClick={close} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></header>
    <div aria-label="Application detail sections" className="applicationDetailTabs" role="tablist">{(["overview", "source", "builds", "runtime"] as DetailTab[]).map((item) => <button aria-selected={tab === item} key={item} onClick={() => setTab(item)} role="tab" type="button">{item === "runtime" ? "Runtime / Deployment" : label(item)}</button>)}</div>
    <div className="applicationDetailBody" role="tabpanel">{tab === "overview" ? <Overview console={console} facts={facts} /> : tab === "source" ? <Source facts={facts} onResume={onResume} /> : tab === "builds" ? <BuildHistory facts={facts} onBuild={onBuild} onResume={onResume} /> : <RuntimeDeployment console={console} facts={facts} />}</div>
    <footer className="applicationDetailActions"><button onClick={close} type="button">Close</button>{tab === "builds" && facts.binding ? <button className="primary" disabled={Boolean(facts.latestBuildJob && !terminalBuild(facts.latestBuildJob))} onClick={onBuild} type="button">Build</button> : null}{tab === "runtime" && (facts.latestDeployment || facts.assignment && facts.latestBuildRecord) ? <button className="primary" onClick={() => viewDeployment(console, facts)} type="button">{deploymentAction(facts)}</button> : null}</footer>
  </dialog>;
}

function Overview({ console, facts }: { console: ConsoleController; facts: ApplicationFacts }) {
  const environment = facts.assignment?.environment_id;
  const runtimeFacts: Array<[string, string | number | undefined]> = [["Port", facts.service.container_port], ["Health path", facts.service.health_path], ["CPU", facts.assignment ? `${facts.assignment.cpu_request_millicores}m` : undefined], ["Memory", facts.assignment ? bytes(facts.assignment.memory_request_bytes) : undefined], ["Exposure", facts.assignment ? label(facts.assignment.exposure.mode) : undefined]];
  return <div className="applicationDetailSections"><section><h3>Identity</h3><dl className="detailFacts"><Fact label="Application ID" value={facts.service.id} mono /><Fact label="Name" value={facts.service.name} /><Fact label="Project" value={console.state.project?.name} /><Fact label="Environment" value={environment ? console.state.foundation.placement?.environments.find((item) => item.id === environment)?.name || environment : undefined} /></dl></section><section><div className="detailSectionHeading"><h3>Placement</h3>{!facts.assignments.length ? <button onClick={() => console.navigate({ view: "infrastructure", tab: "topology", topologyMode: "design", service: facts.service.id })} type="button">Open in Topology</button> : null}</div><dl className="detailFacts"><Fact label="State" value={facts.assignments.length ? "Placed" : "Unplaced"} /><Fact label="Runtime / server" value={placementLabel(facts)} /></dl></section>{runtimeFacts.some(([, value]) => value !== undefined) ? <section><h3>Runtime configuration</h3><dl className="detailFacts">{runtimeFacts.map(([name, value]) => value === undefined ? null : <Fact key={name} label={name} value={value} />)}</dl></section> : null}<section><h3>Independent state</h3><div className="independentState"><State label="Source" state={facts.binding ? "Ready" : "Incomplete"} /><State label="Build" state={buildState(facts) === "not_built" ? "Not built yet" : label(buildState(facts))} /><State label="Topology" state={facts.assignments.length ? "Placed" : "Unplaced"} /><State label="Runtime" state={facts.latestDeployment ? label(deploymentState(facts)) : "Not deployed"} /></div></section></div>;
}

function Source({ facts, onResume }: { facts: ApplicationFacts; onResume: () => void }) {
  if (!facts.binding) return <section className="sourceIncomplete"><StatusBadge label="Source binding incomplete" value="degraded" /><p>The Application identity remains unchanged. Resume the canonical P05A binding flow to supply missing source authority.</p><button className="primary" onClick={onResume} type="button">Resume source binding</button></section>;
  return <div className="applicationDetailSections"><section><h3>Canonical source binding</h3><dl className="detailFacts"><Fact label="GitHub installation / account" value={facts.installation?.account_login || `Installation ${facts.binding.installation_id}`} /><Fact label="Repository" value={facts.repository?.full_name || String(facts.binding.repository_id)} /><Fact label="Selected ref" value={facts.binding.selected_ref} mono /><Fact label="Application root" value={facts.binding.application_root} mono /><Fact label="Build context" value={facts.binding.build_context} mono /><Fact label="Requested Build strategy" value={facts.binding.build_strategy === "buildpack" ? "Cloud Native Buildpacks" : label(facts.binding.build_strategy)} />{facts.binding.dockerfile_path ? <Fact label="Dockerfile path" value={facts.binding.dockerfile_path} mono /> : null}</dl></section><section><h3>Revision identity</h3><dl className="detailFacts"><Fact label="Selected ref" value={facts.binding.selected_ref} mono /><Fact label="Latest built revision" value={exactSourceSHA(facts)} mono /></dl><p className="muted">The selected branch or ref is mutable. Only an exact commit SHA identifies a built revision.</p></section></div>;
}

function BuildHistory({ facts, onBuild, onResume }: { facts: ApplicationFacts; onBuild: () => void; onResume: () => void }) {
  if (!facts.binding && !facts.buildJobs.length && !facts.buildRecords.length) return <section className="sourceIncomplete"><StatusBadge label="Source binding incomplete" value="degraded" /><p>A canonical source binding is required before creating a BuildJob for this Application.</p><button className="primary" onClick={onResume} type="button">Resume source binding</button></section>;
  if (!facts.buildJobs.length && !facts.buildRecords.length) return <div className="emptyBuild"><b>Not built yet</b><p>Source binding is complete, but no BuildJob or accepted BuildRecord exists.</p><button className="primary" onClick={onBuild} type="button">Build</button></div>;
  const matchedRecords = new Set<string>();
  return <div className="buildHistory">{facts.buildJobs.map((job) => { const record = facts.buildRecords.find((item) => item.id === job.build_record_id || item.build.build_job_id === job.id); if (record) matchedRecords.add(record.id); return <section key={job.id}><BuildJobFacts job={job} />{record ? <BuildRecordFacts record={record} /> : null}</section>; })}{facts.buildRecords.filter((record) => !matchedRecords.has(record.id)).map((record) => <section key={record.id}><BuildRecordFacts record={record} /></section>)}</div>;
}

function RuntimeDeployment({ console, facts }: { console: ConsoleController; facts: ApplicationFacts }) {
  const deployment = facts.latestDeployment;
  return <div className="applicationDetailSections"><section><div className="detailSectionHeading"><h3>Topology assignment</h3>{!facts.assignments.length ? <button onClick={() => console.navigate({ view: "infrastructure", tab: "topology", topologyMode: "design", service: facts.service.id })} type="button">Open in Topology</button> : null}</div><dl className="detailFacts"><Fact label="Placement" value={placementLabel(facts)} /><Fact label="Runtime" value={facts.runtime ? `${facts.runtime.name} · ${facts.runtime.type}` : facts.assignment?.runtime_id} /><Fact label="Exposure intent" value={facts.assignment?.exposure.mode ? label(facts.assignment.exposure.mode) : undefined} /></dl></section><section><h3>Latest factual deployment</h3>{deployment ? <><dl className="detailFacts"><Fact label="DeploymentJob" value={deployment.id} mono /><Fact label="Status" value={label(deploymentState(facts))} /><Fact label="Image digest" value={deployment.current_digest || deployment.terminal_result?.current_digest || deployment.desired_digest || deployment.snapshot?.image.digest} mono /><Fact label="Runtime" value={facts.runtime?.name || deployment.runtime_id} /><Fact label="Exposure" value={exposure(facts)} /></dl><button onClick={() => viewDeployment(console, facts)} type="button">View deployment</button></> : <><p className="muted">Not deployed. An accepted BuildRecord does not imply runtime deployment.</p>{facts.assignment && facts.latestBuildRecord ? <button className="primary" onClick={() => viewDeployment(console, facts)} type="button">Review deployment</button> : null}</>}</section></div>;
}

function State({ label: name, state }: { label: string; state: string }) { return <div><span>{name}</span><b>{state}</b></div>; }
function label(value: string) { return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase()); }
function bytes(value: number) { return value % 1073741824 === 0 ? `${value / 1073741824} GiB` : value % 1048576 === 0 ? `${value / 1048576} MiB` : `${value} bytes`; }
function exposure(facts: ApplicationFacts) { return facts.latestExposure?.exposure_spec ? `${facts.latestExposure.exposure_spec.hostname}${facts.latestExposure.exposure_spec.path}` : facts.assignment ? label(facts.assignment.exposure.mode) : "Not reported"; }
function deploymentAction(facts: ApplicationFacts) { return facts.latestDeployment ? "View deployment" : facts.assignment && facts.latestBuildRecord ? "Review deployment" : "Open in Topology"; }
function viewDeployment(console: ConsoleController, facts: ApplicationFacts) { if (facts.latestDeployment) console.navigate({ view: "delivery", tab: "deployments", service: facts.service.id, deployment: facts.latestDeployment.id }); else console.navigate({ view: "infrastructure", tab: "topology", topologyMode: "design", service: facts.service.id }); }
function reviewBuild(console: ConsoleController, createBuild: (service: ServiceRecord, key: string) => Promise<unknown>, service: ServiceRecord) { console.reviewMutation({ project: console.state.project?.name || console.state.project?.id || "", targetType: "BuildJob", targetID: service.id, operation: "build", diff: [`Resolve exact commit from the active source binding for ${service.name}`, "Resolve the canonical build strategy in Cloud", "Publish and accept an immutable BuildRecord only after verification"], risk: "Creates a new canonical BuildJob intent. It does not mutate a prior failed job, place the Application, or deploy it." }, async (key) => { const job = await createBuild(service, key) as { id: string; status: string }; return `BuildJob ${job.id} accepted with factual state ${job.status}.`; }); }
