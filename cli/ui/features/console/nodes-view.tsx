import { Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { AddServerForm } from "@/features/nodes/add-server-form";
import { BootstrapTimeline } from "@/features/nodes/bootstrap-timeline";
import { NodeDetail } from "@/features/nodes/node-detail";
import { NodesTable } from "@/features/nodes/nodes-table";

export function NodesView({ console }: { console: ConsoleController }) {
  return (
    <section className="grid">
      <AddServerForm console={console} />
      <Panel title="Node list">
        <NodesTable console={console} />
      </Panel>
      <NodeDetail console={console} />
      <BootstrapTimeline console={console} />
    </section>
  );
}
