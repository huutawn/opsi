"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Icon, Input, Select, StatusBadge } from "@/components/ui/primitives";
import { publicHostname, publicSubdomainFromHostname, publicSubdomainSuffix, validatePublicSubdomain } from "@/features/deploy/public-subdomain";
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
  const selectedSubdomain = hostname || publicSubdomainFromHostname(defaultHostname);
  const hostnameError = validatePublicSubdomain(selectedSubdomain);
  const resolvedHostname = hostnameError ? "" : publicHostname(selectedSubdomain);

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
		  // Cloud owns suffix expansion and rejects client-supplied FQDNs.
		  hostname: selectedSubdomain,
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
        <label className="text-sm font-medium" htmlFor="public-route-hostname">Public subdomain
          <div className="mt-1 flex items-center"><Input aria-describedby="public-route-hostname-help" aria-invalid={Boolean(hostnameError)} className="rounded-r-none" disabled={!canMutate || busy} id="public-route-hostname" onChange={(event) => setHostname(event.target.value)} placeholder={publicSubdomainFromHostname(defaultHostname) || "tcip"} value={hostname} /><span className="min-h-10 border border-l-0 border-outline-variant/40 bg-surface-container-low px-3 py-2 font-mono text-xs text-on-surface-variant">.{publicSubdomainSuffix}</span></div>
        </label>
      </div>
      <p className="mt-2 text-xs text-on-surface-variant" id="public-route-hostname-help">{defaultHostname ? <>Current deployment subdomain: <span className="font-mono">{defaultHostname}</span>.</> : <>Choose the public subdomain to publish.</>} Cloudflare serves the public URL over HTTPS; Opsi verifies the VPS origin over HTTP.</p>
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
  return <div className="mt-4 border border-outline-variant/30 p-3 text-sm" role="status"><div className="flex flex-wrap items-center justify-between gap-2"><p className="font-medium">Route rollout</p><StatusBadge status={succeeded ? "ready" : failed ? "failed" : "in_progress"} value={succeeded ? "ready" : failed ? "failed" : "in_progress"} /></div>{succeeded ? <a className="mt-2 inline-flex min-h-10 items-center gap-2 text-primary underline underline-offset-4" href={`https://${hostname}`} rel="noreferrer" target="_blank"><Icon name="open_in_new" />https://{hostname}</a> : <p className="mt-2 text-on-surface-variant">{failed ? "The route was not applied. Review the rollout in Technical details before trying again." : "The Agent is applying and verifying the route."}</p>}</div>;
}

function routeFailure(cause: unknown) {
  const error = cause as LocalAPIError;
  return error.message || "The public route could not be created. Refresh and review the route state before retrying.";
}
