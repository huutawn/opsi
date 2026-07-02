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
        <label>
          Branch
          <input className="field" name="branch" placeholder="main" />
        </label>
        <label>
          Git SHA
          <input className="field" name="git_sha" required />
        </label>
        <label>
          Build method
          <select className="select" name="build_method">
            <option value="dockerfile">Dockerfile</option>
          </select>
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
        <p className="muted span2">Deploy requires readiness, a healthy deploy-capable Agent, and a concrete Git SHA.</p>
        <button className="primary span2" disabled={console.state.busy === "service"}>
          {console.state.busy === "service" ? "Saving" : "Save draft"}
        </button>
      </form>
    </Panel>
  );
}
