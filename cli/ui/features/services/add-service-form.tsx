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
          Source
          <select className="select" name="source_type">
            <option value="git">Git</option>
            <option value="image">Image</option>
            <option value="external">External</option>
          </select>
        </label>
        <label>
          Image
          <input className="field" name="image" />
        </label>
        <label className="span2">
          Repo URL
          <input className="field" name="repo_url" />
        </label>
        <p className="muted span2">Deploy Now remains disabled until project readiness is ready.</p>
        <button className="primary span2" disabled={console.state.busy === "service"}>
          {console.state.busy === "service" ? "Saving" : "Save draft"}
        </button>
      </form>
    </Panel>
  );
}
