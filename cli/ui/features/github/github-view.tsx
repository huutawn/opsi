"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { GitHubBinding, GitHubInstallation, GitHubRepository } from "@/lib/contracts/registry";
import { RepositoryCD } from "@/features/github/repository-cd";

type GitHubState = {
  status: "idle" | "loading" | "ready" | "error";
  message: string;
  installations: GitHubInstallation[];
  repositories: GitHubRepository[];
  bindings: GitHubBinding[];
};

const initialState: GitHubState = {
  status: "idle",
  message: "",
  installations: [],
  repositories: [],
  bindings: [],
};

export function GitHubView({ console }: { console: ConsoleController }) {
  const project = console.state.project;
  const client = useMemo(() => new LocalClient(), []);
  const [state, setState] = useState(initialState);

  const load = useCallback(async () => {
    if (!project) return;
    setState((current) => ({ ...current, status: "loading", message: "" }));
    try {
      const [installations, repositories, bindings] = await Promise.all([
        client.githubInstallations(project.id),
        client.githubRepositories(project.id),
        client.githubBindings(project.id),
      ]);
      setState((current) => ({
        ...current,
        status: "ready",
        message: "",
        installations: installations.installations ?? [],
        repositories: repositories.repositories ?? [],
        bindings: bindings.bindings ?? [],
      }));
    } catch (error) {
      setState((current) => ({ ...current, status: "error", message: errorMessage(error) }));
    }
  }, [client, project]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!project) return <StatePanel title="GitHub App" text="Select a project before connecting a GitHub App installation." />;
  if (state.status === "loading" && state.installations.length === 0)
    return <StatePanel title="Loading GitHub state" text="Reading installation, repository, and binding inventory through the local API." />;
  if (state.status === "error" && state.installations.length === 0)
    return <StatePanel title="GitHub state unavailable" text={state.message} retry={() => void load()} />;

  const projectID = project.id;
  const projectName = project.name;

  async function connect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const installationID = Number(form.get("installation_id"));
    console.reviewMutation(
      { project: projectName, targetType: "GitHub installation", targetID: String(installationID), operation: "claim", diff: [`installation: ${installationID}`, "Cloud verifies the GitHub identity and installation"], risk: "Starts an external GitHub authorization redirect; no credential enters browser state." },
      async (key) => {
        const started = await client.startGitHubInstallationClaim(projectID, installationID, key);
        window.location.assign(started.authorization_url);
        return "GitHub authorization started by the Local backend.";
      },
    );
  }

  async function createBinding(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const serviceID = String(form.get("service_id") ?? "");
    const repositoryID = Number(form.get("repository_id"));
    const serviceKey = String(form.get("service_key") ?? "");
    const configPath = String(form.get("config_path") ?? ".opsi/opsi-cd.yaml");
    console.reviewMutation(
      { project: projectName, targetType: "service binding", targetID: serviceKey, operation: "create", diff: [`repository: ${repositoryID}`, `service: ${serviceID}`, `config: ${configPath}`], risk: "Associates immutable CD intent with the selected repository." },
      async (key) => {
        await client.createGitHubBinding(projectID, { service_id: serviceID, repository_id: repositoryID, service_key: serviceKey, config_path: configPath }, key);
        await load();
        return "Service binding created by Cloud.";
      },
    );
  }

  return (
    <section className="grid">
      {state.message ? <StatePanel title="GitHub action failed" text={state.message} retry={() => void load()} /> : null}

      <Panel title="GitHub App connection">
        <p className="muted">Project: {projectName} ({projectID}). Authorization returns to the local CLI backend; credentials never enter browser storage.</p>
        <form className="form" onSubmit={(event) => void connect(event)}>
          <label>
            Installation ID
            <input className="field" min="1" name="installation_id" required type="number" />
          </label>
          <button className="primary" type="submit">
            Authorize and claim
          </button>
        </form>
        <InstallationTable installations={state.installations} />
      </Panel>

      <Panel title="Repository inventory">
        {state.repositories.length ? (
          <div className="tableWrap">
            <table>
              <thead>
                <tr><th>Repository</th><th>Inventory</th><th>Ownership</th><th>Action</th></tr>
              </thead>
              <tbody>
                {state.repositories.map((repository) => (
                  <tr key={repository.repository_id}>
                    <td><b>{repository.full_name}</b><br /><span className="muted">ID {repository.repository_id} · installation {repository.installation_id}</span></td>
                    <td><StatusBadge value={repository.archived ? "archived" : repository.disabled ? "disabled" : repository.status} /></td>
                    <td><StatusBadge value={repository.claim_status || "available"} /></td>
                    <td className="actions">
                      {repository.claim_status === "active" ? (
                        <button
                          onClick={() => {
                            console.reviewMutation(
                              { project: projectName, targetType: "repository", targetID: String(repository.repository_id), operation: "release ownership", diff: [`repository: ${repository.full_name}`, "active bindings must already be absent"], risk: "Destructive: this project will no longer own the repository.", confirmation: String(repository.repository_id) },
                              async (key) => { await client.releaseGitHubRepository(projectID, repository.repository_id, key); await load(); return "Repository ownership released by Cloud."; },
                            );
                          }}
                          type="button"
                        >Release</button>
                      ) : (
                        <button
                          disabled={repository.status !== "active" || repository.archived || repository.disabled || repository.claim_status === "conflict"}
                          onClick={() => console.reviewMutation(
                            { project: projectName, targetType: "repository", targetID: String(repository.repository_id), operation: "claim ownership", diff: [`repository: ${repository.full_name}`, "numeric repository identity is authoritative"], risk: "Claims this repository for the selected project." },
                            async (key) => { await client.claimGitHubRepository(projectID, repository.repository_id, key); await load(); return "Repository claimed by this project."; },
                          )}
                          type="button"
                        >{repository.claim_status === "conflict" ? "Owned elsewhere" : "Claim"}</button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : <Empty text="No repositories are visible. Install the App with Only select repositories, then authorize the numeric installation ID." />}
      </Panel>

      <Panel title="Service bindings">
        <form className="form" onSubmit={(event) => void createBinding(event)}>
          <label>Service<select className="select" name="service_id" required>{console.state.services.map((service) => <option key={service.id} value={service.id}>{service.name} ({service.id})</option>)}</select></label>
          <label>Repository<select className="select" name="repository_id" required>{state.repositories.filter((repository) => repository.claim_status === "active").map((repository) => <option key={repository.repository_id} value={repository.repository_id}>{repository.full_name}</option>)}</select></label>
          <label>Service key<input className="field" name="service_key" pattern="[a-z0-9][a-z0-9-]{0,62}" placeholder="api" required /></label>
          <label>Config path<input className="field" defaultValue=".opsi/opsi-cd.yaml" name="config_path" required /></label>
          <button className="primary" disabled={!console.state.services.length} type="submit">Create binding</button>
        </form>
        <BindingTable
          bindings={state.bindings}
          project={{ id: projectID }}
          repositories={state.repositories}
          services={console.state.services}
          remove={(binding) => {
            const repository = state.repositories.find((item) => item.repository_id === binding.repository_id);
            const service = console.state.services.find((item) => item.id === binding.service_id);
            console.reviewMutation(
              { project: projectName, targetType: "service binding", targetID: binding.id, operation: "remove", diff: [`repository: ${repository?.full_name ?? binding.repository_id}`, `service: ${service?.name ?? binding.service_id}`, `service key: ${binding.service_key}`], risk: "Destructive: this service will no longer be associated with the repository.", confirmation: binding.id },
              async (key) => { await client.removeGitHubBinding(projectID, binding.id, key); await load(); return "Service binding removed by Cloud."; },
            );
          }}
        />
      </Panel>

      <RepositoryCD console={console} />
    </section>
  );
}

function InstallationTable({ installations }: { installations: GitHubInstallation[] }) {
  if (!installations.length) return <Empty text="No installation is claimed by this project yet." />;
  return <div className="tableWrap"><table><tbody>{installations.map((installation) => <tr key={installation.installation_id}><td><b>{installation.account_login || "GitHub account"}</b><br /><span className="muted">Installation {installation.installation_id}</span></td><td><StatusBadge value={installation.suspended ? "suspended" : installation.status} /></td></tr>)}</tbody></table></div>;
}

function BindingTable({ bindings, project, repositories, services, remove }: { bindings: GitHubBinding[]; project: { id: string }; repositories: GitHubRepository[]; services: Array<{ id: string; name: string }>; remove: (binding: GitHubBinding) => void }) {
  const active = bindings.filter((binding) => binding.status !== "removed");
  if (!active.length) return <Empty text="No active service bindings. Create distinct service keys such as api and web for the same repository." />;
  return <div className="tableWrap"><table><thead><tr><th>Service</th><th>Repository</th><th>Key / config</th><th>Action</th></tr></thead><tbody>{active.map((binding) => <tr key={binding.id}><td>{services.find((service) => service.id === binding.service_id)?.name ?? binding.service_id}</td><td>{repositories.find((repository) => repository.repository_id === binding.repository_id)?.full_name ?? binding.repository_id}</td><td><b>{binding.service_key}</b><br /><span className="muted">{binding.config_path} · {binding.id} · {project.id}</span></td><td><button onClick={() => remove(binding)} type="button">Remove</button></td></tr>)}</tbody></table></div>;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "GitHub operation failed; retry through the local API.";
}
