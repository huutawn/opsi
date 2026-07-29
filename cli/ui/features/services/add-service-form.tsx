import { Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function AddServiceForm({ console }: { console: ConsoleController }) {
  return (
    <Panel title="Add service">
      <form className="form" onSubmit={(event) => void console.actions.createService(event)}>
        <label>
          Type
          <select className="select" name="type">
            <option value="application">Application service</option>
            <option value="managed">Managed backing service</option>
            <option value="external">External dependency</option>
          </select>
        </label>
        <label>
          Name
          <input className="field" name="name" required />
        </label>
        <label>
          Container port
          <input className="field" min="1" name="container_port" type="number" />
        </label>
        <label>
          Health path
          <input className="field" name="health_path" placeholder="/health" />
        </label>
        <label>
          Replicas
          <input className="field" min="1" name="replicas" type="number" defaultValue={1} />
        </label>
        <p className="muted span2">This creates only service catalog identity. Immutable BuildRecords provide the image digest used for deployment; Git/source-build inputs do not belong here.</p>
        <button className="primary span2" disabled={console.state.busy === "service"}>
          {console.state.busy === "service" ? "Saving" : "Save draft"}
        </button>
      </form>
    </Panel>
  );
}
