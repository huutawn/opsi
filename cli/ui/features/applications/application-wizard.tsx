"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildJob, BuildRecord, GitHubBinding, GitHubInstallation, GitHubRepository, ServiceRecord } from "@/lib/contracts/registry";
import { buildFailure, terminalBuild } from "@/lib/presentation/build";

const serviceKeyPattern = "[a-z0-9](?:[a-z0-9_\\-]{0,61}[a-z0-9])?";
type Step = "source" | "application" | "build";
type BuildStrategy = "auto" | "dockerfile" | "buildpack";

export function ApplicationWizard({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated?: () => void | Promise<void> }) {
  const client = useMemo(() => new LocalClient(), []);
  const dialog = useRef<HTMLDialogElement>(null);
  const projectID = console.state.project?.id ?? "";
  const [step, setStep] = useState<Step>("source");
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [bindings, setBindings] = useState<GitHubBinding[]>([]);
  const [services, setServices] = useState<ServiceRecord[]>([]);
  const [installationID, setInstallationID] = useState(0);
  const [repositoryID, setRepositoryID] = useState(0);
  const [selectedRef, setSelectedRef] = useState("");
  const [serviceKey, setServiceKey] = useState("");
  const [applicationRoot, setApplicationRoot] = useState(".");
  const [buildContext, setBuildContext] = useState(".");
  const [buildStrategy, setBuildStrategy] = useState<BuildStrategy>("auto");
  const [dockerfilePath, setDockerfilePath] = useState("");
  const [createdService, setCreatedService] = useState<ServiceRecord | null>(null);
  const [buildJob, setBuildJob] = useState<BuildJob | null>(null);
  const [buildRecord, setBuildRecord] = useState<BuildRecord | null>(null);
  const [loading, setLoading] = useState(true);
  const [buildLoading, setBuildLoading] = useState(false);
  const [error, setError] = useState("");
  const createdServiceID = createdService?.id ?? "";
  const createdServiceName = createdService?.name ?? "";
  const buildJobID = buildJob?.id ?? "";
  const buildJobStatus = buildJob?.status ?? "";

  useEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => { if (element?.open) element.close(); };
  }, []);

  useEffect(() => {
    let active = true;
    void Promise.all([client.githubInstallations(projectID), client.githubRepositories(projectID), client.githubBindings(projectID), client.services(projectID)])
      .then(([installationData, repositoryData, bindingData, serviceData]) => {
        if (!active) return;
        const nextInstallations = (installationData.installations ?? []).filter((item) => item.status === "active" && !item.suspended);
        const nextRepositories = repositoryData.repositories ?? [];
        const firstInstallation = nextInstallations[0];
        const firstRepository = nextRepositories.find((item) => item.installation_id === firstInstallation?.installation_id && repositoryUsable(item));
        setInstallations(nextInstallations);
        setRepositories(nextRepositories);
        setBindings((bindingData.bindings ?? []).filter((item) => item.status === "active"));
        setServices(serviceData.services ?? []);
        setInstallationID(firstInstallation?.installation_id ?? 0);
        setRepositoryID(firstRepository?.repository_id ?? 0);
        setSelectedRef(firstRepository?.default_branch || "main");
        setLoading(false);
      })
      .catch((cause: unknown) => {
        if (!active) return;
        setError(cause instanceof Error ? cause.message : "GitHub inventory is unavailable.");
        setLoading(false);
      });
    return () => { active = false; };
  }, [client, projectID]);

  useEffect(() => {
    if (step !== "build" || !createdServiceID || !buildJobID || buildJobStatus === "failed" || buildJobStatus === "cancelled") return;
    let active = true;
    let timer = 0;
    async function refresh() {
      try {
        const jobs = await client.buildJobs(projectID, createdServiceID);
        const latest = jobs.build_jobs?.[0] ?? null;
        if (!active) return;
        setBuildJob(latest ?? null);
        if (latest?.build_record_id || latest?.status === "succeeded") {
          const records = await client.buildRecords(projectID, { serviceKey: createdServiceName });
          if (active) setBuildRecord(records.records?.[0] ?? null);
        }
        if (active && latest && !terminalBuild(latest)) timer = window.setTimeout(refresh, 3000);
      } catch (cause) {
        if (active) setError(cause instanceof Error ? cause.message : "Build state is unavailable.");
      }
    }
    void refresh();
    return () => { active = false; window.clearTimeout(timer); };
  }, [buildJobID, buildJobStatus, client, createdServiceID, createdServiceName, projectID, step]);

  const repository = repositories.find((item) => item.repository_id === repositoryID);
  const installation = installations.find((item) => item.installation_id === installationID);
  const availableRepositories = repositories.filter((item) => item.installation_id === installationID);
  const existingService = services.find((service) => service.name === serviceKey);
  const existingBinding = existingService ? bindings.find((binding) => binding.service_id === existingService.id) : undefined;
  const expectedRepoURL = repository ? `https://github.com/${repository.full_name}` : "";
  const resumeBinding = Boolean(existingService && !existingBinding && existingService.source_type === "git" && existingService.repo_url === expectedRepoURL);
  const alreadyBound = Boolean(existingBinding && existingBinding.repository_id === repositoryID && existingBinding.service_key === serviceKey);
  const keyConflict = existingService && !resumeBinding && !alreadyBound
    ? "An application with this name already exists with a different source binding."
    : bindings.some((binding) => binding.repository_id === repositoryID && binding.service_key === serviceKey && binding.service_id !== existingService?.id)
      ? "This repository application name already has an active binding."
      : "";

  function chooseInstallation(value: string) {
    const nextID = Number(value);
    const first = repositories.find((item) => item.installation_id === nextID && repositoryUsable(item));
    setInstallationID(nextID);
    setRepositoryID(first?.repository_id ?? 0);
    setSelectedRef(first?.default_branch || "main");
  }

  function chooseRepository(value: string) {
    const selected = repositories.find((item) => item.repository_id === Number(value));
    setRepositoryID(selected?.repository_id ?? 0);
    setSelectedRef(selected?.default_branch || "main");
  }

  function connectGitHub(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextInstallationID = Number(new FormData(event.currentTarget).get("installation_id"));
    if (!nextInstallationID) return;
    console.reviewMutation(
      { project: console.state.project?.name || projectID, targetType: "GitHub installation", targetID: String(nextInstallationID), operation: "connect", diff: [`Connect GitHub installation ${nextInstallationID}`], risk: "Starts GitHub authorization through the existing Local API flow." },
      async (key) => {
        const started = await client.startGitHubInstallationClaim(projectID, nextInstallationID, key);
        window.location.assign(started.authorization_url);
        return "GitHub authorization started.";
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
    const containerPort = optionalNumber(form.get("container_port"));
    const healthPath = String(form.get("health_path") || "");
    const cpu = String(form.get("cpu") || "");
    const memory = String(form.get("memory") || "");
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
      ...(containerPort ? { container_port: containerPort } : {}),
      ...(healthPath ? { health_path: healthPath } : {}),
      ...(cpu || memory ? { resource_requests: { ...(cpu ? { cpu } : {}), ...(memory ? { memory } : {}) } } : {}),
    };
    const needsClaim = repository.claim_status === "available";
    const strategyLabel = buildStrategy === "buildpack" ? "Buildpacks" : buildStrategy === "dockerfile" ? `Dockerfile ${dockerfilePath}` : "Automatic";
    const mutations = [
      ...(needsClaim ? [`Claim repository ${repository.full_name} for this project`] : []),
      ...(resumeBinding ? [] : [`Create Application ${serviceKey}: ${repository.full_name}@${selectedRef}, root ${applicationRoot}, runtime Web service`]),
      `Bind source: context ${buildContext}, build ${strategyLabel}`,
    ];
    console.reviewMutation(
      { project: console.state.project?.name || projectID, targetType: "application", targetID: serviceKey, operation: "create", diff: mutations, risk: "Creates source identity only. The Application remains Unplaced; no build, placement, topology apply, or deployment starts automatically." },
      async (key) => {
        if (needsClaim) await client.claimGitHubRepository(projectID, repository.repository_id, key);
        const created = existingService ?? await client.createService(projectID, serviceBody, key);
        await client.createGitHubBinding(projectID, { service_id: created.id, repository_id: repository.repository_id, service_key: serviceKey, config_path: configPath, selected_ref: selectedRef, application_root: applicationRoot, build_context: buildContext, build_strategy: buildStrategy, ...(buildStrategy === "dockerfile" ? { dockerfile_path: dockerfilePath } : {}) }, key);
        setCreatedService(created);
        setServices((current) => current.some((item) => item.id === created.id) ? current : [...current, created]);
        setStep("build");
        await Promise.all([console.actions.load(), onCreated?.()]);
        await loadBuildFacts(created);
        return `Application ${serviceKey} ${resumeBinding ? "source binding resumed" : "created and GitHub bound"}. Placement is Unplaced; no build or deployment started.`;
      },
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
      const [jobs, records] = await Promise.all([client.buildJobs(projectID, service.id), client.buildRecords(projectID, { serviceKey: service.name })]);
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
      { project: console.state.project?.name || projectID, targetType: "BuildJob", targetID: createdService.id, operation: "build", diff: [`Resolve exact commit from ${repository?.full_name ?? "bound repository"}@${selectedRef}`, "Resolve the canonical build strategy in Cloud", "Publish one immutable image digest and accept a BuildRecord only after verification"], risk: "Creates a BuildJob only. It does not create a BuildRecord client-side, place the Application, or deploy it." },
      async (key) => {
        const job = await client.createBuildJob(projectID, createdService.id, key);
        setBuildJob(job);
        setBuildRecord(null);
        return `BuildJob ${job.id} accepted with factual state ${job.status}.`;
      },
    );
  }

  const preview = `kind: Application\nmetadata:\n  name: ${serviceKey || "waiting..."}\n  state: Unplaced\nspec:\n  source:\n    repository: ${repository?.full_name || "waiting..."}\n    ref: ${selectedRef || "waiting..."}\n    root: ${applicationRoot}\n  build:\n    method: ${buildStrategy === "buildpack" ? "buildpacks" : buildStrategy}\n    context: ${buildContext}`;

  return <dialog aria-describedby="applicationWizardDescription" aria-labelledby="applicationWizardTitle" className="nativeDialog applicationWizard" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}>
    <header className="applicationWizardHeader"><div><p className="eyebrow">Project application</p><h2 id="applicationWizardTitle">Add application</h2><p id="applicationWizardDescription">Connect source, review one Application identity, then build when you are ready. New Applications remain <b>Unplaced</b>.</p></div><button aria-label="Close application wizard" autoFocus className="iconButton" onClick={onClose} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button></header>
    <ol aria-label="Application setup steps" className="applicationWizardSteps">{(["source", "application", "build"] as Step[]).map((item, index) => <li aria-current={step === item ? "step" : undefined} data-active={step === item} data-complete={index < (["source", "application", "build"] as Step[]).indexOf(step)} key={item}><span>{index + 1}</span>{item === "source" ? "Source" : item === "application" ? "Application" : "Build"}</li>)}</ol>
    <div className="applicationWizardLayout"><main className="applicationWizardMain">
      {loading ? <p aria-live="polite" role="status">Loading GitHub installations, repositories, claims, Applications, and source bindings...</p> : null}
      {error ? <div className="errorBox" role="alert"><b>Factual state unavailable</b><span>{error}</span></div> : null}
      {!loading && step === "source" ? installations.length === 0 ? <form className="applicationSection form" onSubmit={connectGitHub}><SectionTitle number="1" title="Connect GitHub" text="No active GitHub installation is connected to this project." /><label className="span2">Installation ID<input className="field" inputMode="numeric" min="1" name="installation_id" required type="number" /></label><Actions><button onClick={onClose} type="button">Cancel</button><button className="primary" type="submit">Connect GitHub</button></Actions></form> : <section className="applicationSection"><SectionTitle number="1" title="Source" text="Choose from the GitHub inventory already authorized for this project." /><div className="form"><label>GitHub installation<select className="select" onChange={(event) => chooseInstallation(event.target.value)} value={installationID}>{installations.map((item) => <option key={item.installation_id} value={item.installation_id}>{item.account_login || `Installation ${item.installation_id}`}</option>)}</select></label><label>Repository<select className="select" onChange={(event) => chooseRepository(event.target.value)} value={repositoryID || ""}><option disabled value="">Choose repository</option>{availableRepositories.map((item) => <option disabled={!repositoryUsable(item)} key={item.repository_id} value={item.repository_id}>{item.full_name} - {repositoryReason(item)}</option>)}</select></label><label className="span2">Branch or ref<input autoComplete="off" className="field" onChange={(event) => setSelectedRef(event.target.value)} required spellCheck={false} value={selectedRef} /></label>{availableRepositories.filter((item) => !repositoryUsable(item)).map((item) => <p className="repositoryConflict span2" key={item.repository_id}><b>{item.full_name}</b>: {repositoryReason(item)}</p>)}</div><Actions><button onClick={onClose} type="button">Cancel</button><button className="primary" disabled={!installation || !repository || !repositoryUsable(repository) || !selectedRef} onClick={() => setStep("application")} type="button">Continue</button></Actions></section> : null}
      {!loading && step === "application" && repository ? <form className="applicationSection applicationForm" onSubmit={reviewApplication}><SectionTitle number="2" title="Application" text="Opsi needs an identity and the directory that contains the application. Language and framework are detected only by the canonical build." /><div className="sourceSelection"><span><b>{repository.full_name}</b><code>{selectedRef}</code></span><small>{repository.claim_status === "active" ? "Claimed by this project" : "Claim included in review"}</small></div><div className="form"><label>Application name<input autoComplete="off" className="field" onChange={(event) => setServiceKey(event.target.value)} pattern={serviceKeyPattern} placeholder="api" required spellCheck={false} value={serviceKey} /></label><label>Runtime type<select className="select" defaultValue="application"><option value="application">Web service</option></select></label><label className="span2" htmlFor="applicationRoot">Application root</label><div className="span2 fieldWithHelp"><input aria-describedby="applicationRootHelp" autoComplete="off" className="field" id="applicationRoot" onChange={(event) => setApplicationRoot(event.target.value)} placeholder="apps/api" required spellCheck={false} value={applicationRoot} /><small id="applicationRootHelp">Application root is the directory containing the application to build. Use <code>.</code> when the repository root is enough.</small></div></div><fieldset className="buildMethod"><legend>Build method</legend><label><input checked={buildStrategy === "auto"} name="build_strategy" onChange={() => { setBuildStrategy("auto"); setDockerfilePath(""); }} type="radio" /><span><b>Automatic <em>Recommended</em></b><small>Opsi uses a Dockerfile when one is available; otherwise it builds source with Cloud Native Buildpacks.</small></span></label><label><input checked={buildStrategy === "dockerfile"} name="build_strategy" onChange={() => setBuildStrategy("dockerfile")} type="radio" /><span><b>Dockerfile</b><small>Build from an exact Dockerfile path.</small></span></label><label><input checked={buildStrategy === "buildpack"} name="build_strategy" onChange={() => { setBuildStrategy("buildpack"); setDockerfilePath(""); }} type="radio" /><span><b>Buildpacks</b><small>Build directly from source.</small></span></label></fieldset>{buildStrategy === "dockerfile" ? <label className="standaloneField">Dockerfile path<input autoComplete="off" className="field" onChange={(event) => setDockerfilePath(event.target.value)} placeholder="apps/api/Dockerfile" required spellCheck={false} value={dockerfilePath} /></label> : null}<details className="applicationAdvanced"><summary>Advanced build settings</summary><div className="form"><label>Build context<input autoComplete="off" className="field" onChange={(event) => setBuildContext(event.target.value)} required spellCheck={false} value={buildContext} /><small>Separate from Application root. Use <code>.</code> for repository-wide Docker build inputs.</small></label><label>Source config path<input autoComplete="off" className="field" defaultValue=".opsi/opsi-cd.yaml" name="config_path" required spellCheck={false} /></label></div></details><details className="applicationAdvanced"><summary>Runtime settings</summary><div className="form"><label>Container port<input className="field" max="65535" min="1" name="container_port" placeholder="8080" type="number" /></label><label>Health path<input autoComplete="off" className="field" name="health_path" pattern="/.*" placeholder="/health" /></label><label>CPU request<input autoComplete="off" className="field" name="cpu" placeholder="250m" /></label><label>Memory request<input autoComplete="off" className="field" name="memory" placeholder="256Mi" /></label></div></details>{resumeBinding ? <p className="sourceSelection" role="status"><b>Resume source binding</b><span>The Application identity exists; only the missing GitHub binding will be applied.</span></p> : null}{alreadyBound ? <p className="sourceSelection" role="status"><b>Source binding complete</b><span>The existing Application and binding will be reused. Existing accepted builds are loaded next.</span></p> : null}{keyConflict ? <p className="repositoryConflict" role="alert">{keyConflict}</p> : null}<Actions><button onClick={() => setStep("source")} type="button">Back</button><button className="primary" disabled={!serviceKey || Boolean(keyConflict)} type="submit">{alreadyBound ? "Continue to build" : resumeBinding ? "Resume source binding" : "Review application"}</button></Actions></form> : null}
      {step === "build" && createdService ? <section className="applicationSection buildSection"><SectionTitle number="3" title="Application ready" text="Source is bound. Building remains an explicit reviewed action and deployment remains separate." /><div className="creationState"><div><span>Application</span><b>{createdService.name}</b></div><div><span>Placement</span><b>Unplaced</b></div><div><span>Source</span><b>Ready</b></div></div>{buildLoading ? <p aria-live="polite" role="status">Loading existing BuildJobs and accepted BuildRecords...</p> : null}{buildRecord ? <BuildRecordFacts record={buildRecord} /> : buildJob ? <BuildJobFacts job={buildJob} /> : <div className="emptyBuild"><b>No build has been requested.</b><p>Opsi will resolve the exact commit and build strategy when you create a BuildJob.</p></div>}<Actions><button onClick={onClose} type="button">Close</button>{!buildRecord && (!buildJob || terminalBuild(buildJob)) ? <button className="primary" onClick={reviewBuild} type="button">{buildJob?.status === "failed" ? "Build again" : "Build application"}</button> : null}</Actions></section> : null}
    </main><aside className="applicationPreview" aria-label="Application factual preview"><p className="eyebrow">Preview blueprint</p><pre><code>{preview}</code></pre><p><b>Unplaced</b> means no compute is consumed until this Application is explicitly placed in Topology.</p></aside></div>
  </dialog>;
}

