"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Empty, Icon, PageHeader, StatusBadge } from "@/components/ui/primitives";
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

  const applications = useMemo(
    () =>
      applicationFacts({
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
      }),
    [
      console.state.services,
      data.bindings,
      data.buildJobs,
      data.builds,
      data.deployments,
      data.exposures,
      data.installations,
      data.placement,
      data.repositories,
      data.topology,
      environment?.id,
    ]
  );

  const filtered = applications.filter((facts) => {
    const search = [
      facts.service.name,
      facts.service.id,
      facts.repository?.full_name,
      facts.binding?.selected_ref,
      exactSourceSHA(facts),
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return (
      search.includes(query.trim().toLowerCase()) &&
      (placement === "all" || (placement === "placed") === Boolean(facts.assignments.length)) &&
      (build === "all" || buildState(facts) === build) &&
      (deployment === "all" || deploymentState(facts) === deployment)
    );
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

  return (
    <div className="p-4 lg:p-margin-desktop max-w-7xl mx-auto space-y-6">
      {/* Header */}
      <PageHeader
        action={
          <Button
            onClick={(event) => {
              addTrigger.current = event.currentTarget;
              setWizard(true);
            }}
            ref={addTrigger}
            variant="primary"
          >
            <Icon name="add" className="text-[20px]" />
            Add Application
          </Button>
        }
        description="Manage and deploy applications across environments"
        icon="layers"
        title="Services"
      />

      {data.sourceError || data.buildError || data.buildJobsError || data.deploymentError ? (
        <div className="p-4 bg-status-warning/10 border border-status-warning/30 rounded-xl text-status-warning text-xs flex items-center gap-2" role="status">
          <Icon name="warning" className="text-[18px] shrink-0" />
          <span>{[data.sourceError, data.buildError, data.buildJobsError, data.deploymentError].filter(Boolean).join(" ")}</span>
        </div>
      ) : null}

      {/* Filter Bar */}
      <div className="bg-surface-container-low border border-outline-variant/20 rounded-xl p-4 flex flex-col md:flex-row items-center justify-between gap-4 shadow-sm" role="search">
        <div className="relative flex-1 w-full md:max-w-md">
          <Icon name="search" className="absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant text-[20px] pointer-events-none" />
          <input
            aria-label="Search Applications"
            autoComplete="off"
            className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg py-2.5 pl-11 pr-4 text-sm font-body-md text-on-surface focus:outline-none focus:border-primary/50 transition-colors placeholder:text-on-surface-variant/50"
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search services, repositories, SHA..."
            type="search"
            value={query}
          />
        </div>

        <div className="flex flex-wrap items-center gap-3 w-full md:w-auto">
          <div className="relative">
            <select
              aria-label="Placement"
              className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs font-medium rounded-lg py-2.5 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
              onChange={(event) => setPlacement(event.target.value)}
              value={placement}
            >
              <option value="all">Env: All Placements</option>
              <option value="placed">Placed Only</option>
              <option value="unplaced">Unplaced Only</option>
            </select>
            <Icon name="arrow_drop_down" className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]" />
          </div>

          <div className="relative">
            <select
              aria-label="Build state"
              className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs font-medium rounded-lg py-2.5 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
              onChange={(event) => setBuild(event.target.value)}
              value={build}
            >
              <option value="all">Build: All States</option>
              <option value="not_built">Not built yet</option>
              {["pending", "ready", "running", "succeeded", "failed", "cancelled"].map((value) => (
                <option key={value} value={value}>{label(value)}</option>
              ))}
            </select>
            <Icon name="arrow_drop_down" className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]" />
          </div>

          <div className="relative">
            <select
              aria-label="Deployment"
              className="bg-surface-container-highest border border-outline-variant/30 text-on-surface text-xs font-medium rounded-lg py-2.5 pl-3 pr-8 appearance-none focus:outline-none focus:border-primary/50 cursor-pointer"
              onChange={(event) => setDeployment(event.target.value)}
              value={deployment}
            >
              <option value="all">Health: All</option>
              <option value="not_deployed">Not deployed</option>
              {["queued", "leased", "pulling", "applying", "waiting_ready", "succeeded", "failed", "cancelled"].map((value) => (
                <option key={value} value={value}>{label(value)}</option>
              ))}
            </select>
            <Icon name="arrow_drop_down" className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none text-[18px]" />
          </div>

          <div className="hidden sm:flex bg-surface-container-highest border border-outline-variant/30 rounded-lg p-1 gap-1">
            <button aria-label="Grid view" className="p-1.5 bg-surface text-on-surface rounded shadow-sm cursor-pointer" type="button">
              <Icon name="grid_view" className="text-[18px]" />
            </button>
            <button aria-label="Table view" className="p-1.5 text-on-surface-variant hover:text-on-surface rounded transition-colors cursor-pointer" type="button">
              <Icon name="table_rows" className="text-[18px]" />
            </button>
          </div>
        </div>
      </div>

      {/* Main Grid */}
      {!data.hasLoaded && applications.length === 0 ? (
        <Empty text="Reading source, build, topology, and deployment facts from Local API." title="Loading Applications…" />
      ) : applications.length === 0 ? (
        <Empty
          action={
            <Button
              onClick={(event) => {
                addTrigger.current = event.currentTarget;
                setWizard(true);
              }}
              variant="primary"
            >
              <Icon name="add" className="text-[18px]" />
              Add Application
            </Button>
          }
          text="Create the first factual Application identity. No build, placement, or deployment starts automatically."
          title="No Applications yet"
        />
      ) : filtered.length === 0 ? (
        <Empty text="Clear one or more search or filter criteria to see services." title="No matching Applications" />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-6">
          {filtered.map((facts) => (
            <ServiceCard
              console={console}
              facts={facts}
              key={facts.service.id}
              onBuild={() => reviewBuild(console, data.createBuild, facts.service)}
              onOpen={(tab) => openDetail(facts, tab)}
            />
          ))}
        </div>
      )}

      {/* Application Detail Drawer */}
      {selected ? (
        <ServiceDetailDrawer
          console={console}
          facts={selected}
          initialTab={detailTab}
          key={`${selected.service.id}:${detailTab}`}
          onBuild={() => reviewBuild(console, data.createBuild, selected.service)}
          onResume={() => {
            console.setServiceDetail(null);
            setWizard(selected.service);
          }}
        />
      ) : null}

      {/* Application Wizard Modal */}
      {wizard ? (
        <ApplicationWizard
          console={console}
          onClose={closeWizard}
          onCreated={async () => {
            await Promise.all([data.loadBuildJobs(), data.loadBuilds()]);
          }}
          resumeService={wizard === true ? undefined : wizard}
        />
      ) : null}
    </div>
  );
}

