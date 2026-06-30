import { Panel } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function AddServerForm({ console }: { console: ConsoleController }) {
  const canWorker = Boolean(console.state.readiness?.can_deploy);
  return (
    <Panel title="Add server">
      <div className="steps" aria-label="Add server steps">
        {["Server access", "Preflight", "Install K3s + Agent", "Verify connection", "Complete"].map((step, index) => (
          <span className={index === 0 ? "active" : ""} key={step}>
            {index + 1}. {step}
          </span>
        ))}
      </div>
      <form className="form" onSubmit={(event) => void console.actions.addServer(event)}>
        <label>
          Server name
          <input className="field" name="name" />
        </label>
        <label>
          Role
          <select className="select" name="role">
            <option value="first_server">First server</option>
            <option disabled={!canWorker} value="worker">
              Worker
            </option>
          </select>
        </label>
        <label>
          Public host
          <input className="field" name="public_host" required />
        </label>
        <label>
          SSH port
          <input className="field" defaultValue="22" name="ssh_port" type="number" />
        </label>
        <label>
          SSH username
          <input className="field" defaultValue="root" name="ssh_username" />
        </label>
        <label>
          Auth method
          <select className="select" name="auth_method">
            <option value="password">Password</option>
            <option value="private_key">Private key</option>
          </select>
        </label>
        <label className="span2">
          Private key/password
          <textarea autoComplete="off" className="textarea" name="secret" />
        </label>
        <p className="muted span2">Credential is submitted once, never saved in browser, then discarded after worker handoff.</p>
        <button className="primary span2" disabled={console.state.busy === "server"}>
          {console.state.busy === "server" ? "Starting" : "Start preflight and install"}
        </button>
      </form>
    </Panel>
  );
}
