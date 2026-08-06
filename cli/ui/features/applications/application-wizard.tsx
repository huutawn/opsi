"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { GitHubBinding, GitHubInstallation, GitHubRepository, ServiceRecord } from "@/lib/contracts/registry";

const serviceKeyPattern = "[a-z0-9](?:[a-z0-9_\\-]{0,61}[a-z0-9])?";

export function ApplicationWizard({ console, onClose, onCreated }: { console: ConsoleController; onClose: () => void; onCreated?: () => void | Promise<void> }) {
  const client = useMemo(() => new LocalClient(), []);
  const dialog = useRef<HTMLDialogElement>(null);
  const projectID = console.state.project?.id ?? "";
  const [step, setStep] = useState<"source" | "application">("source");
  const [installations, setInstallations] = useState<GitHubInstallation[]>([]);
  const [repositories, setRepositories] = useState<GitHubRepository[]>([]);
  const [bindings, setBindings] = useState<GitHubBinding[]>([]);
  const [services, setServices] = useState<ServiceRecord[]>([]);
  const [repositoryID, setRepositoryID] = useState(0);
  const [branch, setBranch] = useState("");
  const [serviceKey, setServiceKey] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

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
        const nextRepositories = repositoryData.repositories ?? [];
        const first = nextRepositories.find(repositoryUsable);
        setInstallations((installationData.installations ?? []).filter((item) => item.status === "active" && !item.suspended));
        setRepositories(nextRepositories);
        setBindings((bindingData.bindings ?? []).filter((item) => item.status === "active"));
        setServices(serviceData.services ?? []);
        setRepositoryID(first?.repository_id ?? 0);
        setBranch(first?.default_branch || "main");
        setLoading(false);
      })
      .catch((cause: unknown) => {
        if (!active) return;
        setError(cause instanceof Error ? cause.message : "GitHub inventory is unavailable.");
        setLoading(false);
      });
    return () => { active = false; };
  }, [client, projectID]);

  const repository = repositories.find((item) => item.repository_id === repositoryID);
  const keyConflict = services.some((service) => service.name === serviceKey)
    ? "An application with this service key already exists."
    : bindings.some((binding) => binding.repository_id === repositoryID && binding.service_key === serviceKey)
      ? "This repository service key already has an active binding."
      : "";

  function chooseRepository(value: string) {
    const selected = repositories.find((item) => item.repository_id === Number(value));
    setRepositoryID(selected?.repository_id ?? 0);
    setBranch(selected?.default_branch || "main");
  }

  function connectGitHub(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const installationID = Number(new FormData(event.currentTarget).get("installation_id"));
    if (!installationID) return;
    console.reviewMutation(
      { project: console.state.project?.name || projectID, targetType: "GitHub installation", targetID: String(installationID), operation: "connect", diff: [`Connect GitHub installation ${installationID}`], risk: "Starts GitHub authorization through the existing Local API flow." },
      async (key) => {
        const started = await client.startGitHubInstallationClaim(projectID, installationID, key);
        window.location.assign(started.authorization_url);
        return "GitHub authorization started.";
      },
    );
    onClose();
  }

  function reviewApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!repository || !repositoryUsable(repository) || keyConflict) return;
    const form = new FormData(event.currentTarget);
    const configPath = String(form.get("config_path") || ".opsi/opsi-cd.yaml").trim();
    const buildContext = String(form.get("build_context") || ".").trim();
    const dockerfile = String(form.get("dockerfile") || "Dockerfile").trim();
    const healthPath = String(form.get("health_path") || "/health").trim();
    const containerPort = Number(form.get("container_port"));
    const repoURL = `https://github.com/${repository.full_name}`;
    const serviceBody = {
      name: serviceKey,
      type: "application",
      source_type: "git",
      repo_url: repoURL,
      branch,
      build_method: "dockerfile",
      build_context: buildContext,
      dockerfile,
      container_port: containerPort,
      health_path: healthPath,
    };
    const needsClaim = repository.claim_status === "available";
    const mutations = [
      ...(needsClaim ? [`Claim repository ${repository.full_name} for this project`] : []),
      `Create application identity ${serviceKey}: git ${repoURL}#${branch}, context ${buildContext}, Dockerfile ${dockerfile}, port ${containerPort}, health ${healthPath}`,
      `Create GitHub service binding: repository ${repository.repository_id}, service ${serviceKey}, config ${configPath}`,
    ];
    console.reviewMutation(
      { project: console.state.project?.name || projectID, targetType: "application", targetID: serviceKey, operation: "create", diff: mutations, risk: "Creates source identity only. It does not place, apply topology, deploy, or mutate the repository." },
      async (key) => {
        if (needsClaim) await client.claimGitHubRepository(projectID, repository.repository_id, key);
        const created = await client.createService(projectID, serviceBody, key);
        await client.createGitHubBinding(projectID, { service_id: created.id, repository_id: repository.repository_id, service_key: serviceKey, config_path: configPath }, key);
        await Promise.all([
          console.actions.load(),
          client.githubInstallations(projectID),
          client.githubRepositories(projectID),
          client.githubBindings(projectID),
          onCreated?.(),
        ]);
        console.navigate({ view: "infrastructure", tab: "topology", topology: `service:${serviceKey}` });
        return `Application ${serviceKey} created and GitHub bound. Placement remains Unplaced.`;
      },
    );
    onClose();
  }

  return <dialog aria-labelledby="applicationWizardTitle" className="nativeDialog applicationWizard" onCancel={(event) => { event.preventDefault(); onClose(); }} ref={dialog}>
    <button aria-label="Close application wizard" autoFocus className="iconButton dialogClose" onClick={onClose} type="button"><svg aria-hidden="true" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15" /></svg></button>
    <p className="eyebrow">Project application</p><h2 id="applicationWizardTitle">Add application</h2><p>Bind factual GitHub source identity now. Placement and delivery remain separate reviewed operations.</p>
    <ol aria-label="Application setup steps" className="applicationWizardSteps"><li data-active={step === "source"}>Source</li><li data-active={step === "application"}>Application</li><li>Review</li></ol>
    {loading ? <p role="status">Loading GitHub installations, repositories, claims, services, and bindings...</p> : null}
    {error ? <div className="errorBox" role="alert"><b>Source inventory unavailable</b><span>{error}</span></div> : null}
    {!loading && !error && step === "source" ? installations.length === 0 ? <form className="form" onSubmit={connectGitHub}><div className="span2"><h3>Connect GitHub</h3><p className="muted">No active GitHub installation is connected to this project.</p></div><label className="span2">Installation ID<input className="field" inputMode="numeric" min="1" name="installation_id" required type="number" /></label><div className="modalActions span2"><button onClick={onClose} type="button">Cancel</button><button className="primary" type="submit">Connect GitHub</button></div></form> : <div className="form"><label className="span2">Repository<select className="select" onChange={(event) => chooseRepository(event.target.value)} value={repositoryID || ""}><option disabled value="">Choose repository</option>{repositories.map((item) => <option disabled={!repositoryUsable(item)} key={item.repository_id} value={item.repository_id}>{item.full_name} - {repositoryReason(item)}</option>)}</select></label>{repositories.filter((item) => !repositoryUsable(item)).map((item) => <p className="repositoryConflict span2" key={item.repository_id}><b>{item.full_name}</b>: {repositoryReason(item)}</p>)}<div className="modalActions span2"><button onClick={onClose} type="button">Cancel</button><button className="primary" disabled={!repository || !repositoryUsable(repository)} onClick={() => setStep("application")} type="button">Continue</button></div></div> : null}
    {!loading && !error && step === "application" && repository ? <form className="form" onSubmit={reviewApplication}><div className="span2 sourceSelection"><b>{repository.full_name}</b><span>{repository.claim_status === "active" ? "Already claimed by this project" : "Available - claim included in review"}</span></div><label>Service key<input autoComplete="off" className="field" onChange={(event) => setServiceKey(event.target.value)} pattern={serviceKeyPattern} placeholder="api" required spellCheck={false} value={serviceKey} /></label><label>Branch<input autoComplete="off" className="field" onChange={(event) => setBranch(event.target.value)} required value={branch} /></label><label>Project path / build context<input autoComplete="off" className="field" defaultValue="." name="build_context" required /></label><label>Dockerfile path<input autoComplete="off" className="field" defaultValue="Dockerfile" name="dockerfile" required /></label><label>Config path<input autoComplete="off" className="field" defaultValue=".opsi/opsi-cd.yaml" name="config_path" required /></label><label>Container port<input className="field" max="65535" min="1" name="container_port" required type="number" /></label><label>Health path<input autoComplete="off" className="field" defaultValue="/health" name="health_path" pattern="/.*" required /></label>{keyConflict ? <p className="repositoryConflict span2" role="alert">{keyConflict}</p> : null}<div className="modalActions span2"><button onClick={() => setStep("source")} type="button">Back</button><button className="primary" disabled={!serviceKey || Boolean(keyConflict)} type="submit">Review application</button></div></form> : null}
  </dialog>;
}

function repositoryUsable(repository: GitHubRepository) {
  return repository.status === "active" && !repository.archived && !repository.disabled && (repository.claim_status === "available" || repository.claim_status === "active");
}

function repositoryReason(repository: GitHubRepository) {
  if (repository.archived) return "archived";
  if (repository.disabled) return "disabled";
  if (repository.status !== "active") return `repository ${repository.status}`;
  if (repository.claim_status === "conflict") return "claimed by another project";
  if (repository.claim_status === "active") return "claimed by this project";
  return "available";
}
