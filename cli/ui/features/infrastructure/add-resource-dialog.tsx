"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Icon } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type { Resource } from "@/lib/contracts/registry";
import {
  buildNATSCreateRequest,
  buildPostgresCreateRequest,
  buildValkeyCreateRequest,
  resourceErrorExplanation,
  resourceTypeCatalog,
  type ResourceCatalogItem,
} from "@/lib/presentation/resources/model";

export function AddResourceDialog({
  environmentID,
  onClose,
  onCreated,
  projectID,
}: {
  environmentID: string;
  onClose: () => void;
  onCreated: (resource: Resource) => Promise<void>;
  projectID: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [selectedType, setSelectedType] = useState<ResourceCatalogItem["type"] | null>(null);
  const [name, setName] = useState("");
  const [cpuMillicores, setCpuMillicores] = useState<number>(500);
  const [memoryMiB, setMemoryMiB] = useState<number>(512);
  const [storageGiB, setStorageGiB] = useState<number>(10);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<{ summary: string; action: string; code?: string } | null>(null);

  const catalog = resourceTypeCatalog();
  const selectedItem = catalog.find((c) => c.type === selectedType);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  function handleSelectType(item: ResourceCatalogItem) {
    setSelectedType(item.type);
    setName(`${item.type}-primary`);
    setCpuMillicores(500);
    setMemoryMiB(item.type === "postgres" ? 512 : 256);
    setStorageGiB(item.stateful ? 10 : 0);
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedType) return;
    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    try {
      let created: Resource;
      if (selectedType === "postgres") {
        const req = buildPostgresCreateRequest({
          name,
          environmentID,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
          storageBytes: storageGiB * 1024 * 1024 * 1024,
        });
        const res = await client.createResource(projectID, req, crypto.randomUUID());
        created = res.resource;
      } else if (selectedType === "redis") {
        const req = buildValkeyCreateRequest({
          name,
          environmentID,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
        });
        const res = await client.createResource(projectID, req, crypto.randomUUID());
        created = res.resource;
      } else {
        const req = buildNATSCreateRequest({
          name,
          environmentID,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
        });
        const res = await client.createResource(projectID, req, crypto.randomUUID());
        created = res.resource;
      }

      await onCreated(created);
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
      aria-describedby="add-resource-desc"
      aria-labelledby="add-resource-title"
      className="fixed inset-0 m-auto bg-surface-container-low border border-outline-variant/30 rounded-2xl shadow-2xl p-6 max-w-2xl w-full backdrop:bg-background/80 backdrop:backdrop-blur-sm z-50 text-on-surface flex flex-col gap-5 max-h-[90vh] overflow-y-auto"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="flex items-start justify-between border-b border-outline-variant/20 pb-4">
        <div>
          <span className="font-label-sm text-xs text-primary font-bold uppercase tracking-wider block mb-1">
            Infrastructure · Managed Resource
          </span>
          <h2 id="add-resource-title" className="font-headline-md text-xl font-bold text-on-surface">
            Add Managed Resource
          </h2>
          <p id="add-resource-desc" className="font-body-md text-xs text-on-surface-variant mt-1">
            {selectedType
              ? `Configure user-facing settings for ${selectedItem?.displayName}.`
              : "Select a supported managed capability from the infrastructure catalog."}
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

      {!selectedType ? (
        <div className="grid grid-cols-2 gap-4" role="list">
          {catalog.map((item) => (
            <button
              aria-label={`Select ${item.displayName}`}
              className="bg-surface-container hover:bg-surface-container-high border border-outline-variant/20 hover:border-primary/50 rounded-2xl p-5 transition-all text-left flex flex-col justify-between gap-4 group cursor-pointer"
              key={item.type}
              onClick={() => handleSelectType(item)}
              type="button"
            >
              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <span className="text-[11px] font-label-sm uppercase font-bold text-primary px-2 py-0.5 bg-primary/10 rounded-md">
                    {item.category}
                  </span>
                  {item.stateful ? (
                    <span className="text-[10px] text-tertiary bg-tertiary/10 px-2 py-0.5 rounded-md font-mono">
                      Persistent
                    </span>
                  ) : (
                    <span className="text-[10px] text-secondary bg-secondary/10 px-2 py-0.5 rounded-md font-mono">
                      In-memory
                    </span>
                  )}
                </div>
                <h3 className="font-headline-md text-base font-bold text-on-surface group-hover:text-primary transition-colors">
                  {item.displayName}
                </h3>
                <p className="text-xs text-on-surface-variant line-clamp-2 leading-relaxed">
                  {item.description}
                </p>
              </div>
              <div className="flex items-center justify-between text-xs pt-3 border-t border-outline-variant/15 text-on-surface-variant">
                <span className="font-mono text-[11px]">Port: {item.defaultPort}</span>
                <span className="text-primary font-semibold group-hover:translate-x-1 transition-transform">
                  Configure →
                </span>
              </div>
            </button>
          ))}
        </div>
      ) : (
        <form className="space-y-4" onSubmit={handleSubmit}>
          <div className="flex items-center justify-between bg-surface-container/60 p-3 rounded-xl border border-outline-variant/15">
            <h3 className="font-headline-md text-sm font-bold text-on-surface">{selectedItem?.displayName} Configuration</h3>
            <button
              className="text-xs text-primary hover:underline cursor-pointer"
              onClick={() => setSelectedType(null)}
              type="button"
            >
              ← Change resource type
            </button>
          </div>

          <div className="space-y-1.5">
            <label className="text-xs font-label-sm text-on-surface-variant block">Resource Name</label>
            <input
              autoComplete="off"
              className="field"
              name="name"
              onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))}
              pattern="^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"
              placeholder={`${selectedType}-instance`}
              required
              spellCheck={false}
              value={name}
            />
            <small className="text-[11px] text-on-surface-variant block">Lowercase alphanumeric characters and hyphens only.</small>
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">CPU Request (millicores)</label>
              <input
                className="field"
                max="16000"
                min="100"
                name="cpu"
                onChange={(e) => setCpuMillicores(Number(e.target.value))}
                required
                step="50"
                type="number"
                value={cpuMillicores}
              />
              <small className="text-[11px] text-on-surface-variant block">500m = 0.5 CPU core</small>
            </div>

            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Memory (MiB)</label>
              <input
                className="field"
                max="65536"
                min="128"
                name="memory"
                onChange={(e) => setMemoryMiB(Number(e.target.value))}
                required
                step="64"
                type="number"
                value={memoryMiB}
              />
              <small className="text-[11px] text-on-surface-variant block">RAM allocated to container</small>
            </div>
          </div>

          {selectedType === "postgres" ? (
            <div className="space-y-1.5">
              <label className="text-xs font-label-sm text-on-surface-variant block">Persistent Storage Size (GiB)</label>
              <input
                className="field"
                max="1024"
                min="1"
                name="storage"
                onChange={(e) => setStorageGiB(Number(e.target.value))}
                required
                step="1"
                type="number"
                value={storageGiB}
              />
              <small className="text-[11px] text-on-surface-variant block">
                Persistent volume backed by standard-rwo storage.
              </small>
            </div>
          ) : null}

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
            <Button disabled={submitting || !name} size="sm" type="submit" variant="primary">
              {submitting ? "Provisioning…" : `Provision ${selectedItem?.displayName || "Resource"}`}
            </Button>
          </div>
        </form>
      )}
    </dialog>
  );
}
