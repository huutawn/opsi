"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Empty, Panel, StatePanel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { LocalClient } from "@/lib/api/local-client";
import type { BuildRecord, BuildRecordList } from "@/lib/contracts/registry";

type LoadState = "loading" | "ready" | "error";

export function BuildRecordsView({ console }: { console: ConsoleController }) {
  const client = useMemo(() => new LocalClient(), []);
  const projectID = console.state.project?.id ?? "";
  const [result, setResult] = useState<BuildRecordList>({ records: [] });
  const [selected, setSelected] = useState<BuildRecord | null>(null);
  const [state, setState] = useState<LoadState>("loading");
  const [message, setMessage] = useState("");
  const [filters, setFilters] = useState({ serviceKey: "", repositoryID: "", sha: "", status: "" });

  const load = useCallback(async (cursor = "") => {
    if (!projectID) {
      setResult({ records: [] });
      setState("ready");
      return;
    }
    setState("loading");
    setMessage("");
    try {
      const next = await client.buildRecords(projectID, { ...filters, cursor });
      setResult(next);
      setSelected((current) => next.records.find((record) => record.id === current?.id) ?? next.records[0] ?? null);
      setState("ready");
    } catch (error) {
      setState("error");
      setMessage((error as Error).message || "BuildRecord list is unavailable.");
    }
  }, [client, filters, projectID]);

  useEffect(() => { queueMicrotask(() => void load()); }, [load]);

  async function select(recordID: string) {
    if (!projectID) return;
    setMessage("");
    try {
      setSelected(await client.buildRecord(projectID, recordID));
    } catch (error) {
      setMessage((error as Error).message || "BuildRecord detail is unavailable.");
    }
  }

  function filter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setFilters({ serviceKey: field(form, "service_key"), repositoryID: field(form, "repository_id"), sha: field(form, "sha"), status: field(form, "status") });
  }

  if (state === "loading") return <StatePanel title="Loading BuildRecords" text="Reading OIDC-bound build metadata through the local backend." />;
  if (state === "error") return <StatePanel title="BuildRecords unavailable" text={message} retry={() => void load()} />;

  return (
    <section className="grid">
      {message ? <StatePanel title="BuildRecord request failed" text={message} retry={() => void load()} /> : null}
      <Panel title="Trusted build records">
        <p className="muted">Read-only metadata submitted by an authorized GitHub Actions workload. No deploy action is available here.</p>
        <form className="form buildRecordFilters" onSubmit={filter}>
          <label>Service key<input className="field" name="service_key" /></label>
          <label>Repository ID<input className="field" inputMode="numeric" name="repository_id" /></label>
          <label>Full source SHA<input className="field" name="sha" /></label>
          <label>Status<select className="select" name="status" defaultValue=""><option value="">Any</option><option value="succeeded">Succeeded</option></select></label>
          <button type="submit">Filter records</button>
        </form>
        {result.records.length ? (
          <div className="tableWrap"><table><thead><tr><th>Service</th><th>Repository / source</th><th>Workflow run</th><th>Artifact</th><th>Status</th></tr></thead><tbody>
            {result.records.map((record) => <tr key={record.id}>
              <td><button className="linkButton" type="button" onClick={() => void select(record.id)}>{record.service_key}</button><br /><span className="muted">{record.id}</span></td>
              <td>{record.repository_id}<br /><code>{short(record.workload.sha, 12)}</code></td>
              <td>{record.workload.workflow}<br /><span className="muted">#{record.workload.run_id} / attempt {record.workload.run_attempt}</span></td>
              <td><code>{record.build.oci_repository}</code><br /><code>{short(record.build.oci_digest, 22)}</code></td>
              <td><StatusBadge value={record.build.status} /></td>
            </tr>)}
          </tbody></table></div>
        ) : <Empty text="No BuildRecords match this project and filter." />}
        {result.next_cursor ? <button type="button" onClick={() => void load(result.next_cursor ?? "")}>Next page</button> : null}
      </Panel>
      <Panel title="BuildRecord detail">
        {selected ? <div className="specList">
          <Fact label="Service / binding" value={`${selected.service_key} / ${selected.active_binding_id}`} />
          <Fact label="Repository / owner" value={`${selected.repository_id} / ${selected.repository_owner_id}`} />
          <Fact label="Source" value={`${selected.workload.ref} @ ${selected.workload.sha}`} mono />
          <Fact label="Workflow" value={selected.workload.workflow_ref} mono />
          <Fact label="Reusable workflow" value={selected.workload.job_workflow_ref || "not used by this workflow contract"} mono />
          <Fact label="Run identity" value={`${selected.workload.run_id} / attempt ${selected.workload.run_attempt} / ${selected.workload.event_name}`} />
          <Fact label="Immutable artifact" value={`${selected.build.oci_repository}@${selected.build.oci_digest}`} mono />
          <Fact label="Platform" value={selected.build.platform} />
          <Fact label="Config hash" value={selected.build.config_hash} mono />
          <Fact label="Plan hash" value={selected.build.plan_hash || "not supplied"} mono />
          <Fact label="Provenance digest" value={selected.build.provenance_digest || "not supplied"} mono />
          <Fact label="Created" value={new Date(selected.created_at).toLocaleString()} />
        </div> : <Empty text="Select a BuildRecord to inspect its verified workload and immutable artifact identity." />}
      </Panel>
    </section>
  );
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div><span>{label}</span><b className={mono ? "monoWrap" : ""}>{value}</b></div>; }
function field(form: FormData, name: string) { return String(form.get(name) ?? "").trim(); }
function short(value: string, size: number) { return value.length > size ? `${value.slice(0, size)}...` : value; }
