"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Icon, Input, Select, StatusBadge } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import { hashExposure, type DeploymentJob, type DeploymentPlan, type DeploymentRunResult, type ExposureMutationRequest } from "@/lib/contracts/registry";

type PublicRouteProps = {
  canMutate: boolean;
  client: LocalClient;
  plan: DeploymentPlan;
  projectID: string;
  result: DeploymentRunResult;
};

type PublishableApplication = DeploymentRunResult["applications"][number] & { deployment_job_id: string; container_port: number };

export function PublicRoute({ canMutate, client, plan, projectID, result }: PublicRouteProps) {
  const [serviceID, setServiceID] = useState("");
  const [hostname, setHostname] = useState("");
  const [rollout, setRollout] = useState<DeploymentJob | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const candidates = useMemo<PublishableApplication[]>(() => result.applications.filter((application): application is PublishableApplication => Boolean(application.deployment_job_id && application.container_port)), [result.applications]);
  const selected = candidates.find((application) => application.service_id === serviceID);
  const defaultHostname = plan.target.hostname?.trim().toLowerCase() || "";
  const resolvedHostname = hostname.trim().toLowerCase() || defaultHostname;
  const hostnameError = hostname.trim() ? validatePublicHostname(hostname) : "";

  useEffect(() => {
    if (!rollout || rolloutReachedTerminalState(rollout)) return;
    const timer = window.setInterval(() => {
      void client.deployment(projectID, rollout.id).then(setRollout).catch(() => undefined);
    }, 2500);
    return () => window.clearInterval(timer);
  }, [client, projectID, rollout]);

  async function publish() {
    if (!selected || !resolvedHostname || hostnameError) return;
    setBusy(true);
    setFailure("");
    try {
      const deploymentID = `dep-exposure-${crypto.randomUUID()}`;
      const base: Omit<ExposureMutationRequest, "expected_state_hash"> = {
        schema_version: "opsi.exposure_mutation/v1",
        base_deployment_job_id: selected.deployment_job_id,
        exposure: {
          schema_version: "opsi.exposure_spec/v1",
          project_id: projectID,
          environment_id: plan.target.environment_id,
          runtime_id: plan.target.runtime_id || "",
          service_key: selected.service_key,
          deployment_job_id: deploymentID,
          hostname: resolvedHostname,
          path: "/",
          service_port: selected.container_port,
          tls: { mode: "disabled" },
          spec_hash: "",
        },
      };
      base.exposure.spec_hash = await hashExposure(base.exposure);
      const preview = await client.exposurePreview(projectID, base);
      if (!preview.eligible) throw new Error(preview.message || "The requested public route cannot be published.");
      const job = await client.exposureApply(projectID, { ...base, expected_state_hash: preview.state_hash }, crypto.randomUUID());
      setRollout(job);
    } catch (cause) {
      setFailure(routeFailure(cause));
    } finally {
      setBusy(false);
    }
  }

  if (candidates.length === 0) return <p className="mt-4 text-sm text-on-surface-variant">No application has a successful deployment record that can be published.</p>;
  return (
    <section aria-labelledby="public-route-title" className="mt-4 border border-outline-variant/30 bg-surface-container p-4">
      <p className="text-xs font-medium uppercase tracking-wider text-secondary">Public route</p>
      <h3 className="mt-1 text-base font-semibold" id="public-route-title">Publish or update one running service</h3>
      <p className="mt-1 text-sm text-on-surface-variant">Choose the application users should reach. You can replace its hostname after an earlier publish. Opsi previews and applies one canonical exposure rollout; it does not redeploy the source.</p>
      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        <label className="text-sm font-medium" htmlFor="public-route-service">Running service
          <Select disabled={!canMutate || busy} id="public-route-service" onChange={(event) => setServiceID(event.target.value)} value={serviceID}>
            <option value="">Choose a service</option>
            {candidates.map((application) => <option key={application.service_id} value={application.service_id}>{application.service_key} · port {application.container_port}</option>)}
          </Select>
        </label>
        <label className="text-sm font-medium" htmlFor="public-route-hostname">Override hostname (optional)
          <Input aria-describedby="public-route-hostname-help" aria-invalid={Boolean(hostnameError)} disabled={!canMutate || busy} id="public-route-hostname" onChange={(event) => setHostname(event.target.value)} placeholder={defaultHostname || "app.example.com"} value={hostname} />
        </label>
      </div>
      <p className="mt-2 text-xs text-on-surface-variant" id="public-route-hostname-help">{defaultHostname ? <>The deployment default is <span className="font-mono">{defaultHostname}</span>. Override it later with a public DNS name such as <span className="font-mono">tcip.103.252.137.163.nip.io</span>.</> : <>No default hostname is configured. Enter a public DNS name such as <span className="font-mono">tcip.103.252.137.163.nip.io</span>.</>} TLS is not configured for this route, so the published URL uses HTTP.</p>
      {hostnameError && <p className="mt-2 text-sm text-error" role="alert">{hostnameError}</p>}
      {failure && <p className="mt-3 border border-status-failed/40 bg-error-container/10 p-3 text-sm text-error" role="alert">{failure}</p>}
      {rollout && <RouteRollout hostname={resolvedHostname} rollout={rollout} />}
      {canMutate ? <Button className="mt-4" disabled={busy || !selected || !resolvedHostname || Boolean(hostnameError)} onClick={() => void publish()}>{busy ? "Applying route…" : "Publish or update service"}</Button> : <p className="mt-4 text-sm text-on-surface-variant">Your role can inspect this deployment but cannot publish a route.</p>}
    </section>
  );
}