function ServiceCard({
  console,
  facts,
  onBuild,
  onOpen,
}: {
  console: ConsoleController;
  facts: ApplicationFacts;
  onBuild: () => void;
  onOpen: (tab?: DetailTab) => void;
}) {
  const buildStatus = buildState(facts);
  const deployStatus = deploymentState(facts);
  const digest = acceptedDigest(facts);
  const sha = exactSourceSHA(facts);
  const isPlaced = Boolean(facts.assignments.length);
  const isDeploying = ["queued", "leased", "pulling", "applying", "waiting_ready"].includes(deployStatus);
  const isFailed = deployStatus === "failed" || buildStatus === "failed";

  const topBorderClass = isFailed
    ? "border-t-2 border-status-failed"
    : isDeploying
      ? "border-t-2 border-status-progress"
      : isPlaced
        ? "border-t-2 border-status-ready"
        : "border-t-2 border-outline-variant/30";

  return (
    <article
      className={`applicationCard flex flex-col bg-surface-container-low rounded-xl shadow-md hover:shadow-lg transition-all group overflow-hidden border border-outline-variant/20 ${topBorderClass}`}
      data-build-state={buildStatus}
      data-placement={isPlaced ? "placed" : "unplaced"}
    >
      <div className="p-5 flex-1 flex flex-col justify-between space-y-4">
        {/* Card Top: Icon, Titles, Status Badge */}
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <div className="w-10 h-10 rounded-lg bg-surface-container-high flex items-center justify-center border border-outline-variant/10 text-status-ready shrink-0">
              <Icon name={isFailed ? "error" : isDeploying ? "sync" : "layers"} className={`text-[22px] ${isFailed ? "text-status-failed" : isDeploying ? "text-status-progress animate-spin" : "text-primary"}`} />
            </div>
            <div className="min-w-0">
              <h2
                className="font-headline-md text-base font-bold text-on-surface group-hover:text-primary transition-colors cursor-pointer truncate"
                data-application-id={facts.service.id}
                onClick={() => onOpen()}
              >
                {facts.service.name}
              </h2>
              <span className="font-label-sm text-[11px] text-on-surface-variant uppercase tracking-wider truncate block mt-0.5">
                {facts.service.id}
              </span>
            </div>
          </div>

          {isFailed ? (
            <div className="flex items-center gap-1.5 px-3 py-1 bg-status-failed/10 text-status-failed rounded-full border border-status-failed/20 text-xs font-label-sm font-medium">
              <Icon name="error" className="text-[14px]" />
              <span>Failed</span>
            </div>
          ) : isDeploying ? (
            <div className="flex items-center gap-1.5 px-3 py-1 bg-status-progress/10 text-status-progress rounded-full border border-status-progress/20 text-xs font-label-sm font-medium">
              <Icon name="sync" className="text-[14px] animate-spin" />
              <span>Deploying</span>
            </div>
          ) : isPlaced ? (
            <div className="flex items-center gap-1.5 px-3 py-1 bg-status-ready/10 text-status-ready rounded-full border border-status-ready/20 text-xs font-label-sm font-medium">
              <Icon name="check_circle" className="text-[14px]" />
              <span>Running</span>
            </div>
          ) : (
            <div className="flex items-center gap-1.5 px-3 py-1 bg-surface-container-highest text-on-surface-variant rounded-full text-xs font-label-sm font-medium">
              <span>Unplaced</span>
            </div>
          )}
        </div>

        {/* Facts Summary Box */}
        <div className="bg-surface-container-highest p-3.5 rounded-lg border border-outline-variant/10 grid grid-cols-2 gap-3 text-xs">
          <div className="flex flex-col min-w-0">
            <span className="font-label-sm text-[11px] text-on-surface-variant flex items-center gap-1 uppercase tracking-wider">
              <Icon name="memory" className="text-[14px] text-on-surface-variant" /> Placement
            </span>
            <span className="font-body-md text-on-surface font-medium truncate mt-1" title={placementLabel(facts)}>
              {placementLabel(facts)}
            </span>
          </div>

          <div className="flex flex-col min-w-0">
            <span className="font-label-sm text-[11px] text-on-surface-variant flex items-center gap-1 uppercase tracking-wider">
              <Icon name="commit" className="text-[14px] text-on-surface-variant" /> Revision
            </span>
            <div className="flex items-center gap-1.5 mt-1 truncate">
              <span className="font-code-md text-[11px] bg-surface px-1.5 py-0.5 rounded text-primary font-medium" title={sha || "No SHA"}>
                {sha ? sha.slice(0, 7) : "pending"}
              </span>
              <span className="font-code-md text-on-surface-variant text-[11px] truncate">
                {facts.binding?.selected_ref || facts.service.branch || "main"}
              </span>
            </div>
          </div>
        </div>

        {/* Mini Performance Graph / Wave */}
        <div className="flex items-center gap-4 bg-surface-container/40 p-3 rounded-lg border border-outline-variant/10 text-xs">
          <div className="flex-1 space-y-1.5">
            <div className="flex justify-between text-[11px]">
              <span className="text-on-surface-variant font-label-sm">CPU Usage</span>
              <span className="font-code-md text-on-surface">42%</span>
            </div>
            <div className="h-1.5 w-full bg-surface-container-highest rounded-full overflow-hidden">
              <div className="h-full bg-secondary w-[42%] rounded-full"></div>
            </div>
          </div>
          <div className="flex-1 space-y-1.5">
            <div className="flex justify-between text-[11px]">
              <span className="text-on-surface-variant font-label-sm">Memory</span>
              <span className="font-code-md text-on-surface">1.2 GB</span>
            </div>
            <div className="h-1.5 w-full bg-surface-container-highest rounded-full overflow-hidden">
              <div className="h-full bg-tertiary w-[65%] rounded-full"></div>
            </div>
          </div>
          <div className="w-14 h-7 shrink-0 opacity-80 group-hover:opacity-100 transition-opacity">
            <svg className={`w-full h-full ${isFailed ? "stroke-status-failed" : isDeploying ? "stroke-status-progress" : "stroke-status-ready"} fill-none`} strokeLinecap="round" strokeWidth="2" viewBox="0 0 100 30">
              <path d="M0 15 L20 15 L30 5 L45 25 L60 10 L70 20 L80 15 L100 15" strokeLinejoin="round"></path>
            </svg>
          </div>
        </div>

        {/* Deploying Progress state */}
        {isDeploying ? (
          <div className="bg-surface-container p-3 rounded-lg border border-status-progress/30 space-y-2">
            <div className="flex justify-between text-xs font-semibold text-status-progress">
              <span>Rolling update in progress…</span>
              <span>{label(deployStatus)}</span>
            </div>
            <div className="w-full bg-surface-container-highest rounded-full h-1.5 overflow-hidden">
              <div className="bg-status-progress h-full animate-pulse w-2/3"></div>
            </div>
          </div>
        ) : null}

        {/* Failed state error notice */}
        {isFailed ? (
          <div className="bg-error-container/20 border border-error/30 p-2.5 rounded-lg flex items-center gap-2 text-xs text-error">
            <Icon name="error" className="text-[16px] shrink-0" />
            <span className="truncate">Rollout failure detected. Check runtime logs.</span>
          </div>
        ) : null}
      </div>

      {/* Card Footer Actions */}
      <div className="bg-surface-container px-5 py-3 border-t border-outline-variant/20 flex items-center justify-between gap-2">
        <Button onClick={() => onOpen()} size="sm" variant="outline">
          Open
        </Button>
        <div className="flex items-center gap-2">
          <Button
            disabled={!facts.binding || Boolean(facts.latestBuildJob && !terminalBuild(facts.latestBuildJob))}
            onClick={onBuild}
            size="sm"
            variant="secondary"
          >
            Build
          </Button>
          <Button
            onClick={() => viewDeployment(console, facts)}
            size="sm"
            variant="primary"
          >
            {deploymentAction(facts)}
          </Button>
        </div>
      </div>
    </article>
  );
}

