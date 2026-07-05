import { Metric, Panel, StatusBadge } from "@/components/ui/primitives";
import { DeploymentMini } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";

export function ServiceDetail({ console }: { console: ConsoleController }) {
  const service = console.state.serviceDetail;
  if (!service) return null;
  const jobs = console.state.deployments.filter((item) => item.service_id === service.id);
  const deps = console.state.services.filter((item) => item.type !== "application" && item.id !== service.id);
  const deployDisabled = !console.state.readiness?.can_deploy || service.source_type !== "git" || console.state.busy === `deploy-${service.id}`;
  return (
    <Panel title="Service detail">
      <div className="metrics">
        <Metric label="Desired" value={service.status} />
        <Metric label="Runtime" value={console.state.readiness?.can_deploy ? "ready" : "not ready"} />
        <Metric label="Image/version" value={service.image || "draft"} />
        <Metric label="Incidents" value={0} />
      </div>
      <h3>Deployments</h3>
      {jobs.length ? <DeploymentMini rows={jobs} /> : <p className="muted">None</p>}
      <h3>Deploy plan review</h3>
      <div className="specList">
        <div>
          <span>Service</span>
          <b>{service.name}</b>
        </div>
        <div>
          <span>Target</span>
          <b>{console.state.project?.name ?? "-"} / default / k3s</b>
        </div>
        <div>
          <span>Affected nodes</span>
          <b>{console.state.nodes.filter((node) => node.status === "healthy").length || 0}</b>
        </div>
        <div>
          <span>Revision</span>
          <b>{service.git_sha || service.image || "missing"}</b>
        </div>
        <div>
          <span>Runtime shape</span>
          <b>
            {service.replicas ?? 1}x / port {service.container_port || "-"} / {service.health_path || "/health"}
          </b>
        </div>
        <div>
          <span>Rollback policy</span>
          <b>previous successful revision required</b>
        </div>
      </div>
      <p className="muted">
        {service.source_type !== "git"
          ? "Image-source deploy is not supported by the current Agent runner."
          : console.state.readiness?.can_deploy
            ? "Review complete. Deployment will queue through the local API."
            : "Project is not ready. Add the first healthy server before deploying."}
      </p>
      <button
        className="primary"
        disabled={deployDisabled}
        onClick={() => void console.actions.deploy(service.id)}
        type="button"
      >
        {console.state.busy === `deploy-${service.id}` ? "Queueing" : "Deploy reviewed plan"}
      </button>
      <h3>Dependencies</h3>
      {deps.length ? deps.map((item) => <StatusBadge key={item.id} value={item.name} />) : <p className="muted">No bindings yet.</p>}
      <h3>Secrets</h3>
      <p className="muted">Bindings only. Values stay masked and require OTP reveal through Agent vault.</p>
    </Panel>
  );
}
