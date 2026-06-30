import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function SecretsView({ console }: { console: ConsoleController }) {
  const services = console.state.services.filter((item) => item.type === "application");
  const dependencies = console.state.services.filter((item) => item.type !== "application");

  return (
    <section className="grid">
      <Panel title="Secret bindings">
        {services.length ? (
          <div className="tableWrap">
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Namespace</th>
                  <th>Bindings</th>
                  <th>Reveal</th>
                  <th>Rotate</th>
                </tr>
              </thead>
              <tbody>
                {services.map((service) => (
                  <tr key={service.id}>
                    <td>{service.name}</td>
                    <td>{service.namespace || "default"}</td>
                    <td>
                      {dependencies.length ? dependencies.map((item) => <StatusBadge key={item.id} value={`${item.name}: masked`} />) : "none"}
                    </td>
                    <td>
                      <button disabled type="button">
                        Requires Owner + OTP
                      </button>
                    </td>
                    <td>
                      <button disabled type="button">
                        Agent vault endpoint required
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty text="No application services yet. Secret values never appear here; only masked bindings are shown." />
        )}
      </Panel>
      <Panel title="Access audit">
        {console.state.audit.some((item) => item.resource_type === "secret") ? (
          <SecretAudit console={console} />
        ) : (
          <Empty text="No secret reveal or rotation audit events for this project." />
        )}
      </Panel>
    </section>
  );
}

function SecretAudit({ console }: { console: ConsoleController }) {
  return (
    <div className="tableWrap">
      <table>
        <tbody>
          {console.state.audit
            .filter((item) => item.resource_type === "secret")
            .map((item) => (
              <tr key={item.id}>
                <td>{item.action}</td>
                <td>{item.resource_id}</td>
                <td>
                  <StatusBadge value={item.result} />
                </td>
              </tr>
            ))}
        </tbody>
      </table>
    </div>
  );
}