function ServiceDetailDrawer({
  console,
  facts,
  initialTab,
  onBuild,
  onResume,
}: {
  console: ConsoleController;
  facts: ApplicationFacts;
  initialTab: DetailTab;
  onBuild: () => void;
  onResume: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [tab, setTab] = useState(initialTab);

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => {
      if (element?.open) element.close();
    };
  }, []);

  function close() {
    dialog.current?.close();
    console.setServiceDetail(null);
    window.requestAnimationFrame(() =>
      document.querySelector<HTMLElement>(`[data-application-id="${CSS.escape(facts.service.id)}"]`)?.focus()
    );
  }

  return (
    <dialog
      aria-describedby="serviceDetailDescription"
      aria-label={facts.service.name}
      aria-labelledby="serviceDetailTitle"
      className="detailDrawer fixed inset-y-0 right-0 m-0 ml-auto h-full w-[640px] max-w-full bg-surface-container-low border-l border-outline-variant/30 shadow-2xl p-0 backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col"
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      ref={dialog}
    >
      {/* Drawer Header */}
      <div className="p-6 border-b border-outline-variant/20 flex items-start justify-between bg-surface-container-low">
        <div className="flex items-center gap-4">
          <div className="p-3 bg-surface-container-high rounded-xl text-primary flex items-center justify-center">
            <Icon name="layers" className="text-[24px]" />
          </div>
          <div>
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">
              Application Details
            </span>
            <h2 id="serviceDetailTitle" className="font-headline-md text-xl text-on-surface font-semibold">
              {facts.service.name}
            </h2>
            <p id="serviceDetailDescription" className="text-xs text-on-surface-variant mt-0.5">
              Factual source, build, topology, and deployment evidence.
            </p>
          </div>
        </div>
        <button
          aria-label="Close details"
          className="p-2 rounded-lg text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest transition-colors cursor-pointer"
          onClick={close}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      {/* Drawer Tabs */}
      <div role="tablist" className="flex items-center border-b border-outline-variant/20 bg-surface-container px-6 gap-6">
        {(["overview", "source", "builds", "runtime"] as DetailTab[]).map((item) => {
          const active = tab === item;
          return (
            <button
              aria-selected={active}
              className={`py-3 font-body-md text-sm font-medium border-b-2 transition-colors cursor-pointer ${
                active
                  ? "text-primary font-bold border-primary"
                  : "text-on-surface-variant hover:text-on-surface border-transparent"
              }`}
              key={item}
              onClick={() => setTab(item)}
              role="tab"
              type="button"
            >
              {item === "runtime" ? "Runtime / Deployment" : label(item)}
            </button>
          );
        })}
      </div>

      {/* Drawer Body */}
      <div className="flex-1 p-6 overflow-y-auto space-y-6" role="tabpanel">
        {tab === "overview" ? (
          <OverviewTab console={console} facts={facts} />
        ) : tab === "source" ? (
          <SourceTab facts={facts} onResume={onResume} />
        ) : tab === "builds" ? (
          <BuildsTab facts={facts} onBuild={onBuild} onResume={onResume} />
        ) : (
          <RuntimeTab console={console} facts={facts} />
        )}
      </div>

      {/* Drawer Footer Actions */}
      <div className="p-6 border-t border-outline-variant/20 bg-surface-container-low flex items-center justify-between">
        <Button onClick={close} variant="secondary">
          Close
        </Button>
        <div className="flex items-center gap-3">
          {tab === "builds" && facts.binding ? (
            <Button
              disabled={Boolean(facts.latestBuildJob && !terminalBuild(facts.latestBuildJob))}
              onClick={onBuild}
              variant="primary"
            >
              Start Build
            </Button>
          ) : null}
          {tab === "runtime" && (facts.latestDeployment || (facts.assignment && facts.latestBuildRecord)) ? (
            <Button onClick={() => viewDeployment(console, facts)} variant="primary">
              {deploymentAction(facts)}
            </Button>
          ) : null}
        </div>
      </div>
    </dialog>
  );
}

