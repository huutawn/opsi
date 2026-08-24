"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildJob, BuildRecord, GitHubBinding, GitHubInstallation, GitHubRepository, ServiceRecord } from "@/lib/contracts/registry";
import { terminalBuild } from "@/lib/presentation/build";
import { BuildJobFacts, BuildRecordFacts } from "@/features/applications/build-facts";
import { Button, Icon } from "@/components/ui/primitives";

const serviceKeyPattern = "[a-z0-9](?:[a-z0-9_\\-]{0,61}[a-z0-9])?";
type Step = "source" | "application" | "build";
type BuildStrategy = "auto" | "dockerfile" | "buildpack";

export function ApplicationWizard({
  console,
  onClose,
  onCreated,
  resumeService,
}: {
  console: ConsoleController;
  onClose: () => void;
  onCreated?: () => void | Promise<void>;
  resumeService?: ServiceRecord;
}) {
  const client = useMemo(() => new LocalClient(), []);
  const dialog = useRef<HTMLDialogElement>(null);
  const projectID = console.state.project?.id ?? "";
  const [step, setStep] = useState<Step>(resumeService ? "application" : "source");
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
	const [linkedInstallationIDs, setLinkedInstallationIDs] = useState<Set<number>>(() => new Set());
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [bindings, setBindings] = useState<GitHubBinding[]>([]);
  const [services, setServices] = useState<ServiceRecord[]>([]);
  const [installationID, setInstallationID] = useState(0);
  const [repositoryID, setRepositoryID] = useState(0);
  const [selectedRef, setSelectedRef] = useState("");
  const [serviceKey, setServiceKey] = useState(resumeService?.name ?? "");
  const [applicationRoot, setApplicationRoot] = useState(".");
  const [buildContext, setBuildContext] = useState(".");
  const [buildStrategy, setBuildStrategy] = useState<BuildStrategy>("auto");
  const [dockerfilePath, setDockerfilePath] = useState("");
  const [createdService, setCreatedService] = useState<ServiceRecord | null>(resumeService ?? null);
  const [buildJob, setBuildJob] = useState<BuildJob | null>(null);
  const [buildRecord, setBuildRecord] = useState<BuildRecord | null>(null);
  const [loading, setLoading] = useState(true);
  const [buildLoading, setBuildLoading] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => {
      if (element?.open) element.close();
    };
  }, []);

  useEffect(() => {
    let active = true;
    async function load() {
      if (!projectID) return;
      setLoading(true);
      setError("");
      try {
        const [inst, repos, binds, srvs, discovered] = await Promise.all([
          client.githubInstallations(projectID),
          client.githubRepositories(projectID),
          client.githubBindings(projectID),
          client.services(projectID),
			client.githubInstallationDiscovery(projectID),
        ]);
        if (!active) return;
        const linked = inst.installations || [];
        const discoveredByID = new Map((discovered.installations || []).map((item) => [item.installation_id, item]));
        for (const item of linked) discoveredByID.set(item.installation_id, item);
        const availableInstallations = [...discoveredByID.values()];
        setInstallations(availableInstallations);
		setLinkedInstallationIDs(new Set(linked.map((item) => item.installation_id)));
        setRepositories(repos.repositories || []);
        setBindings(binds.bindings || []);
        setServices(srvs.services || []);
        const firstInst = availableInstallations[0];
        const nextInstID = firstInst?.installation_id ?? 0;
        setInstallationID(nextInstID);
        const firstRepo = (repos.repositories || []).find((item) => !nextInstID || item.installation_id === nextInstID);
        setRepositoryID(firstRepo?.repository_id ?? 0);
        setSelectedRef(firstRepo?.default_branch || "main");
        if (!resumeService?.name && firstRepo) {
          const repoName = firstRepo.name || firstRepo.full_name?.split("/").pop() || "app";
          setServiceKey(repoName.toLowerCase().replace(/[^a-z0-9_-]/g, "-").slice(0, 63));
        }
      } catch (cause) {
        if (!active) return;
        setError(cause instanceof Error ? cause.message : "GitHub integration state is unavailable.");
      } finally {
        if (active) setLoading(false);
      }
    }
    void load();
    return () => {
      active = false;
    };
  }, [client, projectID, resumeService]);

  const installation = installations.find((item) => item.installation_id === installationID);
	const installationNeedsClaim = Boolean(installation && !linkedInstallationIDs.has(installation.installation_id));
  const availableRepositories = repositories.filter((item) => !installationID || item.installation_id === installationID);
  const repository = repositories.find((item) => item.repository_id === repositoryID);
  const existingService = services.find((item) => item.name === serviceKey);
  const existingBinding = bindings.find((item) => item.service_key === serviceKey);
  const alreadyBound = existingBinding && existingService;
  const resumeBinding = Boolean((existingService || resumeService) && !existingBinding);
  const keyConflict =
    serviceKey &&
    services.some((item) => item.name === serviceKey && item.id !== (existingService?.id || resumeService?.id))
      ? "An Application with this name already exists in this project."
      : "";

  function chooseInstallation(value: string) {
    const nextID = Number(value);
    const first = repositories.find((item) => item.installation_id === nextID);
    setInstallationID(nextID);
    setRepositoryID(first?.repository_id ?? 0);
    setSelectedRef(first?.default_branch || "main");
    if (!serviceKey && first) {
      const repoName = first.name || first.full_name?.split("/").pop() || "app";
      setServiceKey(repoName.toLowerCase().replace(/[^a-z0-9_-]/g, "-").slice(0, 63));
    }
  }

  function chooseRepository(value: string) {
    const selected = repositories.find((item) => item.repository_id === Number(value));
    setRepositoryID(selected?.repository_id ?? 0);
    setSelectedRef(selected?.default_branch || "main");
    if (!serviceKey && selected) {
      const repoName = selected.name || selected.full_name?.split("/").pop() || "app";
      setServiceKey(repoName.toLowerCase().replace(/[^a-z0-9_-]/g, "-").slice(0, 63));
    }
  }

  function discoverGitHub() {
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "GitHub installation",
		targetID: "discover",
		operation: "discover",
		diff: ["Open GitHub to discover installations available to the signed-in account"],
		risk: "Starts GitHub authorization; no installation, repository, application, build, or deployment is changed yet.",
      },
      async (key) => {
		const started = await client.startGitHubInstallationDiscovery(projectID, key);
        window.location.assign(started.authorization_url);
		return "GitHub installation discovery started.";
      }
    );
    onClose();
  }

	function claimDiscoveredInstallation() {
		if (!installation || !installationNeedsClaim) return;
		const installationLabel = installation.account_login || `Installation ${installation.installation_id}`;
		console.reviewMutation(
			{
				project: console.state.project?.name || projectID,
				targetType: "GitHub installation",
				targetID: installationLabel,
				operation: "connect",
				diff: [`Connect GitHub installation for ${installationLabel}`],
				risk: "GitHub verifies the signed-in identity and installation access before repositories are synced.",
			},
			async (key) => {
				const started = await client.startGitHubInstallationClaim(projectID, installation.installation_id, key);
				window.location.assign(started.authorization_url);
				return "GitHub installation connection started.";
			},
		);
		onClose();
	}

  function reviewApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!repository || !repositoryUsable(repository) || keyConflict) return;
    if (alreadyBound && existingService) {
      void openBuild(existingService);
      return;
    }
    const form = new FormData(event.currentTarget);
    const configPath = String(form.get("config_path") || ".opsi/opsi-cd.yaml");
    const repoURL = `https://github.com/${repository.full_name}`;
    const serviceBody = {
      name: serviceKey,
      type: "application",
      source_type: "git",
      repo_url: repoURL,
      branch: selectedRef,
      build_method: buildStrategy,
      build_context: buildContext,
      ...(dockerfilePath ? { dockerfile: dockerfilePath } : {}),
    };
    const needsClaim = repository.claim_status === "available";
    const strategyLabel =
      buildStrategy === "buildpack" ? "Buildpacks" : buildStrategy === "dockerfile" ? `Dockerfile ${dockerfilePath}` : "Automatic";
    const mutations = [
      ...(needsClaim ? [`Claim repository ${repository.full_name} for this project`] : []),
      ...(resumeBinding ? [] : [`Create Application ${serviceKey}: ${repository.full_name}@${selectedRef}, root ${applicationRoot}, runtime Web service`]),
      `Bind source: context ${buildContext}, build ${strategyLabel}`,
    ];
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "application",
        targetID: serviceKey,
        operation: "create",
        diff: mutations,
        risk: "Creates source identity only. The Application remains Unplaced; no build, placement, topology apply, or deployment starts automatically.",
      },
      async (key) => {
        if (needsClaim) await client.claimGitHubRepository(projectID, repository.repository_id, key);
        const created = existingService ?? (await client.createService(projectID, serviceBody, key));
        await client.createGitHubBinding(
          projectID,
          {
            service_id: created.id,
            repository_id: repository.repository_id,
            service_key: serviceKey,
            config_path: configPath,
            selected_ref: selectedRef,
            application_root: applicationRoot,
            build_context: buildContext,
            build_strategy: buildStrategy,
            ...(buildStrategy === "dockerfile" ? { dockerfile_path: dockerfilePath } : {}),
          },
          key
        );
        setCreatedService(created);
        setServices((current) => (current.some((item) => item.id === created.id) ? current : [...current, created]));
        setStep("build");
        await Promise.all([console.actions.load(), onCreated?.()]);
        await loadBuildFacts(created);
        return `Application ${serviceKey} ${resumeBinding ? "source binding resumed" : "created and GitHub bound"}. Placement is Unplaced; no build or deployment started.`;
      }
    );
  }

  async function openBuild(service: ServiceRecord) {
    setCreatedService(service);
    setStep("build");
    await loadBuildFacts(service);
  }

  async function loadBuildFacts(service: ServiceRecord) {
    setBuildLoading(true);
    setError("");
    try {
      const [jobs, records] = await Promise.all([
        client.buildJobs(projectID, service.id),
        client.buildRecords(projectID, { serviceKey: service.name }),
      ]);
      setBuildJob(jobs.build_jobs?.[0] ?? null);
      setBuildRecord(records.records?.[0] ?? null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Build state is unavailable.");
    } finally {
      setBuildLoading(false);
    }
  }

  function reviewBuild() {
    if (!createdService) return;
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "BuildJob",
        targetID: createdService.id,
        operation: "build",
        diff: [
          `Resolve exact commit from ${repository?.full_name ?? "bound repository"}@${selectedRef}`,
          "Resolve the canonical build strategy in Cloud",
          "Publish one immutable image digest and accept a BuildRecord only after verification",
        ],
        risk: "Creates a BuildJob only. It does not create a BuildRecord client-side, place the Application, or deploy it.",
      },
      async (key) => {
        const job = await client.createBuildJob(projectID, createdService.id, key);
        setBuildJob(job);
        await loadBuildFacts(createdService);
        return `BuildJob ${job.id} accepted with factual state ${job.status}.`;
      }
    );
  }

  const preview = `kind: Application
metadata:
  name: ${serviceKey || "waiting..."}
  state: Unplaced
spec:
  source:
    repository: ${repository?.full_name || "waiting..."}
    ref: ${selectedRef || "waiting..."}
    root: ${applicationRoot}
  build:
    method: ${buildStrategy === "buildpack" ? "buildpacks" : buildStrategy}
    context: ${buildContext}`;

  return (
    <dialog
      aria-describedby="applicationWizardDescription"
      aria-labelledby="applicationWizardTitle"
      aria-label="Add application"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-0 max-w-4xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface overflow-hidden flex flex-col max-h-[90vh]"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      {/* Wizard Header */}
      <div className="flex items-center justify-between px-8 py-6 border-b border-outline-variant/20 bg-surface-container-low">
        <div className="flex items-center gap-4">
          <div className="p-3 bg-surface-container-high rounded-xl text-primary flex items-center justify-center">
            <Icon name="add_box" className="text-[24px]" />
          </div>
          <div>
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider block">
              Project Application
            </span>
            <h2 id="applicationWizardTitle" className="font-headline-md text-xl text-on-surface font-semibold">
              Add Application
            </h2>
          </div>
        </div>
        <button
          aria-label="Close application wizard"
          className="text-on-surface-variant hover:text-on-surface p-2 rounded-lg cursor-pointer transition-colors min-w-[40px] min-h-[40px] inline-flex items-center justify-center"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      {/* Step Indicator */}
      <div className="applicationWizardSteps flex items-center border-b border-outline-variant/20 bg-surface-container px-8 py-3 gap-6 overflow-x-auto">
        {(["source", "application", "build"] as Step[]).map((item, index) => {
          const active = step === item;
          const completed = (["source", "application", "build"] as Step[]).indexOf(step) > index;
          return (
            <div key={item} className="flex items-center gap-3">
              <span
                className={`w-6 h-6 rounded-full flex items-center justify-center font-label-sm text-xs font-semibold ${
                  active
                    ? "bg-primary text-on-primary"
                    : completed
                      ? "bg-status-ready/20 text-status-ready border border-status-ready/40"
                      : "bg-surface-container-highest text-on-surface-variant"
                }`}
              >
                {completed ? <Icon name="check" className="text-[14px]" /> : index + 1}
              </span>
              <span
                className={`font-body-md text-sm font-medium ${
                  active ? "text-primary font-bold" : completed ? "text-on-surface" : "text-on-surface-variant"
                }`}
              >
                {item === "source" ? "Source" : item === "application" ? "Application" : "Build"}
              </span>
              {index < 2 ? <Icon name="chevron_right" className="text-[16px] text-outline-variant/60" /> : null}
            </div>
          );
        })}
      </div>

      {/* Wizard Body: 2 Columns */}
      <div className="grid grid-cols-1 md:grid-cols-12 flex-1 overflow-y-auto min-h-0">
        {/* Left Column: Form Steps */}
        <div className="md:col-span-7 p-6 sm:p-8 flex flex-col gap-6 overflow-y-auto">
          {loading ? (
            <div className="flex items-center gap-3 py-12 justify-center text-on-surface-variant">
              <Icon name="sync" className="animate-spin text-[24px]" />
              <span>Loading GitHub inventory and applications…</span>
            </div>
          ) : null}

          {error ? (
            <div className="bg-error-container/20 border border-error/30 text-error p-4 rounded-xl text-xs flex items-center gap-2" role="alert">
              <Icon name="error" className="text-[18px] shrink-0" />
              <span>{error}</span>
            </div>
          ) : null}

          {/* Step 1: Source */}
          {!loading && step === "source" ? (
            installations.length === 0 ? (
              <section className="flex flex-col gap-4" aria-labelledby="connect-github-heading">
                <SectionTitle number="1" text="Authorize GitHub and Opsi will show the installations available to your account." title="Connect GitHub" />
                <p className="text-xs text-on-surface-variant" id="connect-github-heading">
                  You do not need an installation ID. GitHub confirms account access before Opsi can show repositories.
                </p>
                <Actions>
                  <Button onClick={onClose} type="button" variant="secondary">Cancel</Button>
                  <Button onClick={discoverGitHub} type="button" variant="primary">Continue with GitHub</Button>
                </Actions>
              </section>
            ) : (
              <div className="flex flex-col gap-5">
                <SectionTitle number="1" text="Select a GitHub installation, repository, and branch from authorized sources." title="Source Repository" />
                <div className="flex flex-col gap-4">
                  {installationNeedsClaim ? (
                    <section className="flex flex-col gap-3 rounded-lg border border-outline-variant/30 bg-surface-container p-4" aria-label="Connect discovered GitHub installation">
                      <label className="flex flex-col gap-2 text-xs font-label-sm uppercase text-on-surface-variant">
                        GitHub installation
                        <select
                          aria-label="GitHub installation"
                          className="w-full rounded-lg border border-outline-variant/30 bg-surface-container-highest p-3 font-body-md text-sm normal-case text-on-surface focus:outline-none focus:border-primary/50"
                          onChange={(event) => chooseInstallation(event.target.value)}
                          value={installationID}
                        >
                          {installations.map((item) => <option key={item.installation_id} value={item.installation_id}>{item.account_login}</option>)}
                        </select>
                      </label>
                      <p className="text-sm font-medium text-on-surface">Connect {installation?.account_login}</p>
                      <p className="text-xs text-on-surface-variant">GitHub found this installation. Continue once to verify access and load its repositories.</p>
                      <Button onClick={claimDiscoveredInstallation} type="button" variant="primary">Connect this GitHub installation</Button>
                    </section>
                  ) : (
                  <>
                  <div className="flex flex-col gap-2">
                    <label htmlFor="wizard-installation-select" className="font-label-sm text-xs text-on-surface-variant uppercase">GitHub installation</label>
                    <select
                      id="wizard-installation-select"
                      aria-label="GitHub installation"
                      className="w-full bg-surface-container-highest border border-outline-variant/30 text-on-surface rounded-lg p-3 font-body-md text-sm focus:outline-none focus:border-primary/50"
                      onChange={(e) => chooseInstallation(e.target.value)}
                      value={installationID}
                    >
                      {installations.map((item) => (
                        <option key={item.installation_id} value={item.installation_id}>
                          {item.account_login || `Installation ${item.installation_id}`}
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="flex flex-col gap-2">
                    <label htmlFor="wizard-repo-select" className="font-label-sm text-xs text-on-surface-variant uppercase">Repository</label>
                    <select
                      id="wizard-repo-select"
                      aria-label="Repository"
                      className="w-full bg-surface-container-highest border border-outline-variant/30 text-on-surface rounded-lg p-3 font-body-md text-sm focus:outline-none focus:border-primary/50"
                      onChange={(e) => chooseRepository(e.target.value)}
                      value={repositoryID || ""}
                    >
                      <option disabled value="">Choose repository</option>
                      {availableRepositories.map((item) => (
                        <option disabled={!repositoryUsable(item)} key={item.repository_id} value={item.repository_id}>
                          {item.full_name}
                        </option>
                      ))}
                    </select>
                    {availableRepositories.some((r) => !repositoryUsable(r)) ? (
                      <div className="space-y-1 pt-1">
                        {availableRepositories
                          .filter((r) => !repositoryUsable(r))
                          .map((r) => (
                            <p key={r.repository_id} className="text-xs text-on-surface-variant font-code-md">
                              {r.full_name}: {repositoryReason(r)}
                            </p>
                          ))}
                      </div>
                    ) : null}
                  </div>

                  <div className="flex flex-col gap-2">
                    <label htmlFor="wizard-ref-input" className="font-label-sm text-xs text-on-surface-variant uppercase">Branch or ref</label>
                    <input
                      id="wizard-ref-input"
                      aria-label="Branch or ref"
                      autoComplete="off"
                      className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-code-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
                      onChange={(e) => setSelectedRef(e.target.value)}
                      required
                      spellCheck={false}
                      value={selectedRef}
                    />
                  </div>
                  </>
                  )}
                </div>

                <Actions>
                  <Button onClick={onClose} type="button" variant="secondary">Cancel</Button>
                  <Button
                    disabled={installationNeedsClaim || !installation || !repository || !repositoryUsable(repository) || !selectedRef}
                    onClick={() => setStep("application")}
                    type="button"
                    variant="primary"
                  >
                    Continue
                  </Button>
                </Actions>
              </div>
            )
          ) : null}

          {/* Step 2: Application Identity & Container Config */}
          {!loading && step === "application" && repository ? (
            <form className="flex flex-col gap-5" onSubmit={reviewApplication}>
              <SectionTitle number="2" text="Specify service identity, build strategy, and runtime properties." title="Application Configuration" />

              <div className="p-3 bg-surface-container rounded-xl border border-outline-variant/20 flex items-center justify-between text-xs">
                <span className="font-code-md text-on-surface font-semibold truncate">{repository.full_name}</span>
                <span className="font-code-md text-primary bg-primary/10 px-2 py-0.5 rounded border border-primary/20">{selectedRef}</span>
              </div>

              <div className="flex flex-col gap-4">
                <div className="flex flex-col gap-2">
                  <label htmlFor="wizard-app-name" className="font-label-sm text-xs text-on-surface-variant uppercase">Application name</label>
                  <input
                    id="wizard-app-name"
                    aria-label="Application name"
                    autoComplete="off"
                    className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-body-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
                    onChange={(e) => setServiceKey(e.target.value)}
                    pattern={serviceKeyPattern}
                    placeholder="api-service"
                    required
                    spellCheck={false}
                    value={serviceKey}
                  />
                </div>

                <div className="flex flex-col gap-2">
                  <label htmlFor="wizard-app-root" className="font-label-sm text-xs text-on-surface-variant uppercase">Application root</label>
                  <input
                    id="wizard-app-root"
                    aria-label="Application root"
                    autoComplete="off"
                    className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-3 font-code-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
                    onChange={(e) => {
                      setApplicationRoot(e.target.value);
                      setBuildContext(e.target.value);
                    }}
                    placeholder="."
                    required
                    spellCheck={false}
                    value={applicationRoot}
                  />
                </div>

                <div className="flex flex-col gap-2">
                  <span className="font-label-sm text-xs text-on-surface-variant uppercase">Build Strategy</span>
                  <div className="space-y-2">
                    <label className="flex items-center gap-3 p-3 bg-surface-container rounded-xl border border-outline-variant/20 cursor-pointer">
                      <input
                        type="radio"
                        name="strategy"
                        value="auto"
                        checked={buildStrategy === "auto"}
                        onChange={() => {
                          setBuildStrategy("auto");
                          setDockerfilePath("");
                        }}
                        className="accent-primary"
                      />
                      <span className="font-body-md text-sm text-on-surface">Automatic Recommended</span>
                    </label>
                    <p className="text-xs text-on-surface-variant pl-7">
                      Opsi uses a Dockerfile when one is available; otherwise it builds source with Cloud Native Buildpacks.
                    </p>
                    <label className="flex items-center gap-3 p-3 bg-surface-container rounded-xl border border-outline-variant/20 cursor-pointer">
                      <input
                        type="radio"
                        name="strategy"
                        value="dockerfile"
                        checked={buildStrategy === "dockerfile"}
                        onChange={() => setBuildStrategy("dockerfile")}
                        className="accent-primary"
                      />
                      <span className="font-body-md text-sm text-on-surface">Dockerfile (custom image definition)</span>
                    </label>
                    {buildStrategy === "dockerfile" ? (
                      <div className="pl-7 pt-1">
                        <label htmlFor="wizard-dockerfile-path" className="font-label-sm text-xs text-on-surface-variant uppercase block mb-1">Dockerfile path</label>
                        <input
                          id="wizard-dockerfile-path"
                          aria-label="Dockerfile path"
                          className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 font-code-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
                          onChange={(e) => setDockerfilePath(e.target.value)}
                          placeholder="apps/api/Dockerfile"
                          required
                          value={dockerfilePath}
                        />
                      </div>
                    ) : null}
                    <label className="flex items-center gap-3 p-3 bg-surface-container rounded-xl border border-outline-variant/20 cursor-pointer">
                      <input
                        type="radio"
                        name="strategy"
                        value="buildpack"
                        checked={buildStrategy === "buildpack"}
                        onChange={() => {
                          setBuildStrategy("buildpack");
                          setDockerfilePath("");
                        }}
                        className="accent-primary"
                      />
                      <span className="font-body-md text-sm text-on-surface">Cloud Native Buildpacks (auto-detect runtime)</span>
                    </label>
                  </div>
                </div>

                <details className="space-y-3 pt-2 border-t border-outline-variant/10">
                  <summary className="font-label-sm text-xs text-primary cursor-pointer select-none">Advanced build settings</summary>
                  <div className="pt-2">
                    <label htmlFor="wizard-build-context" className="font-label-sm text-xs text-on-surface-variant uppercase block mb-1">Build context</label>
                    <input
                      id="wizard-build-context"
                      aria-label="Build context"
                      className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 font-code-md text-sm text-on-surface focus:outline-none focus:border-primary/50"
                      onChange={(e) => setBuildContext(e.target.value)}
                      value={buildContext}
                    />
                  </div>
                </details>
              </div>

              {resumeBinding ? (
                <div role="status" className="bg-status-warning/10 border border-status-warning/30 text-status-warning p-3 rounded-lg text-xs">
                  <span>Resume source binding</span>: Service exists but GitHub source binding is missing.
                </div>
              ) : null}

              {keyConflict ? (
                <div className="bg-error-container/20 border border-error/30 text-error p-3 rounded-lg text-xs" role="alert">
                  {keyConflict}
                </div>
              ) : null}

              <Actions>
                <Button onClick={() => setStep("source")} type="button" variant="secondary">Back</Button>
                <Button disabled={!serviceKey || Boolean(keyConflict)} type="submit" variant="primary">
                  {alreadyBound ? "Continue to build" : resumeBinding ? "Resume source binding" : "Review application"}
                </Button>
              </Actions>
            </form>
          ) : null}

          {/* Step 3: Build Verification */}
          {step === "build" && createdService ? (
            <div className="flex flex-col gap-5">
              <SectionTitle number="3" text="Source binding is active. Build jobs are executed on Cloud runners." title="Application ready" />

              <div className="bg-surface-container rounded-xl p-4 border border-outline-variant/20 grid grid-cols-3 gap-3 text-xs">
                <div>
                  <span className="font-label-sm text-on-surface-variant uppercase block">Application</span>
                  <span className="font-body-md text-on-surface font-semibold">{createdService.name}</span>
                </div>
                <div>
                  <span className="font-label-sm text-on-surface-variant uppercase block">State</span>
                  <span className="font-label-sm text-status-warning font-semibold bg-status-warning/10 px-2 py-0.5 rounded border border-status-warning/20">Unplaced</span>
                </div>
                <div>
                  <span className="font-label-sm text-on-surface-variant uppercase block">Source</span>
                  <span className="font-label-sm text-status-ready font-semibold bg-status-ready/10 px-2 py-0.5 rounded border border-status-ready/20">Ready</span>
                </div>
              </div>

              {buildLoading ? (
                <div className="flex items-center gap-2 text-sm text-on-surface-variant py-4">
                  <Icon name="sync" className="animate-spin text-[18px]" />
                  <span>Loading build records…</span>
                </div>
              ) : null}

              {buildRecord ? (
                <BuildRecordFacts record={buildRecord} />
              ) : buildJob ? (
                <BuildJobFacts job={buildJob} />
              ) : (
                <div className="bg-surface-container-lowest border border-dashed border-outline-variant/30 rounded-xl p-8 text-center flex flex-col items-center gap-2">
                  <Icon name="terminal" className="text-[32px] text-on-surface-variant/50" />
                  <p className="text-on-surface-variant text-xs max-w-sm">
                    No build has been requested.
                  </p>
                </div>
              )}

              <Actions>
                <Button onClick={onClose} type="button" variant="secondary">Close</Button>
                {!buildRecord && (!buildJob || terminalBuild(buildJob)) ? (
                  <Button onClick={reviewBuild} type="button" variant="primary">
                    Build application
                  </Button>
                ) : null}
              </Actions>
            </div>
          ) : null}
        </div>

        {/* Right Column: Live YAML Identity Preview */}
        <div className="md:col-span-5 bg-surface-container border-t md:border-t-0 md:border-l border-outline-variant/20 p-6 flex flex-col gap-4">
          <div className="flex items-center justify-between">
            <span className="font-label-sm text-xs text-on-surface-variant uppercase tracking-wider">
              Identity Descriptor Preview
            </span>
            <span className="text-[10px] font-code-md text-primary bg-primary/10 px-2 py-0.5 rounded border border-primary/20">
              YAML
            </span>
          </div>

          <pre className="font-code-md text-xs text-on-surface/90 bg-surface-container-lowest p-4 rounded-xl border border-outline-variant/20 flex-1 overflow-x-auto leading-relaxed whitespace-pre font-mono">
            <code>{preview}</code>
          </pre>

          <div className="p-4 bg-surface-container-high rounded-xl border border-outline-variant/20 flex flex-col gap-2">
            <div className="flex items-center gap-2 text-xs font-semibold text-on-surface">
              <Icon name="shield" className="text-primary text-[16px]" />
              <span>Boundary Invariants</span>
            </div>
            <ul className="text-[11px] text-on-surface-variant space-y-1 list-disc pl-4">
              <li>Application identity creates source state without placement.</li>
              <li>No build, topology apply, or rollout occurs until explicit review.</li>
              <li>PAT secrets remain safely in native keychain storage.</li>
            </ul>
          </div>
        </div>
      </div>
    </dialog>
  );
}

function SectionTitle({ number, title, text }: { number: string; title: string; text: string }) {
  return (
    <div className="flex flex-col gap-1 border-b border-outline-variant/20 pb-3">
      <div className="flex items-center gap-2">
        <span className="w-5 h-5 rounded-full bg-primary/10 text-primary font-label-sm text-xs flex items-center justify-center font-bold">
          {number}
        </span>
        <h3 className="font-headline-md text-base font-semibold text-on-surface">{title}</h3>
      </div>
      <p className="text-xs text-on-surface-variant">{text}</p>
    </div>
  );
}

function Actions({ children }: { children: ReactNode }) {
  return <div className="flex items-center justify-end gap-3 pt-4 border-t border-outline-variant/20">{children}</div>;
}

function repositoryUsable(repository: GitHubRepository) {
  return (
    repository.status === "active" &&
    !repository.archived &&
    !repository.disabled &&
    (repository.claim_status === "available" || repository.claim_status === "active")
  );
}

function repositoryReason(repository: GitHubRepository) {
  if (repository.archived) return "archived";
  if (repository.disabled) return "disabled";
  if (repository.status !== "active") return `repository ${repository.status}`;
  if (repository.claim_status === "conflict") return "claimed by another project";
  if (repository.claim_status === "active") return "claimed by this project";
  return "available";
}