function rolloutReachedTerminalState(rollout: DeploymentJob) {
  return ["succeeded", "failed", "cancelled"].includes(rollout.status) || ["succeeded", "failed", "rolled_back", "rollback_failed", "cleaned"].includes(rollout.rollout_state || "");
}

function RouteRollout({ hostname, rollout }: { hostname: string; rollout: DeploymentJob }) {
  const succeeded = rollout.status === "succeeded" && rollout.rollout_state === "succeeded";
  const failed = ["failed", "rolled_back", "cancelled"].includes(rollout.status);
  return <div className="mt-4 border border-outline-variant/30 p-3 text-sm" role="status"><div className="flex flex-wrap items-center justify-between gap-2"><p className="font-medium">Route rollout</p><StatusBadge status={succeeded ? "ready" : failed ? "failed" : "in_progress"} value={succeeded ? "ready" : failed ? "failed" : "in_progress"} /></div>{succeeded ? <a className="mt-2 inline-flex min-h-10 items-center gap-2 text-primary underline underline-offset-4" href={`http://${hostname}`} rel="noreferrer" target="_blank"><Icon name="open_in_new" />http://{hostname}</a> : <p className="mt-2 text-on-surface-variant">{failed ? "The route was not applied. Review the rollout in Technical details before trying again." : "The Agent is applying and verifying the route."}</p>}</div>;
}

function validatePublicHostname(value: string) {
  const hostname = value.toLowerCase();
  if (!hostname) return "A public hostname is required.";
  if (hostname !== hostname.trim() || hostname.includes(":") || hostname.includes("/") || hostname.includes("?") || hostname.includes("#") || hostname.includes("*") || hostname === "localhost" || hostname.endsWith(".localhost")) return "Enter a DNS hostname without a scheme, port, path, wildcard, or whitespace.";
  const labels = hostname.split(".");
  if (labels.length < 2 || labels.some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) return "Enter a public DNS hostname such as app.example.com.";
  return "";
}

function routeFailure(cause: unknown) {
  const error = cause as LocalAPIError;
  return error.message || "The public route could not be created. Refresh and review the route state before retrying.";
}