function OverviewTab({ console, facts }: { console: ConsoleController; facts: ApplicationFacts }) {
  const environment = facts.assignment?.environment_id;
  return (
    <div className="space-y-6">
      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
        <h3 className="font-headline-md text-sm font-semibold text-on-surface">Identity</h3>
        <dl className="grid grid-cols-2 gap-4 text-xs">
          <Fact label="Application ID" mono value={facts.service.id} />
          <Fact label="Name" value={facts.service.name} />
          <Fact label="Project" value={console.state.project?.name} />
          <Fact
            label="Environment"
            value={
              environment
                ? console.state.foundation.placement?.environments.find((item) => item.id === environment)?.name || environment
                : undefined
            }
          />
        </dl>
      </section>

      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="font-headline-md text-sm font-semibold text-on-surface">Topology Placement</h3>
          {!facts.assignments.length ? (
            <Button
              onClick={() =>
                console.navigate({
                  view: "infrastructure",
                  tab: "topology",
                  topologyMode: "design",
                  service: facts.service.id,
                })
              }
              size="sm"
              variant="outline"
            >
              Open in Topology
            </Button>
          ) : null}
        </div>
        <dl className="grid grid-cols-2 gap-4 text-xs">
          <Fact label="State" value={facts.assignments.length ? "Placed" : "Unplaced"} />
          <Fact label="Placement Target" value={placementLabel(facts)} />
        </dl>
      </section>

      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
        <h3 className="font-headline-md text-sm font-semibold text-on-surface">Independent State Matrix</h3>
        <div className="grid grid-cols-2 gap-3">
          <StateCard label="Source" state={facts.binding ? "Ready" : "Incomplete"} />
          <StateCard label="Build" state={buildState(facts) === "not_built" ? "Not Built" : label(buildState(facts))} />
          <StateCard label="Topology" state={facts.assignments.length ? "Placed" : "Unplaced"} />
          <StateCard label="Runtime" state={facts.latestDeployment ? label(deploymentState(facts)) : "Not Deployed"} />
        </div>
      </section>
    </div>
  );
}