function SectionTitle({ number, title, text }: { number: string; title: string; text: string }) { return <div className="applicationSectionTitle"><span aria-hidden="true">{number}</span><div><h3>{title}</h3><p>{text}</p></div></div>; }
function Actions({ children }: { children: ReactNode }) { return <div className="applicationActions">{children}</div>; }

function BuildJobFacts({ job }: { job: BuildJob }) {
  const failure = job.status === "failed" ? buildFailure(job.failure_code, job.failure_message_redacted) : null;
  return <div className="buildFacts"><div className="buildStateHeading"><span><small>BuildJob</small><code>{job.id}</code></span><b data-state={job.status}>{job.status}</b></div><dl><Fact label="Selected ref" value={job.source.selected_ref} /><Fact label="Exact commit" value={job.source.resolved_commit_sha} mono /><Fact label="Requested strategy" value={strategyName(job.requested_build_strategy)} /><Fact label="Resolved strategy" value={strategyName(job.resolved_build_strategy)} /><Fact label="Application root" value={job.source.application_root} mono /><Fact label="Build context" value={job.source.build_context} mono />{job.dockerfile_path ? <Fact label="Dockerfile" value={job.dockerfile_path} mono /> : null}</dl>{failure ? <div className="buildFailure" role="alert"><b>{failure.title}</b><p>{failure.action}</p>{job.failure_code ? <code>{job.failure_code}</code> : null}</div> : null}</div>;
}

