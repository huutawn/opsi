import { Panel } from "@/components/ui/primitives";
import { DeploymentsTable, Metrics, ReadinessPanel } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";

export function OverviewView({ console }: { console: ConsoleController }) {
  return (
    <section className="grid cols">
      <div className="grid">
        <ReadinessPanel console={console} />
        <Metrics console={console} />
      </div>
      <Panel title="Current work">
        <DeploymentsTable console={console} />
      </Panel>
    </section>
  );
}
