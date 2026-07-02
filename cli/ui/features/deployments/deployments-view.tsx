import { Empty, Panel } from "@/components/ui/primitives";
import { DeploymentsTable, EventsList } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";

export function DeploymentsView({ console }: { console: ConsoleController }) {
  const activeDeploymentID = console.state.deploymentEvents[0]?.deployment_id;
  const activeDeployment = console.state.deployments.find((item) => item.id === activeDeploymentID);

  return (
    <section className="grid">
      <Panel title="Deployments">
        <DeploymentsTable console={console} />
      </Panel>
      <Panel title="Deployment progress">
        {console.state.deploymentEvents.length ? (
          <>
            <div className="specList compact">
              <div>
                <span>Request ID</span>
                <b>{console.state.deploymentEvents.find((item) => item.request_id)?.request_id ?? "-"}</b>
              </div>
              <div>
                <span>Plan hash</span>
                <b>{shortHash(activeDeployment?.deployment_plan_hash)}</b>
              </div>
              <div>
                <span>Manifest hash</span>
                <b>{shortHash(activeDeployment?.manifest_hash)}</b>
              </div>
              <div>
                <span>Target</span>
                <b>{activeDeployment?.node_id ? `${activeDeployment.node_id} / ${activeDeployment.agent_id}` : "-"}</b>
              </div>
              <div>
                <span>Rollback</span>
                <b>{activeDeployment?.rollback_eligible ? "available" : (activeDeployment?.rollback_blocked_reason ?? "not available")}</b>
              </div>
              <div>
                <span>Result</span>
                <b>{activeDeployment?.failure_code || activeDeployment?.status || "-"}</b>
              </div>
            </div>
            <button
              disabled={!activeDeployment?.rollback_eligible || console.state.busy === `rollback-${activeDeployment.id}`}
              onClick={() => activeDeployment && void console.actions.rollback(activeDeployment.id)}
              type="button"
            >
              Confirm rollback
            </button>
            <EventsList events={console.state.deploymentEvents} />
          </>
        ) : (
          <Empty text="Select a deployment to reconnect to its redacted event stream." />
        )}
      </Panel>
    </section>
  );
}

function shortHash(value?: string) {
  return value ? value.slice(0, 12) : "-";
}