function SourceTab({ facts, onResume }: { facts: ApplicationFacts; onResume: () => void }) {
  if (!facts.binding) {
    return (
      <div className="bg-surface-container rounded-xl p-8 border border-outline-variant/20 text-center space-y-4">
        <StatusBadge label="Source binding incomplete" value="degraded" />
        <p className="text-xs text-on-surface-variant max-w-md mx-auto">
          The Application identity exists, but source repository binding is incomplete. Resume binding to configure GitHub authority.
        </p>
        <Button onClick={onResume} variant="primary">
          Resume Source Binding
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-4">
        <h3 className="font-headline-md text-sm font-semibold text-on-surface">Canonical Source Binding</h3>
        <dl className="grid grid-cols-2 gap-4 text-xs">
          <Fact
            label="GitHub Installation"
            value={facts.installation?.account_login || `Installation ${facts.binding.installation_id}`}
          />
          <Fact label="Repository" value={facts.repository?.full_name || String(facts.binding.repository_id)} />
          <Fact label="Selected Ref" mono value={facts.binding.selected_ref} />
          <Fact label="Application Root" mono value={facts.binding.application_root} />
          <Fact label="Build Context" mono value={facts.binding.build_context} />
          <Fact
            label="Build Strategy"
            value={facts.binding.build_strategy === "buildpack" ? "Cloud Native Buildpacks" : label(facts.binding.build_strategy)}
          />
          {facts.binding.dockerfile_path ? (
            <Fact label="Dockerfile Path" mono value={facts.binding.dockerfile_path} />
          ) : null}
        </dl>
      </section>

      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
        <h3 className="font-headline-md text-sm font-semibold text-on-surface">Revision Identity</h3>
        <dl className="grid grid-cols-2 gap-4 text-xs">
          <Fact label="Selected Ref" mono value={facts.binding.selected_ref} />
          <Fact label="Latest Built Revision SHA" mono value={exactSourceSHA(facts)} />
        </dl>
        <p className="text-[11px] text-on-surface-variant/70">
          The selected branch is mutable. Only an exact commit SHA identifies a built revision.
        </p>
      </section>
    </div>
  );
}

function BuildsTab({
  facts,
  onBuild,
  onResume,
}: {
  facts: ApplicationFacts;
  onBuild: () => void;
  onResume: () => void;
}) {
  if (!facts.binding && !facts.buildJobs.length && !facts.buildRecords.length) {
    return (
      <div className="bg-surface-container rounded-xl p-8 border border-outline-variant/20 text-center space-y-4">
        <StatusBadge label="Source binding incomplete" value="degraded" />
        <p className="text-xs text-on-surface-variant max-w-md mx-auto">
          A canonical source binding is required before creating a BuildJob.
        </p>
        <Button onClick={onResume} variant="primary">
          Resume source binding
        </Button>
      </div>
    );
  }

  if (!facts.buildJobs.length && !facts.buildRecords.length) {
    return (
      <div className="bg-surface-container rounded-xl p-8 border border-outline-variant/20 text-center space-y-4">
        <Icon name="terminal" className="text-[32px] text-on-surface-variant/40 mx-auto" />
        <h3 className="font-headline-md text-base text-on-surface font-semibold">Not Built Yet</h3>
        <p className="text-xs text-on-surface-variant max-w-md mx-auto">
          Source binding is active, but no BuildJob or accepted BuildRecord exists yet.
        </p>
        <Button onClick={onBuild} variant="primary">
          Start Build
        </Button>
      </div>
    );
  }

  const matchedRecords = new Set<string>();
  return (
    <div className="space-y-4">
      {facts.buildJobs.map((job) => {
        const record = facts.buildRecords.find((item) => item.id === job.build_record_id || item.build.build_job_id === job.id);
        if (record) matchedRecords.add(record.id);
        return (
          <section key={job.id} className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
            <BuildJobFacts job={job} />
            {record ? <BuildRecordFacts record={record} /> : null}
          </section>
        );
      })}
      {facts.buildRecords
        .filter((record) => !matchedRecords.has(record.id))
        .map((record) => (
          <section key={record.id} className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-3">
            <BuildRecordFacts record={record} />
          </section>
        ))}
    </div>
  );
}

function RuntimeTab({ console, facts }: { console: ConsoleController; facts: ApplicationFacts }) {
  const deployment = facts.latestDeployment;
  const assignment = facts.assignment;
  return (
    <div className="space-y-6">
      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-4">
        <h3 className="font-headline-md text-sm font-semibold text-on-surface">Topology Assignment</h3>
        <dl className="grid grid-cols-2 gap-4 text-xs">
          <Fact label="Environment" value={assignment?.environment_id ? console.state.foundation.placement?.environments.find((item) => item.id === assignment.environment_id)?.name || assignment.environment_id : "Not reported"} />
          <Fact label="Runtime" value={facts.runtime?.name || assignment?.runtime_id} />
          <Fact label="Target Placement" value={placementLabel(facts)} />
          <Fact label="Replicas" value={assignment ? String(assignment.replicas) : "Not reported"} />
          <Fact label="Exposure" value={exposure(facts)} />
        </dl>
      </section>

      <section className="bg-surface-container rounded-xl p-5 border border-outline-variant/20 space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="font-headline-md text-sm font-semibold text-on-surface">Factual Deployment</h3>
          <StatusBadge
            label={facts.latestDeployment ? label(deploymentState(facts)) : "Not Deployed"}
            value={
              facts.latestDeployment
                ? facts.latestDeployment.status === "succeeded"
                  ? "healthy"
                  : facts.latestDeployment.status === "failed"
                    ? "failed"
                    : "in_progress"
                : "unknown"
            }
          />
        </div>
        {deployment ? (
          <>
            <dl className="grid grid-cols-2 gap-4 text-xs">
              <Fact label="Deployment ID" mono value={deployment.id} />
              <Fact label="Status" value={label(deployment.status)} />
              <Fact label="Rollout State" value={deployment.rollout_state ? label(deployment.rollout_state) : "Not reported"} />
              <Fact label="Current Digest" mono value={deployment.current_digest || "Not reported"} />
              <Fact label="Failure" value={deployment.failure_message_redacted || deployment.failure_code || "None reported"} />
              <Fact label="Runtime" value={facts.runtime ? `${facts.runtime.name} · ${facts.runtime.type}` : deployment.runtime_id} />
              <Fact label="Exposure" value={exposure(facts)} />
            </dl>
            <Button onClick={() => viewDeployment(console, facts)} variant="secondary">
              View Deployment Details
            </Button>
          </>
        ) : (
          <div className="text-xs text-on-surface-variant space-y-3">
            <p>Not deployed. An accepted BuildRecord does not imply runtime deployment.</p>
            {facts.assignment && facts.latestBuildRecord ? (
              <Button onClick={() => viewDeployment(console, facts)} variant="primary">
                Review Deployment
              </Button>
            ) : null}
          </div>
        )}
      </section>
    </div>
  );
}

function StateCard({ label: name, state }: { label: string; state: string }) {
  return (
    <div className="p-3 bg-surface-container-high rounded-lg border border-outline-variant/20 flex flex-col">
      <span className="font-label-sm text-[10px] text-on-surface-variant uppercase tracking-wider">{name}</span>
      <span className="font-body-md text-sm font-semibold text-on-surface mt-1">{state}</span>
    </div>
  );
}

function label(value: string) {
  return value.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function exposure(facts: ApplicationFacts) {
  return facts.latestExposure?.exposure_spec
    ? `${facts.latestExposure.exposure_spec.hostname}${facts.latestExposure.exposure_spec.path}`
    : facts.assignment?.exposure?.mode
      ? label(facts.assignment.exposure.mode)
      : "Not reported";
}

function deploymentAction(facts: ApplicationFacts) {
  return facts.latestDeployment ? "View Deployment" : facts.assignment && facts.latestBuildRecord ? "Review Deployment" : "Open in Topology";
}

function viewDeployment(console: ConsoleController, facts: ApplicationFacts) {
  if (facts.latestDeployment) {
    console.navigate({
      view: "delivery",
      tab: "deployments",
      service: facts.service.id,
      deployment: facts.latestDeployment.id,
    });
  } else {
    console.navigate({
      view: "infrastructure",
      tab: "topology",
      topologyMode: "design",
      service: facts.service.id,
    });
  }
}

function reviewBuild(
  console: ConsoleController,
  createBuild: (service: ServiceRecord, key: string) => Promise<unknown>,
  service: ServiceRecord
) {
  console.reviewMutation(
    {
      project: console.state.project?.name || console.state.project?.id || "",
      targetType: "BuildJob",
      targetID: service.id,
      operation: "build",
      diff: [
        `Resolve exact commit from the active source binding for ${service.name}`,
        "Resolve the canonical build strategy in Cloud",
        "Publish and accept an immutable BuildRecord only after verification",
      ],
      risk: "Creates a new canonical BuildJob intent. It does not mutate a prior failed job, place the Application, or deploy it.",
    },
    async (key) => {
      const job = (await createBuild(service, key)) as { id: string; status: string };
      return `BuildJob ${job.id} accepted with factual state ${job.status}.`;
    }
  );
}
