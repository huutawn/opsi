"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Empty, StatusBadge } from "@/components/ui/primitives";
import type { ConsoleController } from "@/features/console/types";
import type { AuditEvent } from "@/lib/contracts/registry";
import { formatTime } from "@/lib/formatting/time";

export function SecurityView({ console }: { console: ConsoleController }) {
  return console.route.tab === "audit" ? <AuditTab console={console} /> : <SecretsTab console={console} />;
}

function SecretsTab({ console }: { console: ConsoleController }) {
  const services = console.state.services.filter((item) => item.type === "application");
  const agentUnavailable = console.session?.agent_connected !== "ok";
  const [operation, setOperation] = useState<"create" | "rotate" | "reveal" | "totp">("create");
  const [serviceID, setServiceID] = useState("");

  function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!serviceID && operation !== "totp") return;
    if (operation === "create") void console.actions.secretCreate(event);
    else if (operation === "rotate") void console.actions.secretRotate(event);
    else if (operation === "reveal") void console.actions.secretReveal(event);
    else console.actions.setupTOTP();
  }

  return <div className="securityStack">
    <section className="securitySection" aria-labelledby="secrets-title">
      <div className="sectionHeading"><div><p className="eyebrow">Protected operations</p><h2 id="secrets-title">Secrets</h2><p>Metadata and inventory are not available from the Local API. Mutations remain explicit and Agent-backed.</p></div><StatusBadge value={agentUnavailable ? "unavailable" : "ready"} /></div>
      <form className="securityOperation" onSubmit={submit}>
        <label>Operation<select className="select" onChange={(event) => setOperation(event.target.value as typeof operation)} value={operation}><option value="create">Create</option><option value="rotate">Rotate</option><option value="reveal">Reveal</option><option value="totp">Set up TOTP</option></select></label>
        {operation === "totp" ? <p className="operationTarget"><b>Target</b><span>Project TOTP fallback for {console.state.project?.name || "the selected project"}</span></p> : <><label>Service<select className="select" name="service_id" onChange={(event) => setServiceID(event.target.value)} required value={serviceID}><option value="">Choose a service…</option>{services.map((service) => <option key={service.id} value={service.id}>{service.name}</option>)}</select></label><label>Secret name<input autoComplete="off" className="field" name="name" placeholder="Choose explicitly" required /></label><label>Namespace<input autoComplete="off" className="field" name="namespace" placeholder="Choose explicitly" required /></label></>}
        {operation === "rotate" || operation === "reveal" ? <SecondFactorFields /> : null}
        <div className="securityReview"><span>Target and second-factor values are reviewed before submission. Secret values never appear in the review.</span><button className="primary" disabled={agentUnavailable || operation !== "totp" && !services.length || console.state.busy.startsWith("secret-")} type="submit">Review {operation === "totp" ? "TOTP setup" : operation}</button></div>
      </form>
      {agentUnavailable ? <p className="truthCallout" role="status"><b>Agent unavailable.</b> Secret mutations are disabled; loaded audit history remains available.</p> : null}
      <p className="capabilityNote">Secret metadata/listing is a backend capability gap. No inventory is shown or implied.</p>
    </section>
    {console.state.secretReveal || console.state.totpSetup ? <ProtectedResult console={console} onClose={console.actions.hideSensitive} /> : null}
    <section className="securitySection" aria-labelledby="secret-audit-title"><div className="sectionHeading"><div><p className="eyebrow">Loaded history</p><h2 id="secret-audit-title">Secret audit</h2></div></div>{console.state.audit.some((item) => item.resource_type === "secret") ? <AuditList rows={console.state.audit.filter((item) => item.resource_type === "secret")} /> : <Empty text="No secret audit events were returned." />}</section>
  </div>;
}

function SecondFactorFields() {
  const [method, setMethod] = useState<"otp" | "totp">("otp");
  return <fieldset className="secondFactor"><legend>Second factor</legend><label>Method<select className="select" name="second_factor_method" onChange={(event) => setMethod(event.target.value as typeof method)} value={method}><option value="otp">One-time approval code</option><option value="totp">TOTP code</option></select></label>{method === "otp" ? <><label>OTP request ID<input autoComplete="off" className="field" name="otp_request_id" required /></label><label>OTP code<input autoComplete="one-time-code" className="field" name="otp_code" required /></label></> : <label>TOTP code<input autoComplete="one-time-code" className="field" name="totp_code" required /></label>}</fieldset>;
}

function ProtectedResult({ console, onClose }: { console: ConsoleController; onClose: () => void }) {
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
      const target = returnFocus.current?.isConnected ? returnFocus.current : document.querySelector<HTMLElement>(".securityReview button:not(:disabled)");
      target?.focus();
    };
  }, []);
  function hide() { dialog.current?.close(); onClose(); }
  useEffect(() => { if (remaining === 0) hide(); }, [remaining]); // eslint-disable-line react-hooks/exhaustive-deps
  return <dialog aria-labelledby="protected-result-title" aria-modal="true" className="protectedDialog" onCancel={(event) => { event.preventDefault(); hide(); }} ref={dialog}>
    <p className="eyebrow">Protected result</p><h2 id="protected-result-title">Sensitive content</h2><p className="warning">Do not copy or record this value. It is visible only while this protected surface is open.</p><p role="status">Automatically hides in {remaining}s.</p>
    {isOpen && result ? <div className="protectedValue"><p>Secret value</p><pre>{`username: ${result.username ?? "Not reported"}\npassword: ${result.password ?? "Not reported"}`}</pre></div> : null}
    {isOpen && totp ? <div className="protectedValue"><p>TOTP setup URI and secret</p><pre>{`${totp.uri}\nsecret: ${totp.secret}`}</pre></div> : null}
    <button onClick={hide} type="button">Hide now</button>
  </dialog>;
}

