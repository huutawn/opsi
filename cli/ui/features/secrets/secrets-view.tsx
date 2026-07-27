import { Empty, Panel, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";

export function SecretsView({ console }: { console: ConsoleController }) {
  const services = console.state.services.filter((item) => item.type === "application");
  const defaultService = services[0];
  const agentUnavailable = console.session?.agent_connected !== "ok";

  return (
    <section className="grid">
      <Panel title="Secret bindings">
        <div className="unavailable">
          <div><b>Secret metadata/listing</b><p>No factual backend API exists. Create, rotate, reveal, and TOTP setup remain explicit Agent operations.</p></div>
          <StatusBadge value="BACKEND_GAP" />
        </div>
      </Panel>
      {defaultService ? (
        <Panel title="Agent secret operations">
          {agentUnavailable ? <p className="muted" role="status">AGENT_UNAVAILABLE: reconnect the configured Agent before submitting secret operations.</p> : null}
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
            <button disabled={agentUnavailable || console.state.busy === "secret-create"} type="submit">
              Create
            </button>
          </form>
          <form className="form" onSubmit={(event) => void console.actions.secretRotate(event)}>
            <SecretMutationFields services={services} defaultServiceID={defaultService.id} />
            <button disabled={agentUnavailable || console.state.busy === "secret-rotate"} type="submit">
              Rotate
            </button>
          </form>
          <form className="form" onSubmit={(event) => void console.actions.secretReveal(event)}>
            <SecretMutationFields services={services} defaultServiceID={defaultService.id} />
            <button disabled={agentUnavailable || console.state.busy === "secret-reveal"} type="submit">
              Reveal
            </button>
          </form>
          <button disabled={agentUnavailable} onClick={console.actions.setupTOTP} type="button">Review TOTP setup</button>
          {console.state.totpSetup ? (
            <div className="secretReveal" role="status">
              <p><b>TOTP setup URI</b> (clears automatically after {console.state.totpSetup.ttl_seconds}s)</p>
              <textarea className="textarea" readOnly value={console.state.totpSetup.uri + "\nsecret: " + console.state.totpSetup.secret} />
            </div>
          ) : null}
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
