"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import { AuditList } from "@/features/security/audit-tab";

export function AccessTab({ console }: { console: ConsoleController }) {
  const services = console.state.services.filter((item) => item.type === "application");
  const agentUnavailable = console.session?.agent_connected !== "ok";
  const [operation, setOperation] = useState<"create" | "rotate" | "reveal" | "totp">("create");
  const [serviceID, setServiceID] = useState("");

  const session = console.session;
  const project = console.state.project;

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!serviceID && operation !== "totp") return;
    if (operation === "create") void console.actions.secretCreate(event);
    else if (operation === "rotate") void console.actions.secretRotate(event);
    else if (operation === "reveal") void console.actions.secretReveal(event);
    else console.actions.setupTOTP();
  }

  const secretAuditEvents = useMemo(() => {
    return console.state.audit.filter(
      (item) => item.resource_type === "secret" || item.action.startsWith("SECRET_") || item.action.startsWith("PAT_"),
    );
  }, [console.state.audit]);

  return (
    <div className="securityStack">
      <section className="securitySection" aria-labelledby="access-id-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Identity & Session</p>
            <h2 id="access-id-title">Access & Identities</h2>
            <p>Authenticated identity, role boundaries, and runtime execution authorities.</p>
          </div>
          <StatusBadge value={console.session?.authenticated ? "ready" : "unavailable"} />
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(300px, 1fr))", gap: 16, marginTop: 14 }}>
          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Authenticated Session</h3>
            <dl className="reviewFacts">
              <div>
                <dt>Actor</dt>
                <dd>{session?.user_id || "Human actor"}</dd>
              </div>
              <div>
                <dt>Role scope</dt>
                <dd><b>{session?.role ? session.role.toUpperCase() : "OPERATOR"}</b></dd>
              </div>
              <div>
                <dt>Organization</dt>
                <dd><code>{session?.org_id || "default"}</code></dd>
              </div>
              <div>
                <dt>Project</dt>
                <dd>{project?.name || "None"} (<code>{project?.id || "none"}</code>)</dd>
              </div>
            </dl>
          </div>

          <div style={{ border: "1px solid var(--line)", padding: 16, background: "var(--surface-muted)", borderRadius: 6 }}>
            <h3 style={{ margin: "0 0 10px", fontSize: 14 }}>Authority Connections</h3>
            <dl className="reviewFacts">
              <div>
                <dt>Cloud API</dt>
                <dd><StatusBadge value={session?.cloud_connected === "ok" ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>Node Agent</dt>
                <dd><StatusBadge value={session?.agent_connected === "ok" ? "ready" : "unavailable"} /></dd>
              </div>
              <div>
                <dt>Keyring PAT</dt>
                <dd>Stored securely in OS Keychain</dd>
              </div>
              <div>
                <dt>Secret Reveal Policy</dt>
                <dd>Explicit review with TTL auto-hide</dd>
              </div>
            </dl>
          </div>
        </div>
      </section>

      <section className="securitySection" aria-labelledby="secrets-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Protected operations</p>
            <h2 id="secrets-title">Secrets</h2>
            <p>Metadata and inventory are not available from the Local API. Mutations remain explicit and Agent-backed.</p>
          </div>
          <StatusBadge value={agentUnavailable ? "unavailable" : "ready"} />
        </div>

        <form className="securityOperation" onSubmit={submit}>
          <label>
            Operation
            <select
              className="select"
              onChange={(event) => setOperation(event.target.value as typeof operation)}
              value={operation}
            >
              <option value="create">Create</option>
              <option value="rotate">Rotate</option>
              <option value="reveal">Reveal</option>
              <option value="totp">Set up TOTP</option>
            </select>
          </label>

          {operation === "totp" ? (
            <p className="operationTarget">
              <b>Target</b>
              <span>Project TOTP fallback for {console.state.project?.name || "the selected project"}</span>
            </p>
          ) : (
            <>
              <label>
                Service
                <select
                  className="select"
                  name="service_id"
                  onChange={(event) => setServiceID(event.target.value)}
                  required
                  value={serviceID}
                >
                  <option value="">Choose a service…</option>
                  {services.map((service) => (
                    <option key={service.id} value={service.id}>
                      {service.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Secret name
                <input autoComplete="off" className="field" name="name" placeholder="Choose explicitly" required />
              </label>
              <label>
                Namespace
                <input autoComplete="off" className="field" name="namespace" placeholder="Choose explicitly" required />
              </label>
            </>
          )}

          {operation === "rotate" || operation === "reveal" ? <SecondFactorFields /> : null}

          <div className="securityReview">
            <span>Target and second-factor values are reviewed before submission. Secret values never appear in the review.</span>
            <button
              className="primary"
              disabled={agentUnavailable || (operation !== "totp" && !services.length) || console.state.busy.startsWith("secret-")}
              type="submit"
            >
              Review {operation === "totp" ? "TOTP setup" : operation}
            </button>
          </div>
        </form>

        {agentUnavailable ? (
          <p className="truthCallout" role="status">
            <b>Agent unavailable.</b> Secret mutations are disabled; loaded audit history remains available.
          </p>
        ) : null}
        <p className="capabilityNote">Secret metadata/listing is a backend capability gap. No inventory is shown or implied.</p>
      </section>

      {console.state.secretReveal || console.state.totpSetup ? (
        <ProtectedResult console={console} onClose={console.actions.hideSensitive} />
      ) : null}

      <section className="securitySection" aria-labelledby="secret-audit-title">
        <div className="sectionHeading">
          <div>
            <p className="eyebrow">Loaded history</p>
            <h2 id="secret-audit-title">Secret audit</h2>
          </div>
        </div>
        {secretAuditEvents.length ? (
          <AuditList rows={secretAuditEvents} />
        ) : (
          <Empty text="No secret audit events were returned." />
        )}
      </section>
    </div>
  );
}

export function SecondFactorFields() {
  const [method, setMethod] = useState<"otp" | "totp">("otp");
  return (
    <fieldset className="secondFactor">
      <legend>Second factor</legend>
      <label>
        Method
        <select
          className="select"
          name="second_factor_method"
          onChange={(event) => setMethod(event.target.value as typeof method)}
          value={method}
        >
          <option value="otp">One-time approval code</option>
          <option value="totp">TOTP code</option>
        </select>
      </label>
      {method === "otp" ? (
        <>
          <label>
            OTP request ID
            <input autoComplete="off" className="field" name="otp_request_id" required />
          </label>
          <label>
            OTP code
            <input autoComplete="one-time-code" className="field" name="otp_code" required />
          </label>
        </>
      ) : (
        <label>
          TOTP code
          <input autoComplete="one-time-code" className="field" name="totp_code" required />
        </label>
      )}
    </fieldset>
  );
}

export function ProtectedResult({ console, onClose }: { console: ConsoleController; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const result = console.state.secretReveal;
  const totp = console.state.totpSetup;
  const ttl = result?.ttl_seconds ?? totp?.ttl_seconds ?? 0;
  const [isOpen, setIsOpen] = useState(false);
  const [remaining, setRemaining] = useState(ttl);

  useEffect(() => {
    returnFocus.current = document.activeElement as HTMLElement | null;
    const element = dialog.current;
    element?.showModal();
    const frame = window.requestAnimationFrame(() => setIsOpen(true));
    const timer = window.setInterval(() => setRemaining((value) => Math.max(0, value - 1)), 1000);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearInterval(timer);
      if (element?.open) element.close();
      const target = returnFocus.current?.isConnected
        ? returnFocus.current
        : document.querySelector<HTMLElement>(".securityReview button:not(:disabled)");
      target?.focus();
    };
  }, []);

  function hide() {
    dialog.current?.close();
    onClose();
  }

  useEffect(() => {
    if (remaining === 0) hide();
  }, [remaining]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <dialog
      aria-labelledby="protected-result-title"
      aria-modal="true"
      className="protectedDialog"
      onCancel={(event) => {
        event.preventDefault();
        hide();
      }}
      ref={dialog}
    >
      <p className="eyebrow">Protected result</p>
      <h2 id="protected-result-title">Sensitive content</h2>
      <p className="warning">Do not copy or record this value. It is visible only while this protected surface is open.</p>
      <p role="status">Automatically hides in {remaining}s.</p>
      {isOpen && result ? (
        <div className="protectedValue">
          <p>Secret value</p>
          <pre>{`username: ${result.username ?? "Not reported"}\npassword: ${result.password ?? "Not reported"}`}</pre>
        </div>
      ) : null}
      {isOpen && totp ? (
        <div className="protectedValue">
          <p>TOTP setup URI and secret</p>
          <pre>{`${totp.uri}\nsecret: ${totp.secret}`}</pre>
        </div>
      ) : null}
      <button onClick={hide} type="button">
        Hide now
      </button>
    </dialog>
  );
}
