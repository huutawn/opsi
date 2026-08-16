"use client";

import { useEffect, useRef, useState } from "react";
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
  const selectedItem = catalog.find((item) => item.type === selectedType);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  function handleSelectType(item: ResourceCatalogItem) {
    setSelectedType(item.type);
    setName(`${item.type}-1`);
    setCpuMillicores(item.defaultCPUMillicores);
    setMemoryMiB(Math.round(item.defaultMemoryBytes / (1024 * 1024)));
    if (item.defaultStorageBytes) {
      setStorageGiB(Math.round(item.defaultStorageBytes / (1024 * 1024 * 1024)));
    }
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (!selectedType || submitting) return;

    setSubmitting(true);
    setError(null);

    const client = new LocalClient();
    const idempotencyKey = crypto.randomUUID();

    try {
      let request;
      if (selectedType === "postgres") {
        request = buildPostgresCreateRequest({
          environmentID,
          name,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
          storageBytes: storageGiB * 1024 * 1024 * 1024,
        });
      } else if (selectedType === "redis") {
        request = buildValkeyCreateRequest({
          environmentID,
          name,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
        });
      } else {
        request = buildNATSCreateRequest({
          environmentID,
          name,
          cpuMillicores,
          memoryBytes: memoryMiB * 1024 * 1024,
        });
      }

      const result = await client.createResource(projectID, request, idempotencyKey);
      await onCreated(result.resource);
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
      className="connectServerDialog placementDialog resourceDialog"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      ref={dialog}
    >
      <div className="dialogHeading">
        <div>
          <p className="eyebrow">Infrastructure · Managed Resource</p>
          <h2 id="add-resource-title">Add Managed Resource</h2>
          <p id="add-resource-desc">
            {selectedType
              ? `Configure user-facing settings for ${selectedItem?.displayName}.`
              : "Select a supported managed capability from the infrastructure catalog."}
          </p>
        </div>
        <button aria-label="Close dialog" autoFocus className="iconButton" onClick={onClose} type="button">
          <svg aria-hidden="true" viewBox="0 0 20 20">
            <path d="m5 5 10 10M15 5 5 15" />
          </svg>
        </button>
      </div>

      {!selectedType ? (
        <div className="resourceCatalogGrid" role="list">
          {catalog.map((item) => (
            <button
              aria-label={`Select ${item.displayName}`}
              className="catalogCard"
              key={item.type}
              onClick={() => handleSelectType(item)}
              type="button"
            >
              <div className="catalogCardHeader">
                <span className="catalogBadge">{item.category}</span>
                {item.stateful ? <span className="storageBadge">Persistent storage</span> : <span className="ephemeralBadge">In-memory</span>}
              </div>
              <h3>{item.displayName}</h3>
              <p>{item.description}</p>
              <div className="catalogCardFooter">
                <span>Default port: {item.defaultPort}</span>
                <span className="catalogAction">Configure →</span>
              </div>
            </button>
          ))}
        </div>
      ) : (
        <form className="form resourceCreateForm" onSubmit={handleSubmit}>
          <div className="formSectionHeader">
            <h3>{selectedItem?.displayName} Configuration</h3>
            <button className="textButton" onClick={() => setSelectedType(null)} type="button">
              ← Change resource type
            </button>
          </div>

          <label className="span2">
            Resource Name
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
            <small className="fieldHint">Lowercase alphanumeric characters and hyphens only.</small>
          </label>

          <label>
            CPU Request (millicores)
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
            <small className="fieldHint">500m = 0.5 CPU core</small>
          </label>

          <label>
            Memory (MiB)
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
            <small className="fieldHint">RAM allocated to container</small>
          </label>

          {selectedType === "postgres" ? (
            <label className="span2">
              Persistent Storage Size (GiB)
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
              <small className="fieldHint">
                Persistent volume backed by standard-rwo storage. Retained upon resource deletion.
              </small>
            </label>
          ) : null}

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
            <button className="primary" disabled={submitting || !name} type="submit">
              {submitting ? "Provisioning…" : `Provision ${selectedItem?.name}`}
            </button>
          </div>
        </form>
      )}
    </dialog>
  );
}
