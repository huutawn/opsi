import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function SecretsView({ console }: { console: ConsoleController }) {
  const services = console.state.services.filter((item) => item.type === "application");
  const dependencies = console.state.services.filter((item) => item.type !== "application");
  const defaultService = services[0];

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
                      <StatusBadge value="local Agent" />
                    </td>
                    <td>
                      <StatusBadge value="local Agent" />
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
      {defaultService ? (
        <Panel title="Agent secret operations">
          <form className="form" onSubmit={(event) => void console.actions.secretCreate(event)}>
            <select className="select" defaultValue={defaultService.id} name="service_id">
              {services.map((service) => (
                <option key={service.id} value={service.id}>
                  {service.name}
                </option>
              ))}
            </select>
            <input className="field" name="name" placeholder="secret name" required />
            <input className="field" name="namespace" placeholder="namespace" />
            <button disabled={console.state.busy === "secret-create"} type="submit">
              Create
            </button>
          </form>
          <form className="form" onSubmit={(event) => void console.actions.secretRotate(event)}>
            <SecretMutationFields services={services} defaultServiceID={defaultService.id} />
            <button disabled={console.state.busy === "secret-rotate"} type="submit">
              Rotate
            </button>
          </form>
          <form className="form" onSubmit={(event) => void console.actions.secretReveal(event)}>
            <SecretMutationFields services={services} defaultServiceID={defaultService.id} />
            <button disabled={console.state.busy === "secret-reveal"} type="submit">
              Reveal
            </button>
          </form>
          {console.state.secretReveal ? (
            <textarea
              className="textarea"
              readOnly
              value={`username: ${console.state.secretReveal.username ?? ""}\npassword: ${console.state.secretReveal.password ?? ""}`}
            />
          ) : null}
        </Panel>
      ) : null}
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

function SecretMutationFields({ services, defaultServiceID }: { services: Array<{ id: string; name: string }>; defaultServiceID: string }) {
  return (
    <>
      <select className="select" defaultValue={defaultServiceID} name="service_id">
        {services.map((service) => (
          <option key={service.id} value={service.id}>
            {service.name}
          </option>
        ))}
      </select>
      <input className="field" name="name" placeholder="secret name" required />
      <input className="field" name="namespace" placeholder="namespace" />
      <input className="field" name="otp_request_id" placeholder="OTP request id" />
      <input autoComplete="one-time-code" className="field" name="otp_code" placeholder="OTP code" />
      <input autoComplete="one-time-code" className="field" name="totp_code" placeholder="TOTP code" />
    </>
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
