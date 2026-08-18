"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Icon } from "@/components/ui/primitives";
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

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedServiceID || !logicalName || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
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
        crypto.randomUUID()
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
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
            Resource Connection
          </span>
          <h2 id="connect-app-title" className="font-headline-md text-xl font-bold text-on-surface">
            Connect Application
          </h2>
          <p id="connect-app-desc" className="font-body-md text-xs text-on-surface-variant mt-1">
            Connect an application to <strong className="text-on-surface">{resource.name}</strong>. Environment variables will be injected securely.
          </p>
        </div>
        <button
          aria-label="Close dialog"
          autoFocus
          className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      <form className="space-y-4" onSubmit={handleSubmit}>
        <div className="space-y-1.5">
          <label className="text-xs font-label-sm text-on-surface-variant block">Application Service</label>
          <select
            className="field"
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
        </div>

        <div className="space-y-1.5">
          <label className="text-xs font-label-sm text-on-surface-variant block">Logical Binding Name</label>
          <input
            autoComplete="off"
            className="field"
            name="logicalName"
            onChange={(e) => setLogicalName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ""))}
            placeholder="DATABASE"
            required
            value={logicalName}
          />
          <small className="text-[11px] text-on-surface-variant block">
            Prefix used for configuration keys (e.g. {logicalName || "DATABASE"}_HOST, {logicalName || "DATABASE"}_URL).
          </small>
        </div>

        <div className="bg-surface-container/60 p-3 rounded-xl border border-outline-variant/15 text-xs text-on-surface-variant space-y-1">
          <p>
            Target resource: <strong className="text-on-surface">{resource.name}</strong> ({resource.type.toUpperCase()})
          </p>
          <p className="text-[11px]">
            Credentials and tokens are stored in secure secrets and not displayed in plaintext.
          </p>
        </div>

        {error ? (
          <div className="p-3 bg-error-container/20 text-error border border-error/30 rounded-xl text-xs space-y-1" role="alert">
            <strong className="block font-semibold">{error.summary}</strong>
            <p>{error.action}</p>
            {error.code ? <small className="font-mono text-[10px]">Error code: {error.code}</small> : null}
          </div>
        ) : null}

        <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
          <Button disabled={submitting} onClick={onClose} size="sm" type="button" variant="outline">
            Cancel
          </Button>
          <Button disabled={submitting || !selectedServiceID || !logicalName} size="sm" type="submit" variant="primary">
            {submitting ? "Connecting…" : "Connect Application"}
          </Button>
        </div>
      </form>
    </dialog>
  );
}
