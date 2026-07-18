"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { GitHubBinding, GitHubInstallation, GitHubRepository } from "@/lib/contracts/registry";

type GitHubState = {
  status: "idle" | "loading" | "ready" | "error";
  message: string;
  notice: string;
  installations: GitHubInstallation[];
  repositories: GitHubRepository[];
  bindings: GitHubBinding[];
  busy: string;
};

const initialState: GitHubState = {
  status: "idle",
  message: "",
  notice: "",
  installations: [],
  repositories: [],
  bindings: [],
  busy: "",
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

  async function mutate(key: string, operation: () => Promise<unknown>, notice: string) {
    setState((current) => ({ ...current, busy: key, message: "", notice: "" }));
    try {
      await operation();
      setState((current) => ({ ...current, notice }));
      await load();
    } catch (error) {
      setState((current) => ({ ...current, message: errorMessage(error) }));
    } finally {
      setState((current) => ({ ...current, busy: "" }));
    }
  }

  async function connect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const installationID = Number(form.get("installation_id"));
    setState((current) => ({ ...current, busy: "connect", message: "", notice: "Opening GitHub authorization." }));
    try {
      const started = await client.startGitHubInstallationClaim(projectID, installationID);
      window.location.assign(started.authorization_url);
    } catch (error) {
      setState((current) => ({ ...current, busy: "", notice: "", message: errorMessage(error) }));
    }
  }

  async function createBinding(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await mutate(
      "binding-create",
      () =>
        client.createGitHubBinding(projectID, {
          service_id: String(form.get("service_id") ?? ""),
          repository_id: Number(form.get("repository_id")),
          service_key: String(form.get("service_key") ?? ""),
          config_path: String(form.get("config_path") ?? ".opsi/opsi-cd.yaml"),
        }),
      "Service binding created. Multiple service keys can share the same numeric repository.",
    );
  }

  return (
    <section className="grid">
      {state.message ? <StatePanel title="GitHub action failed" text={state.message} retry={() => void load()} /> : null}
      {state.notice ? <div className="notice">{state.notice}</div> : null}

      <Panel title="GitHub App connection">
        <p className="muted">Project: {projectName} ({projectID}). Authorization returns to the local CLI backend; credentials never enter browser storage.</p>
        <form className="form" onSubmit={(event) => void connect(event)}>
          <label>
            Installation ID
            <input className="field" min="1" name="installation_id" required type="number" />
          </label>
          <button className="primary" disabled={state.busy === "connect"} type="submit">
            {state.busy === "connect" ? "Waiting for GitHub" : "Authorize and claim"}
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
                          disabled={state.busy === `release-${repository.repository_id}`}
                          onClick={() => {
                            const consequence = `Release repository ownership from project ${projectName} (${projectID}).\nRepository: ${repository.full_name} (${repository.repository_id}).\nConsequence: release is rejected while any active service binding remains.`;
                            if (window.confirm(consequence)) void mutate(`release-${repository.repository_id}`, () => client.releaseGitHubRepository(projectID, repository.repository_id), "Repository ownership released.");
                          }}
                          type="button"
                        >Release</button>
                      ) : (
                        <button
                          disabled={repository.status !== "active" || repository.archived || repository.disabled || repository.claim_status === "conflict" || state.busy === `claim-${repository.repository_id}`}
                          onClick={() => void mutate(`claim-${repository.repository_id}`, () => client.claimGitHubRepository(projectID, repository.repository_id), "Repository claimed by this project.")}
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
          <button className="primary" disabled={state.busy === "binding-create" || !console.state.services.length} type="submit">Create binding</button>
        </form>
        <BindingTable
          bindings={state.bindings}
          busy={state.busy}
          project={{ id: projectID }}
          repositories={state.repositories}
          services={console.state.services}
          remove={(binding) => {
            const repository = state.repositories.find((item) => item.repository_id === binding.repository_id);
            const service = console.state.services.find((item) => item.id === binding.service_id);
            const consequence = `Remove service binding from project ${projectName} (${projectID}).\nRepository: ${repository?.full_name ?? binding.repository_id}.\nService: ${service?.name ?? binding.service_id} (${binding.service_id}).\nBinding: ${binding.id}; service key: ${binding.service_key}.\nConsequence: this service will no longer be associated with the repository.`;
            if (window.confirm(consequence)) void mutate(`binding-${binding.id}`, () => client.removeGitHubBinding(projectID, binding.id), "Service binding removed.");
          }}
        />
      </Panel>
    </section>
  );
}

function InstallationTable({ installations }: { installations: GitHubInstallation[] }) {
  if (!installations.length) return <Empty text="No installation is claimed by this project yet." />;
  return <div className="tableWrap"><table><tbody>{installations.map((installation) => <tr key={installation.installation_id}><td><b>{installation.account_login || "GitHub account"}</b><br /><span className="muted">Installation {installation.installation_id}</span></td><td><StatusBadge value={installation.suspended ? "suspended" : installation.status} /></td></tr>)}</tbody></table></div>;
}

function BindingTable({ bindings, busy, project, repositories, services, remove }: { bindings: GitHubBinding[]; busy: string; project: { id: string }; repositories: GitHubRepository[]; services: Array<{ id: string; name: string }>; remove: (binding: GitHubBinding) => void }) {
  const active = bindings.filter((binding) => binding.status !== "removed");
  if (!active.length) return <Empty text="No active service bindings. Create distinct service keys such as api and web for the same repository." />;
  return <div className="tableWrap"><table><thead><tr><th>Service</th><th>Repository</th><th>Key / config</th><th>Action</th></tr></thead><tbody>{active.map((binding) => <tr key={binding.id}><td>{services.find((service) => service.id === binding.service_id)?.name ?? binding.service_id}</td><td>{repositories.find((repository) => repository.repository_id === binding.repository_id)?.full_name ?? binding.repository_id}</td><td><b>{binding.service_key}</b><br /><span className="muted">{binding.config_path} · {binding.id} · {project.id}</span></td><td><button disabled={busy === `binding-${binding.id}`} onClick={() => remove(binding)} type="button">Remove</button></td></tr>)}</tbody></table></div>;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "GitHub operation failed; retry through the local API.";
}
