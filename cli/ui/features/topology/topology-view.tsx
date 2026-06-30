import { Empty, Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function TopologyView({ console }: { console: ConsoleController }) {
  if (!console.state.nodes.length || !console.state.services.length) {
    return (
      <Panel title="Topology">
        <Empty text="Topology will appear after at least one healthy server and one deployed service." />
      </Panel>
    );
  }
  return (
    <Panel title="Topology">
      <div className="topology">
        <div className="topoRow">
          <TopoCol title="Project" items={[console.state.project?.name ?? "Project", "default"]} />
          <div className="edge">to</div>
          <TopoCol title="Nodes" items={console.state.nodes.map((item) => `${item.name} ${item.status}`)} />
          <div className="edge">to</div>
          <TopoCol title="Services" items={console.state.services.map((item) => `${item.name} ${item.type}`)} />
          <div className="edge">to</div>
          <TopoCol title="Deployments" items={console.state.deployments.map((item) => `${item.id} ${item.status}`)} />
        </div>
      </div>
    </Panel>
  );
}

function TopoCol({ title, items }: { title: string; items: string[] }) {
  return (
    <div className="topoCol">
      <h3>{title}</h3>
      {items.map((item) => (
        <div className="topoNode" key={item}>
          {item}
        </div>
      ))}
    </div>
  );
}
