"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import {
  LocalClient,
  type RepositoryCDConfig,
  type RepositoryCDPlan,
  type RepositoryCDService,
  type RepositoryMutationPreview,
} from "@/lib/api/local-client";

type Draft = {
  key: string;
  context: string;
  dockerfile: string;
  platform: string;
  watchPaths: string;
  sharedPaths: string;
  dependencies: string;
  branch: string;
  preview: boolean;
};

const emptyDraft: Draft = {
  key: "",
  context: ".",
  dockerfile: "Dockerfile",
  platform: "linux/amd64",
  watchPaths: "",
  sharedPaths: "",
  dependencies: "",
  branch: "main",
  preview: false,
};

export function RepositoryCD() {
  const client = useMemo(() => new LocalClient(), []);
  const [config, setConfig] = useState<RepositoryCDConfig | null>(null);
  const [configHash, setConfigHash] = useState("");
  const [draft, setDraft] = useState(emptyDraft);
  const [preview, setPreview] = useState<RepositoryMutationPreview | null>(null);
  const [applyKey, setApplyKey] = useState("");
  const [plan, setPlan] = useState<RepositoryCDPlan | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "previewing" | "applying" | "success" | "error">("loading");
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    setStatus("loading");
    setMessage("");
    try {
      const response = await client.repositoryCDConfig();
      setConfig(response.config);
      setConfigHash(response.config_hash);
      setStatus("ready");
    } catch (error) {
      setStatus("ready");
      setMessage(errorMessage(error));
    }
  }, [client]);

  useEffect(() => {
    let active = true;
    void client.repositoryCDConfig().then((response) => {
      if (!active) return;
      setConfig(response.config);
      setConfigHash(response.config_hash);
      setStatus("ready");
    }).catch((error: unknown) => {
      if (!active) return;
      setStatus("ready");
      setMessage(errorMessage(error));
    });
    return () => { active = false; };
  }, [client]);

  function edit(service: RepositoryCDService) {
    setDraft({
      key: service.key,
      context: service.build.context,
      dockerfile: service.build.dockerfile,
      platform: service.build.platform,
      watchPaths: service.watch_paths.join(", "),
      sharedPaths: service.shared_paths.join(", "),
      dependencies: service.dependencies.join(", "),
      branch: service.deploy.production.branches[0] ?? "main",
      preview: service.deploy.preview.enabled,
    });
    setPreview(null);
    setApplyKey("");
  }

  async function previewMutation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setStatus("previewing");
    setMessage("");
    setPreview(null);
    try {
      const result = await client.previewRepositoryMutation(toService(draft));
      setPreview(result);
      setApplyKey(crypto.randomUUID());
      setStatus("ready");
    } catch (error) {
      setStatus("error");
      setMessage(errorMessage(error));
    }
  }

  async function apply() {
    if (!preview) return;
    const changed = preview.files.filter((file) => file.action !== "unchanged").map((file) => `${file.action}: ${file.path}`).join("\n");
    if (!window.confirm(`Apply the reviewed repository changes?\n${changed || "No file changes"}`)) return;
    setStatus("applying");
    setMessage("");
    try {
      const result = await client.applyRepositoryMutation(toService(draft), preview.preview_hash, applyKey);
      setPreview(result);
      setConfig(result.config);
      setConfigHash(result.config_hash);
      setStatus("success");
    } catch (error) {
      setStatus("error");
      setMessage(errorMessage(error));
    }
  }

  async function previewPlan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setMessage("");
    try {
      setPlan(await client.previewRepositoryPlan({
        event: String(form.get("event")) as RepositoryCDPlan["event"],
        base: String(form.get("base") ?? "").trim(),
        head: String(form.get("head") ?? "").trim(),
      }));
    } catch (error) {
      setMessage(errorMessage(error));
    }
  }

  if (status === "loading") return <StatePanel title="Loading repository CD" text="Validating the repository-local Opsi config through the local API." />;

  return (
    <section className="grid">
      {message ? <StatePanel title="Repository CD action failed" text={message} retry={() => void load()} /> : null}
      {status === "success" ? <div className="notice">Repository config and workflow were written atomically. A repeated apply reports unchanged files.</div> : null}

      <Panel title="Monorepo services">
        <p className="muted">Config hash: {configHash || "not available"}. This file contains repository intent only—no project, Cloud, node, runtime, or credential identity.</p>
        {config?.services.length ? (
          <div className="tableWrap"><table><thead><tr><th>Service</th><th>Build</th><th>Watch / shared</th><th>Dependencies</th><th /></tr></thead><tbody>
            {config.services.map((service) => <tr key={service.key}>
              <td><b>{service.key}</b><br /><StatusBadge value={service.deploy.preview.enabled ? "preview enabled" : "production"} /></td>
              <td>{service.build.context}<br /><span className="muted">{service.build.dockerfile} · {service.build.platform}</span></td>
              <td>{[...service.watch_paths, ...service.shared_paths].join(", ") || "context only"}</td>
              <td>{service.dependencies.join(", ") || "none"}</td>
              <td><button type="button" onClick={() => edit(service)}>Edit</button></td>
            </tr>)}
          </tbody></table></div>
        ) : <Empty text="No valid v2 config is present yet. Preview a first service to create it." />}
      </Panel>

      <Panel title="Add or update one service">
        <form className="form" onSubmit={(event) => void previewMutation(event)}>
          <label>Service key<input className="field" required value={draft.key} onChange={(event) => setDraft({ ...draft, key: event.target.value })} /></label>
          <label>Build context<input className="field" required value={draft.context} onChange={(event) => setDraft({ ...draft, context: event.target.value })} /></label>
          <label>Dockerfile<input className="field" required value={draft.dockerfile} onChange={(event) => setDraft({ ...draft, dockerfile: event.target.value })} /></label>
          <label>Platform<input className="field" required value={draft.platform} onChange={(event) => setDraft({ ...draft, platform: event.target.value })} /></label>
          <label>Watch paths<input className="field" placeholder="services/api, schemas" value={draft.watchPaths} onChange={(event) => setDraft({ ...draft, watchPaths: event.target.value })} /></label>
          <label>Shared paths<input className="field" placeholder="shared" value={draft.sharedPaths} onChange={(event) => setDraft({ ...draft, sharedPaths: event.target.value })} /></label>
          <label>Dependencies<input className="field" placeholder="api" value={draft.dependencies} onChange={(event) => setDraft({ ...draft, dependencies: event.target.value })} /></label>
          <label>Production branch<input className="field" required value={draft.branch} onChange={(event) => setDraft({ ...draft, branch: event.target.value })} /></label>
          <label><input type="checkbox" checked={draft.preview} onChange={(event) => setDraft({ ...draft, preview: event.target.checked })} /> Enable pull-request preview intent</label>
          <button className="primary" disabled={status === "previewing" || status === "applying"} type="submit">{status === "previewing" ? "Validating" : "Preview config and workflow"}</button>
        </form>
        {preview ? <div className="grid">
          <p><b>Config hash:</b> {preview.config_hash} {preview.migrated_v1 ? "· v1 migration preview" : ""}</p>
          <pre>{preview.config_diff || "Config unchanged"}</pre>
          <pre>{preview.workflow_diff || "Workflow unchanged"}</pre>
          <button className="primary" disabled={status === "applying"} onClick={() => void apply()} type="button">{status === "applying" ? "Applying" : status === "error" ? "Retry apply" : "Apply reviewed changes"}</button>
        </div> : null}
      </Panel>

      <Panel title="Affected-service preview">
        <form className="form" onSubmit={(event) => void previewPlan(event)}>
          <label>Event<select className="select" name="event" defaultValue="push"><option value="push">Push</option><option value="pull_request">Pull request</option><option value="merge">Merge</option><option value="initial">Initial build</option></select></label>
          <label>Base commit<input className="field" name="base" placeholder="40-character commit ID" /></label>
          <label>Head commit<input className="field" name="head" placeholder="40-character commit ID" /></label>
          <button className="primary" type="submit">Preview affected services</button>
        </form>
        {plan ? <div className="grid">
          <p><StatusBadge value={plan.full_build ? "full build fallback" : "trusted diff"} /> {plan.explanation}</p>
          <p><b>Affected:</b> {plan.affected_service_keys.join(", ") || "none"}<br /><b>Config hash:</b> {plan.config_hash}<br /><b>Plan hash:</b> {plan.plan_hash}</p>
          {plan.services.map((service) => <div key={service.key}><b>{service.key}</b>{service.reasons.map((reason) => <p className="muted" key={`${reason.code}-${reason.path ?? reason.dependency ?? "global"}`}>{reason.code}: {reason.explanation}</p>)}</div>)}
        </div> : null}
      </Panel>
    </section>
  );
}

function toService(draft: Draft): RepositoryCDService {
  return {
    key: draft.key.trim(),
    build: { context: draft.context.trim(), dockerfile: draft.dockerfile.trim(), platform: draft.platform.trim() },
    watch_paths: splitList(draft.watchPaths),
    shared_paths: splitList(draft.sharedPaths),
    dependencies: splitList(draft.dependencies),
    deploy: {
      production: { enabled: true, branches: [draft.branch.trim()] },
      preview: { enabled: draft.preview, pull_requests: draft.preview },
    },
  };
}

function splitList(value: string) { return value.split(",").map((item) => item.trim()).filter(Boolean); }
function errorMessage(error: unknown) { return error instanceof Error ? error.message : "Repository CD operation failed."; }
