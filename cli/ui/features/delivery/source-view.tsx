"use client";

import { useRef } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import { RepositoryCD } from "@/features/github/repository-cd";
import { SourceRiskPanel } from "@/features/dependencies/source-risk-panel";
import { ServiceFilter, type DeliveryViewProps } from "@/features/delivery/shared";
import { LocalClient } from "@/lib/api/local-client";
import type { GitHubBinding, GitHubRepository } from "@/lib/contracts/registry";

export function SourceView({ console, data, selectedService }: DeliveryViewProps) {
  const client = useRef(new LocalClient()).current;
  const projectID = console.state.project?.id ?? "";
  const activeBindings = data.bindings.filter(
    (binding) => binding.status === "active" && (!selectedService || binding.service_id === selectedService.id)
  );

  function connect(form: HTMLFormElement) {
    const installationID = Number(new FormData(form).get("installation_id"));
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "GitHub installation",
        targetID: String(installationID),
        operation: "claim",
        diff: ["installation: " + installationID, "Cloud verifies GitHub identity and installation authority"],
        risk: "Starts an external authorization redirect; no credential enters browser storage.",
      },
      async (key) => {
        const started = await client.startGitHubInstallationClaim(projectID, installationID, key);
        window.location.assign(started.authorization_url);
        return "GitHub authorization started by the Local backend.";
      }
    );
  }

  function claim(repository: GitHubRepository) {
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "repository",
        targetID: String(repository.repository_id),
        operation: "claim",
        diff: ["repository: " + repository.full_name, "numeric repository identity is authoritative"],
        risk: "Claims this repository for the selected project.",
      },
      async (key) => {
        await client.claimGitHubRepository(projectID, repository.repository_id, key);
        return "Repository ownership claimed by Cloud; refresh Source to load the factual inventory.";
      }
    );
  }

  function bind(form: HTMLFormElement) {
    const values = new FormData(form);
    const body = {
      service_id: String(values.get("service_id") || ""),
      repository_id: Number(values.get("repository_id")),
      service_key: String(values.get("service_key") || ""),
      config_path: String(values.get("config_path") || ".opsi/opsi-cd.yaml"),
    };
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "service binding",
        targetID: body.service_key,
        operation: "create",
        diff: ["service: " + body.service_id, "repository: " + body.repository_id, "service key: " + body.service_key],
        risk: "Creates the canonical repository-to-service delivery identity.",
      },
      async (key) => {
        await client.createGitHubBinding(projectID, body, key);
        return "Service binding created by Cloud; refresh Source to load the factual inventory.";
      }
    );
  }

  function remove(binding: GitHubBinding) {
    console.reviewMutation(
      {
        project: console.state.project?.name || projectID,
        targetType: "service binding",
        targetID: binding.id,
        operation: "remove",
        diff: ["service: " + binding.service_id, "repository: " + binding.repository_id, "service key: " + binding.service_key],
        risk: "Destructive: future BuildRecords can no longer use this binding.",
        confirmation: binding.id,
      },
      async (key) => {
        await client.removeGitHubBinding(projectID, binding.id, key);
        return "Service binding removed by Cloud.";
      }
    );
  }

  return (
    <div className="deliveryPage">
      <div className="deliveryToolbar">
        <ServiceFilter console={console} services={data.services} selected={selectedService} />
        <p aria-live="polite">{data.sourceError || "Repository, binding, monorepo, and policy identities are factual Local API data."}</p>
      </div>
      <ol className="setupRail" aria-label="Source setup flow">
        <li>Connect Installation</li>
        <li>Claim Repository</li>
        <li>Bind Service</li>
        <li>Configure Monorepo</li>
        <li>Preview Files</li>
        <li>Apply</li>
        <li>Review Policy</li>
      </ol>
      <section className="sourceSection">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">1–3 · Cloud Identity</p>
            <h2>Repository Ownership & Service Bindings</h2>
          </div>
        </div>
        <div className="sourceInventory">
          <div>
            <h3>Installations</h3>
            {data.installations.length ? (
              data.installations.map((installation) => (
                <div className="inventoryRow" key={installation.installation_id}>
                  <span>
                    <strong>{installation.account_login || "GitHub account"}</strong>
                    <small>Installation {installation.installation_id}</small>
                  </span>
                  <StatusBadge value={installation.suspended ? "failed" : installation.status} />
                </div>
              ))
            ) : (
              <Empty title="No installation connected" text="Connect a numeric GitHub App installation through the Local backend." />
            )}
            <details>
              <summary>Connect Installation</summary>
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  connect(event.currentTarget);
                }}
              >
                <label>
                  Installation ID
                  <input inputMode="numeric" min="1" name="installation_id" required type="number" />
                </label>
                <button type="submit">Review Connection</button>
              </form>
            </details>
          </div>
          <div>
            <h3>Repositories</h3>
            {data.repositories.length ? (
              data.repositories.map((repository) => (
                <div className="inventoryRow" key={repository.repository_id}>
                  <span>
                    <strong>{repository.full_name}</strong>
                    <small>
                      ID {repository.repository_id} · {repository.default_branch || "branch not reported"}
                    </small>
                  </span>
                  {repository.claim_status === "active" ? (
                    <StatusBadge value="healthy" label="Owned" />
                  ) : (
                    <button
                      disabled={repository.status !== "active" || repository.archived || repository.disabled || repository.claim_status === "conflict"}
                      onClick={() => claim(repository)}
                      type="button"
                    >
                      Claim
                    </button>
                  )}
                </div>
              ))
            ) : (
              <Empty title="No repository inventory" text="The Local API has not returned a usable GitHub repository inventory." />
            )}
          </div>
        </div>
        <div>
          <h3>Service Bindings</h3>
          {activeBindings.length ? (
            activeBindings.map((binding) => (
              <div className="bindingRow" key={binding.id}>
                <span>
                  <strong>{data.services.find((service) => service.id === binding.service_id)?.name || binding.service_id}</strong>
                  <small>{data.repositories.find((repository) => repository.repository_id === binding.repository_id)?.full_name || binding.repository_id}</small>
                </span>
                <code>{binding.service_key}</code>
                <span>{binding.config_path}</span>
                <button onClick={() => remove(binding)} type="button">
                  Remove
                </button>
              </div>
            ))
          ) : (
            <Empty title="No active service binding" text="One repository may bind web, api, and worker as separate canonical services." />
          )}
          <details>
            <summary>Bind Another Service</summary>
            <form
              className="deploymentForm"
              onSubmit={(event) => {
                event.preventDefault();
                bind(event.currentTarget);
              }}
            >
              <label>
                Service
                <select name="service_id" required>
                  {data.services
                    .filter((service) => service.type === "application")
                    .map((service) => (
                      <option key={service.id} value={service.id}>
                        {service.name}
                      </option>
                    ))}
                </select>
              </label>
              <label>
                Owned Repository
                <select name="repository_id" required>
                  {data.repositories
                    .filter((repository) => repository.claim_status === "active")
                    .map((repository) => (
                      <option key={repository.repository_id} value={repository.repository_id}>
                        {repository.full_name}
                      </option>
                    ))}
                </select>
              </label>
              <label>
                Service Key
                <input name="service_key" pattern="[a-z0-9](?:[a-z0-9]|-){0,62}" placeholder="api…" required />
              </label>
              <label>
                Config Path
                <input defaultValue=".opsi/opsi-cd.yaml" name="config_path" required />
              </label>
              <button type="submit">Review Service Binding</button>
            </form>
          </details>
        </div>
      </section>
      <section className="sourceSection">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">4–6 · Repository Intent</p>
            <h2>Monorepo CD Configuration</h2>
          </div>
        </div>
        <RepositoryCD console={console} />
      </section>
      <section className="sourceSection">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">7 · Cloud Policy</p>
            <h2>Deployment Policy</h2>
          </div>
          <button onClick={() => console.navigate({ view: "infrastructure", tab: "topology" })} type="button">
            Configure Policy
          </button>
        </div>
        {data.policies.length ? (
          <div className="policyList">
            {data.policies.map((policy) => (
              <details key={policy.id}>
                <summary>
                  <span>
                    <strong>{policy.policy.service_keys.join(", ")}</strong>
                    <small>
                      {policy.policy.environment_id} · revision {policy.revision}
                    </small>
                  </span>
                  <StatusBadge
                    value={policy.policy.enabled ? "healthy" : "unknown"}
                    label={policy.policy.enabled ? "Enabled" : "Disabled"}
                  />
                </summary>
                <dl className="evidenceGrid">
                  <div>
                    <dt>Automatic Main</dt>
                    <dd>{policy.policy.automatic_main ? "Enabled" : "Disabled"}</dd>
                  </div>
                  <div>
                    <dt>Preview</dt>
                    <dd>{policy.policy.preview?.enabled ? "Enabled" : "Disabled"}</dd>
                  </div>
                  <div>
                    <dt>Allowed Refs</dt>
                    <dd>{policy.policy.allowed_git_refs.join(", ")}</dd>
                  </div>
                  <div>
                    <dt>Allowed Workflow</dt>
                    <dd>{policy.policy.workflow_refs.join(", ")}</dd>
                  </div>
                  <div>
                    <dt>Environment / Runtime</dt>
                    <dd>
                      {policy.policy.environment_id} / {policy.policy.allowed_runtime_ids.join(", ")}
                    </dd>
                  </div>
                  <div>
                    <dt>Immutable Repositories</dt>
                    <dd>{policy.policy.allowed_oci_repositories.join(", ")}</dd>
                  </div>
                  <div>
                    <dt>Revision / State Hash</dt>
                    <dd>
                      <code>
                        {policy.revision} / {policy.state_hash}
                      </code>
                    </dd>
                  </div>
                </dl>
                <p className="truthCallout">Policy enabled means delivery is authorized under these constraints. It does not mean CD succeeded.</p>
              </details>
            ))}
          </div>
        ) : (
          <Empty title="No deployment policy reported" text="Configure topology and policy before a BuildRecord can become an eligible deployment." />
        )}
      </section>
      {selectedService ? (
        <section className="sourceSection">
          <div className="sectionHeading">
            <div>
              <p className="eyebrow">Security & Configuration Scans</p>
              <h2>Source Risk Analysis · {selectedService.name}</h2>
            </div>
          </div>
          <SourceRiskPanel applicationID={selectedService.id} projectID={projectID} />
        </section>
      ) : null}
    </div>
  );
}