function BuildRecordFacts({ record }: { record: BuildRecord }) {
  const processes = record.build.builder?.processes?.map((process) => process.type).join(", ");
  return <div className="buildFacts acceptedBuild"><div className="buildStateHeading"><span><small>Accepted BuildRecord</small><code>{record.id}</code></span><b data-state="succeeded">succeeded</b></div><dl><Fact label="Exact source SHA" value={record.workload.sha} mono /><Fact label="Strategy" value={strategyName(record.build.build_strategy)} /><Fact label="Image digest" value={record.build.oci_digest} mono /><Fact label="Created" value={new Date(record.created_at).toLocaleString()} /><Fact label="Builder" value={[record.build.builder_identity, record.build.builder_version].filter(Boolean).join(" · ") || "Not reported"} />{processes ? <Fact label="Detected processes" value={processes} /> : null}</dl></div>;
}

function Fact({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "monoWrap" : undefined}>{value || "Not reported"}</dd></div>; }
function optionalNumber(value: FormDataEntryValue | null) { const text = String(value || ""); return text ? Number(text) : 0; }
function strategyName(value?: string) { return value === "buildpack" ? "Buildpacks" : value === "dockerfile" ? "Dockerfile" : value === "auto" ? "Automatic" : "Not resolved"; }
function repositoryUsable(repository: GitHubRepository) { return repository.status === "active" && !repository.archived && !repository.disabled && (repository.claim_status === "available" || repository.claim_status === "active"); }
function repositoryReason(repository: GitHubRepository) { if (repository.archived) return "archived"; if (repository.disabled) return "disabled"; if (repository.status !== "active") return `repository ${repository.status}`; if (repository.claim_status === "conflict") return "claimed by another project"; if (repository.claim_status === "active") return "claimed by this project"; return "available"; }
