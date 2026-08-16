"use client";

import { useEffect, useRef, useState } from "react";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { Resource, ResourceBinding, ServiceRecord } from "@/lib/contracts/registry";
import { resourceErrorExplanation } from "@/lib/presentation/resources/model";

export function ConnectApplicationDialog({
  environmentID,
  onBindingCreated,
  onClose,
  projectID,
  resource,
  services,
}: {
  environmentID: string;
  onBindingCreated: (binding: ResourceBinding) => Promise<void>;
  onClose: () => void;
  projectID: string;
  resource: Resource;
  services: ServiceRecord[];
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const defaultLogicalName = resource.type === "postgres" ? "DATABASE" : resource.type === "redis" ? "REDIS" : "NATS";
  const [selectedServiceID, setSelectedServiceID] = useState(services[0]?.id ?? "");
  const [logicalName, setLogicalName] = useState(defaultLogicalName);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (!selectedServiceID || !logicalName || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      const result = await client.createResourceBinding(
        projectID,
        {
          environment_id: environmentID,
          source: { kind: "application", id: selectedServiceID },
          target: { kind: "managed_service", id: resource.id },
          protocol: resource.type === "postgres" ? "postgres" : resource.type === "redis" ? "redis" : "nats",
          logical_name: logicalName.trim().toUpperCase(),
        },
        idempotencyKey,
      );

      await onBindingCreated(result.binding);
      onClose();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      const explanation = resourceErrorExplanation(apiErr.code, apiErr.message);
      setError({ ...explanation, code: apiErr.code });
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <dialog
      aria-describedby="connect-app-desc"
      aria-labelledby="connect-app-title"
      className="connectServerDialog placementDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">Resource Connection</p>
          <h2 id="connect-app-title">Connect Application</h2>
          <p id="connect-app-desc">
            Connect an application service to <strong>{resource.name}</strong>. Environment variables with connection
            details will be provisioned securely.
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      <form className="form" onSubmit={handleSubmit}>
        <label className="span2">
          Application Service
          <select
            className="select"
            disabled={services.length === 0}
            name="service"
            onChange={(e) => setSelectedServiceID(e.target.value)}
            required
            value={selectedServiceID}
          >
            {services.length === 0 ? (
              <option value="">No applications available in project</option>
            ) : (
              services.map((svc) => (
                <option key={svc.id} value={svc.id}>
                  {svc.name} ({svc.id})
                </option>
              ))
            )}
          </select>
        </label>

        <label className="span2">
          Logical Binding Name
          <input
            autoComplete="off"
            className="field"
            name="logicalName"
            onChange={(e) => setLogicalName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ""))}
            placeholder="DATABASE"
            required
            value={logicalName}
          />
          <small className="fieldHint">
            Prefix used for injected configuration keys (e.g. {logicalName || "DATABASE"}_HOST,{" "}
            {logicalName || "DATABASE"}_URL).
          </small>
        </label>

        <div className="infoBanner span2">
          <p>
            Target resource: <strong>{resource.name}</strong> ({resource.type.toUpperCase()})
          </p>
          <small>
            Credentials are generated and managed by Cloud authority. Passwords and tokens are stored in secure secrets
            and not displayed in plaintext.
          </small>
        </div>

        {error ? (
          <div className="truthCallout span2" role="alert">
            <b>{error.summary}</b>
            <p>{error.action}</p>
            {error.code ? <small className="errorCode">Error code: {error.code}</small> : null}
          </div>
        ) : null}

        <div className="dialogActions span2">
          <button disabled={submitting} onClick={onClose} type="button">
            Cancel
          </button>
          <button className="primary" disabled={submitting || !selectedServiceID || !logicalName} type="submit">
            {submitting ? "Connecting…" : "Connect Application"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
