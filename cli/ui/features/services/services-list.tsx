import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

const serviceGroups = ["application", "managed", "external"];

export function ServicesList({ console }: { console: ConsoleController }) {
  if (!console.state.services.length) return <Empty text="No services. Save a draft, then deploy when the project is ready." />;
  return (
    <div className="grid">
      {serviceGroups.map((group) => {
        const rows = console.state.services.filter((item) => item.type === group);
        return (
          <div key={group}>
            <h3>{group}</h3>
            {rows.length ? <ServiceGroup console={console} rows={rows} /> : <p className="muted">None</p>}
          </div>
        );
      })}
    </div>
  );
}

function ServiceGroup({ console, rows }: { console: ConsoleController; rows: typeof console.state.services }) {
  return (
    <div className="tableWrap">
      <table>
        <tbody>
          {rows.map((service) => (
            <tr key={service.id}>
              <td>
                <b>{service.name}</b>
                <br />
                <span className="muted">
                  {service.source_type} {service.repo_url || service.image || ""}
                </span>
              </td>
              <td>
                <StatusBadge value={service.status} />
              </td>
              <td className="actions">
                <button onClick={() => console.setServiceDetail(service)} type="button">
                  Review
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
