import { Empty, Panel } from "@/components/ui/primitives";
import { DeploymentsTable, EventsList } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";

export function DeploymentsView({ console }: { console: ConsoleController }) {
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
                <span>Logs</span>
                <b>redacted event stream only</b>
              </div>
              <div>
                <span>Rollback</span>
                <b>disabled until Cloud exposes rollback API</b>
              </div>
            </div>
            <EventsList events={console.state.deploymentEvents} />
          </>
        ) : (
          <Empty text="Select a deployment to reconnect to its redacted event stream." />
        )}
      </Panel>
    </section>
  );
}
