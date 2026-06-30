import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import { Metrics, ReadinessPanel } from "@/features/console/shared";
import type { ConsoleController } from "@/features/console/types";
import { formatTime } from "@/lib/formatting/time";

export function ProjectsView({ console }: { console: ConsoleController }) {
  return (
    <section className="grid cols">
      <Panel title="Projects">
        <form className="form" onSubmit={(event) => void console.actions.createProject(event)}>
          <label>
            Name
            <input className="field" name="name" required />
          </label>
          <label>
            Slug
            <input className="field" name="slug" />
          </label>
          <button className="primary span2" disabled={console.state.busy === "project"}>
            {console.state.busy === "project" ? "Creating" : "Create project"}
          </button>
        </form>
        {console.state.projects.length ? <ProjectTable console={console} /> : <Empty text="Create a project to start the production workflow." />}
      </Panel>
      <div className="grid">
        <ReadinessPanel console={console} />
        <Metrics console={console} />
      </div>
    </section>
  );
}

function ProjectTable({ console }: { console: ConsoleController }) {
  return (
    <div className="tableWrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Environment</th>
            <th>Readiness</th>
            <th>Nodes</th>
            <th>Services</th>
            <th>Last deploy</th>
            <th>Owner/team</th>
          </tr>
        </thead>
        <tbody>
          {console.state.projects.map((item) => (
            <tr key={item.id}>
              <td>
                <button className="linkButton" onClick={() => console.setProjectID(item.id)} type="button">
                  {item.name}
                </button>
              </td>
              <td>default</td>
              <td>
                <StatusBadge value={item.status} />
              </td>
              <td>{console.state.project?.id === item.id ? console.state.nodes.length : "-"}</td>
              <td>{console.state.project?.id === item.id ? console.state.services.length : "-"}</td>
              <td>{console.state.deployments[0] ? formatTime(console.state.deployments[0].created_at) : "-"}</td>
              <td>{item.created_by || item.org_id}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