function AuditTab({ console }: { console: ConsoleController }) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return console.state.audit.filter((item) => {
      const haystack = [item.actor_user_id, item.actor_type, item.action, item.resource_type, item.resource_id].filter(Boolean).join(" ").toLowerCase();
      return (!needle || haystack.includes(needle)) && (!result || item.result === result);
    });
  }, [console.state.audit, query, result]);
  const selected = rows.find((item) => item.id === selectedID) ?? rows[0];
  return <div className="securityStack"><section className="securitySection" aria-labelledby="audit-title"><div className="sectionHeading"><div><p className="eyebrow">Loaded history</p><h2 id="audit-title">Audit</h2><p>Filters apply to loaded history; this API does not expose server pagination.</p></div><StatusBadge value={console.state.status === "ready" ? "ready" : "unavailable"} /></div><div className="filterBar"><label>Actor, action, or resource<input className="field" onChange={(event) => setQuery(event.target.value)} placeholder="Search loaded history" value={query} /></label><label>Result<select className="select" onChange={(event) => setResult(event.target.value)} value={result}><option value="">All results</option><option value="success">Success</option><option value="denied">Denied</option><option value="failed">Failed</option></select></label></div>{console.state.status !== "ready" && !console.state.audit.length ? <p className="truthCallout">Audit history unavailable; this is not an empty result.</p> : rows.length ? <div className="auditExplorer"><div className="auditList"><p className="muted">{rows.length} loaded event(s)</p>{rows.map((item) => <button aria-pressed={selected?.id === item.id} className={`auditRow audit-${item.result}`} key={item.id} onClick={() => setSelectedID(item.id)} type="button"><time>{formatTime(item.created_at)}</time><span>{actorLabel(item.actor_type)}{item.actor_user_id ? ` · ${item.actor_user_id}` : ""}</span><b>{item.action}</b><span>{item.resource_type}/{item.resource_id}</span><StatusBadge value={item.result} /></button>)}</div><AuditDetail item={selected} /></div> : <Empty text={console.state.audit.length ? "No loaded audit events match these filters." : "No audit events were returned."} />}</section></div>;
}

function AuditList({ rows }: { rows: AuditEvent[] }) { return <div className="auditList">{rows.map((item) => <div className="auditRow" key={item.id}><time>{formatTime(item.created_at)}</time><span>{actorLabel(item.actor_type)}</span><b>{item.action}</b><span>{item.resource_type}/{item.resource_id}</span><StatusBadge value={item.result} /></div>)}</div>; }

function AuditDetail({ item }: { item?: AuditEvent }) {
  if (!item) return <Empty title="Select an audit event" text="Choose a loaded event to inspect bounded evidence." />;
  const metadata = boundedMetadata(item.metadata_redacted);
  const requestID = item.metadata_redacted?.request_id ?? item.metadata_redacted?.correlation_id;
  return <aside className="auditDetail" aria-label="Audit event detail"><p className="eyebrow">Selected event</p><h3>{item.action}</h3><dl className="reviewFacts"><div><dt>Audit ID</dt><dd><code>{item.id}</code></dd></div><div><dt>Actor type</dt><dd>{item.actor_type}</dd></div><div><dt>Authenticated actor</dt><dd>{item.actor_user_id || "Not reported"}</dd></div><div><dt>Resource</dt><dd>{item.resource_type}/{item.resource_id}</dd></div><div><dt>Request/correlation ID</dt><dd>{typeof requestID === "string" ? requestID : "Not reported"}</dd></div><div><dt>Timestamp</dt><dd>{formatTime(item.created_at)}</dd></div><div><dt>Result</dt><dd><StatusBadge value={item.result} /></dd></div></dl><h4>Redacted metadata</h4>{metadata.length ? <dl className="metadataList">{metadata.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl> : <p className="muted">No redacted metadata reported.</p>}</aside>;
}

function boundedMetadata(value?: Record<string, unknown>) {
  return Object.entries(value ?? {}).slice(0, 12).map(([key, item]) => [key, formatValue(item)] as [string, string]);
}

function formatValue(value: unknown): string { if (value === null || value === undefined) return "Not reported"; if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value); if (Array.isArray(value)) return value.slice(0, 8).map(formatValue).join(", "); return "Structured value redacted"; }
function actorLabel(value: string) {
  const actor = value.toLowerCase();
  if (["human", "user"].includes(actor)) return "Human actor";
  if (["machine", "agent", "system", "automation"].includes(actor)) return "Machine actor";
  return "Unknown actor";
}
