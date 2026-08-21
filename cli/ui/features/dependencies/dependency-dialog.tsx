"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Icon, StatusBadge } from "@/components/ui/primitives";
import { LocalAPIError, LocalClient } from "@/lib/api/local-client";
import type {
  ApplicationDependency,
  DependencyInjectionMapping,
  PlacementFacts,
  ServiceConfigurationDiff,
  ServiceConfigurationDraft,
  ServiceConfigurationPreview,
  ServiceConfigurationValidation,
  ServiceRecord,
} from "@/lib/contracts/registry";
import {
  POSTGRES_CONVENTIONAL_MAPPINGS,
  POSTGRES_DATABASE_URL_MAPPING,
  VALKEY_REDIS_URL_MAPPING,
  detectPostgresPreset,
  detectValkeyPreset,
  type PostgresPreset,
  type ValkeyPreset,
} from "./types";

type Props = {
  consumer: ServiceRecord;
  existingDependency?: ApplicationDependency;
  facts: PlacementFacts;
  onApply: (updatedConsumer: ServiceRecord) => Promise<void>;
  onClose: () => void;
  projectID: string;
  targetIdentityHint?: string;
  targetKindHint?: "managed_service" | "application";
};

type DependencyKind = "postgres" | "valkey" | "app_http";

export function DependencyDialog({
  consumer,
  existingDependency,
  facts,
  onApply,
  onClose,
  projectID,
  targetIdentityHint,
  targetKindHint,
}: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const client = useMemo(() => new LocalClient(), []);

  const managedResources = useMemo(
    () => (facts.resources ?? []).filter((r) => r.kind === "managed_service"),
    [facts.resources]
  );
  const postgresResources = useMemo(
    () => managedResources.filter((r) => r.type === "postgres" || r.type === "postgresql"),
    [managedResources]
  );
  const valkeyResources = useMemo(
    () => managedResources.filter((r) => r.type === "redis" || r.type === "valkey"),
    [managedResources]
  );
  const targetApplications = useMemo(
    () => facts.services.filter((s) => s.id !== consumer.id),
    [facts.services, consumer.id]
  );

  const isEditing = Boolean(existingDependency?.logical_name);

  // Determine initial kind
  const initialKind: DependencyKind = useMemo(() => {
    if (isEditing && existingDependency) {
      if (existingDependency.target_kind === "application") return "app_http";
      if (existingDependency.protocol === "postgres") return "postgres";
      if (existingDependency.protocol === "redis") return "valkey";
    }
    if (targetKindHint === "application") return "app_http";
    if (targetIdentityHint) {
      const match = managedResources.find((r) => r.id === targetIdentityHint);
      if (match?.type === "postgres" || match?.type === "postgresql") return "postgres";
      if (match?.type === "redis" || match?.type === "valkey") return "valkey";
    }
    if (postgresResources.length) return "postgres";
    if (valkeyResources.length) return "valkey";
    return "app_http";
  }, [isEditing, existingDependency, targetKindHint, targetIdentityHint, managedResources, postgresResources, valkeyResources]);

  const [depKind, setDepKind] = useState<DependencyKind>(initialKind);
  const [logicalName, setLogicalName] = useState(
    isEditing ? existingDependency!.logical_name : initialKind === "postgres" ? "database" : initialKind === "valkey" ? "cache" : "backend"
  );
  const [targetIdentity, setTargetIdentity] = useState(
    (isEditing ? existingDependency?.target_identity : undefined) ||
      targetIdentityHint ||
      (initialKind === "postgres"
        ? postgresResources[0]?.id || ""
        : initialKind === "valkey"
        ? valkeyResources[0]?.id || ""
        : targetApplications[0]?.id || "")
  );
  const [required, setRequired] = useState(isEditing ? existingDependency!.required : true);
  const [injectionPhase, setInjectionPhase] = useState<"runtime" | "build">(
    isEditing && existingDependency?.injection_phase === "build" ? "build" : "runtime"
  );

  // App-to-App fields
  const [accessContext, setAccessContext] = useState<"browser" | "server">(
    isEditing && existingDependency?.access_context === "browser" ? "browser" : "server"
  );
  const [strategy, setStrategy] = useState<"same_origin" | "internal_http" | "public_http">(
    (isEditing && (existingDependency?.strategy as "same_origin" | "internal_http" | "public_http")) || "internal_http"
  );
  const [httpPath, setHttpPath] = useState(isEditing ? existingDependency?.path || "/api" : "/api");
  const [httpEnvName, setHttpEnvName] = useState(
    isEditing && existingDependency?.injection_mappings?.[0]?.env_name ? existingDependency.injection_mappings[0].env_name : "API_URL"
  );

  // Presets
  const [pgPreset, setPgPreset] = useState<PostgresPreset>(
    isEditing && existingDependency?.injection_mappings ? detectPostgresPreset(existingDependency.injection_mappings) : "DATABASE_URL"
  );
  const [pgDbUrlEnv, setPgDbUrlEnv] = useState(
    isEditing && existingDependency?.injection_mappings?.[0]?.env_name ? existingDependency.injection_mappings[0].env_name : "DATABASE_URL"
  );
  const [valkeyPreset, setValkeyPreset] = useState<ValkeyPreset>(
    isEditing && existingDependency?.injection_mappings ? detectValkeyPreset(existingDependency.injection_mappings) : "REDIS_URL"
  );
  const [valkeyUrlEnv, setValkeyUrlEnv] = useState(
    isEditing && existingDependency?.injection_mappings?.[0]?.env_name ? existingDependency.injection_mappings[0].env_name : "REDIS_URL"
  );
  const [customMappings, setCustomMappings] = useState<DependencyInjectionMapping[]>(
    isEditing && existingDependency?.injection_mappings && existingDependency.injection_mappings.length > 0
      ? existingDependency.injection_mappings
      : [{ env_name: "DATABASE_URL", symbolic_source: "connection.url" }]
  );

  // Optional verification assertion contract
  const [enableVerification, setEnableVerification] = useState(Boolean(isEditing && existingDependency?.verification_contract));
  const [verifyPath, setVerifyPath] = useState(existingDependency?.verification_contract?.path || "/health/dependencies/database");
  const [verifyStatus, setVerifyStatus] = useState(existingDependency?.verification_contract?.expected_status || 200);

  // Review state
  const [step, setStep] = useState<"form" | "review">("form");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ code?: string; message: string; nextAction?: string } | null>(null);
  const [reviewResult, setReviewResult] = useState<{
    preview: ServiceConfigurationPreview;
    validation: ServiceConfigurationValidation;
    diff: ServiceConfigurationDiff;
    draft: ServiceConfigurationDraft;
  } | null>(null);

  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  // Update target when kind changes
  function handleKindChange(nextKind: DependencyKind) {
    setDepKind(nextKind);
    setError(null);
    if (nextKind === "postgres") {
      setLogicalName("database");
      setTargetIdentity(postgresResources[0]?.id || "");
      setInjectionPhase("runtime");
    } else if (nextKind === "valkey") {
      setLogicalName("cache");
      setTargetIdentity(valkeyResources[0]?.id || "");
      setInjectionPhase("runtime");
    } else {
      setLogicalName("backend");
      setTargetIdentity(targetApplications[0]?.id || "");
      setStrategy("internal_http");
      setAccessContext("server");
    }
  }

  // Build the ApplicationDependency object
  function buildDependency(): ApplicationDependency {
    let mappings: DependencyInjectionMapping[] = [];

    if (depKind === "postgres") {
      if (pgPreset === "DATABASE_URL") {
        mappings = POSTGRES_DATABASE_URL_MAPPING(pgDbUrlEnv.trim() || "APP_DATABASE_URL");
      } else if (pgPreset === "PG_CONVENTIONAL") {
        mappings = POSTGRES_CONVENTIONAL_MAPPINGS;
      } else {
        mappings = customMappings.filter((m) => m.env_name.trim());
      }
      return {
        logical_name: logicalName.trim(),
        target_kind: "managed_service",
        target_identity: targetIdentity,
        protocol: "postgres",
        required,
        injection_phase: injectionPhase,
        injection_mappings: mappings,
        verification_contract: enableVerification
          ? {
              type: "consumer_http",
              path: verifyPath.trim() || "/health/dependencies/database",
              expected_status: Number(verifyStatus) || 200,
            }
          : undefined,
      };
    }

    if (depKind === "valkey") {
      if (valkeyPreset === "REDIS_URL") {
        mappings = VALKEY_REDIS_URL_MAPPING(valkeyUrlEnv.trim() || "APP_REDIS_URL");
      } else {
        mappings = customMappings.filter((m) => m.env_name.trim());
      }
      return {
        logical_name: logicalName.trim(),
        target_kind: "managed_service",
        target_identity: targetIdentity,
        protocol: "redis",
        required,
        injection_phase: injectionPhase,
        injection_mappings: mappings,
        verification_contract: enableVerification
          ? {
              type: "consumer_http",
              path: verifyPath.trim() || "/health/dependencies/cache",
              expected_status: Number(verifyStatus) || 200,
            }
          : undefined,
      };
    }

    // App-to-App HTTP
    if (strategy === "same_origin") {
      return {
        logical_name: logicalName.trim(),
        target_kind: "application",
        target_identity: targetIdentity,
        protocol: "http",
        strategy: "same_origin",
        access_context: "browser",
        path: httpPath.trim() || "/api",
        required,
        injection_phase: injectionPhase,
        verification_contract: enableVerification
          ? {
              type: "consumer_http",
              path: verifyPath.trim() || "/health/dependencies/api",
              expected_status: Number(verifyStatus) || 200,
            }
          : undefined,
      };
    }

    if (strategy === "internal_http") {
      mappings = [{ env_name: httpEnvName.trim() || "API_URL", symbolic_source: "internal.url" }];
      return {
        logical_name: logicalName.trim(),
        target_kind: "application",
        target_identity: targetIdentity,
        protocol: "http",
        strategy: "internal_http",
        access_context: "server",
        required,
        injection_phase: injectionPhase,
        injection_mappings: mappings,
        verification_contract: enableVerification
          ? {
              type: "consumer_http",
              path: verifyPath.trim() || "/health/dependencies/api",
              expected_status: Number(verifyStatus) || 200,
            }
          : undefined,
      };
    }

    // public_http
    mappings = [{ env_name: httpEnvName.trim() || "PUBLIC_API_URL", symbolic_source: "public.url" }];
    return {
      logical_name: logicalName.trim(),
      target_kind: "application",
      target_identity: targetIdentity,
      protocol: "http",
      strategy: "public_http",
      access_context: accessContext,
      required,
      injection_phase: injectionPhase,
      injection_mappings: mappings,
      verification_contract: enableVerification
        ? {
            type: "consumer_http",
            path: verifyPath.trim() || "/health/dependencies/api",
            expected_status: Number(verifyStatus) || 200,
          }
        : undefined,
    };
  }

  function compileDraft(dep: ApplicationDependency): ServiceConfigurationDraft {
    const current = consumer.configuration;
    const oldDeps = current?.dependencies ?? [];
    const otherDeps = oldDeps.filter(
      (d) => d.logical_name !== (existingDependency?.logical_name || dep.logical_name)
    );
    return {
      schema_version: "opsi.service_configuration/v1",
      environment: current?.environment ?? [],
      public_route: current?.public_route,
      bindings: current?.bindings ?? [],
      resource_bindings: current?.resource_bindings ?? [],
      dependencies: [...otherDeps, dep],
    };
  }

  async function handleReview() {
    if (!logicalName.trim() || !targetIdentity) return;
    setError(null);
    setBusy(true);

    const dep = buildDependency();
    const draft = compileDraft(dep);

    try {
      const [preview, validation, diff] = await Promise.all([
        client.serviceConfigurationPreview(projectID, consumer.id, draft),
        client.serviceConfigurationValidate(projectID, consumer.id, draft),
        client.serviceConfigurationDiff(projectID, consumer.id, draft),
      ]);
      setReviewResult({ preview, validation, diff, draft });
      setStep("review");
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      setError({
        code: apiErr.code || "REVIEW_FAILED",
        message: apiErr.message || "Failed to preview service configuration.",
        nextAction: apiErr.nextAction,
      });
    } finally {
      setBusy(false);
    }
  }

  async function handleApply() {
    if (!reviewResult || !reviewResult.validation.valid) return;
    setBusy(true);
    setError(null);

    const idempotencyKey = crypto.randomUUID();
    try {
      const res = await client.serviceConfigurationApply(
        projectID,
        consumer.id,
        {
          draft: reviewResult.preview.configuration,
          expected_revision: reviewResult.preview.current_revision,
          expected_state_hash: reviewResult.preview.current_state_hash,
        },
        idempotencyKey
      );
      const updated: ServiceRecord = {
        ...consumer,
        configuration: res.configuration,
      };
      await onApply(updated);
      onClose();
    } catch (cause) {
      const apiErr = cause as LocalAPIError;
      setError({
        code: apiErr.code || "APPLY_FAILED",
        message: apiErr.message || "Failed to apply service configuration.",
        nextAction: apiErr.nextAction,
      });
    } finally {
      setBusy(false);
    }
  }

  // Custom mapping helpers
  function addCustomMapping() {
    setCustomMappings((current) => [...current, { env_name: "", symbolic_source: "connection.url" }]);
  }
  function updateCustomMapping(index: number, field: "env_name" | "symbolic_source", val: string) {
    setCustomMappings((current) =>
      current.map((item, i) => (i === index ? { ...item, [field]: val } : item))
    );
  }
  function removeCustomMapping(index: number) {
    setCustomMappings((current) => current.filter((_, i) => i !== index));
  }

  return (
    <dialog
      aria-labelledby="dep-dialog-title"
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
            Application Dependency Contract
          </span>
          <h2 id="dep-dialog-title" className="font-headline-md text-xl font-bold text-on-surface">
            {existingDependency?.logical_name ? "Edit Dependency Contract" : "Add Dependency Contract"}
          </h2>
          <p className="font-body-md text-xs text-on-surface-variant mt-0.5">
            Consumer: <strong className="text-on-surface">{consumer.name}</strong> ({consumer.id})
          </p>
        </div>
        <button
          aria-label="Close dialog"
          className="p-1.5 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest rounded-lg transition-colors cursor-pointer"
          onClick={onClose}
          type="button"
        >
          <Icon name="close" className="text-[20px]" />
        </button>
      </div>

      {error ? (
        <div className="p-4 bg-error-container/20 border border-status-failed/30 rounded-xl text-xs space-y-1 text-on-surface" role="alert">
          <div className="flex items-center gap-2 text-status-failed font-bold">
            <Icon name="error" className="text-[16px]" />
            <span>{error.code || "Error"}</span>
          </div>
          <p>{error.message}</p>
          {error.nextAction ? <p className="text-on-surface-variant text-[11px] mt-1">{error.nextAction}</p> : null}
        </div>
      ) : null}

      {step === "form" ? (
        <form
          className="space-y-5"
          onSubmit={(e) => {
            e.preventDefault();
            void handleReview();
          }}
        >
          {/* Kind Selector */}
          <div>
            <label className="font-label-sm text-xs text-on-surface-variant block mb-2 font-semibold">
              Dependency Type
            </label>
            <div className="grid grid-cols-3 gap-2">
              <button
                type="button"
                onClick={() => handleKindChange("postgres")}
                className={`p-3 rounded-xl border flex flex-col items-center gap-1.5 transition-all text-xs cursor-pointer ${
                  depKind === "postgres"
                    ? "bg-primary-container border-primary text-primary font-bold shadow-sm"
                    : "bg-surface-container border-outline-variant/20 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-high"
                }`}
              >
                <Icon name="database" className="text-[22px]" />
                <span>PostgreSQL</span>
              </button>
              <button
                type="button"
                onClick={() => handleKindChange("valkey")}
                className={`p-3 rounded-xl border flex flex-col items-center gap-1.5 transition-all text-xs cursor-pointer ${
                  depKind === "valkey"
                    ? "bg-primary-container border-primary text-primary font-bold shadow-sm"
                    : "bg-surface-container border-outline-variant/20 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-high"
                }`}
              >
                <Icon name="memory" className="text-[22px]" />
                <span>Valkey / Redis</span>
              </button>
              <button
                type="button"
                onClick={() => handleKindChange("app_http")}
                className={`p-3 rounded-xl border flex flex-col items-center gap-1.5 transition-all text-xs cursor-pointer ${
                  depKind === "app_http"
                    ? "bg-primary-container border-primary text-primary font-bold shadow-sm"
                    : "bg-surface-container border-outline-variant/20 text-on-surface-variant hover:text-on-surface hover:bg-surface-container-high"
                }`}
              >
                <Icon name="api" className="text-[22px]" />
                <span>App HTTP</span>
              </button>
            </div>
          </div>

          {/* Core Fields */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div>
              <label className="font-label-sm text-xs text-on-surface-variant block mb-1 font-semibold" htmlFor="dep-logical-name">
                Logical Dependency Name <span className="text-status-failed">*</span>
              </label>
              <input
                id="dep-logical-name"
                className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 text-xs text-on-surface font-code-md focus:outline-none focus:border-primary/50"
                placeholder="e.g. database, cache, backend"
                required
                value={logicalName}
                onChange={(e) => setLogicalName(e.target.value)}
              />
              <span className="text-[11px] text-on-surface-variant block mt-0.5">
                Symbolic name referenced by application
              </span>
            </div>

            <div>
              <label className="font-label-sm text-xs text-on-surface-variant block mb-1 font-semibold" htmlFor="dep-target">
                Target Resource / Application <span className="text-status-failed">*</span>
              </label>
              <select
                id="dep-target"
                className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2.5 text-xs text-on-surface font-code-md focus:outline-none focus:border-primary/50 cursor-pointer"
                required
                value={targetIdentity}
                onChange={(e) => setTargetIdentity(e.target.value)}
              >
                {depKind === "postgres" ? (
                  postgresResources.length ? (
                    postgresResources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name} ({r.id}) — PostgreSQL
                      </option>
                    ))
                  ) : (
                    <option value="">No PostgreSQL resource available</option>
                  )
                ) : depKind === "valkey" ? (
                  valkeyResources.length ? (
                    valkeyResources.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name} ({r.id}) — Valkey
                      </option>
                    ))
                  ) : (
                    <option value="">No Valkey resource available</option>
                  )
                ) : targetApplications.length ? (
                  targetApplications.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.key || s.id} — Application
                    </option>
                  ))
                ) : (
                  <option value="">No other application available</option>
                )}
              </select>
            </div>
          </div>

          {/* Phase & Requirement */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 bg-surface-container p-3 rounded-xl border border-outline-variant/20 text-xs">
            <div>
              <label className="font-label-sm text-xs text-on-surface-variant block mb-1 font-semibold">
                Injection Phase
              </label>
              <div className="flex items-center gap-4">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    name="injection_phase"
                    value="runtime"
                    checked={injectionPhase === "runtime"}
                    onChange={() => setInjectionPhase("runtime")}
                  />
                  <span>Runtime (injected at deploy)</span>
                </label>
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="radio"
                    name="injection_phase"
                    value="build"
                    checked={injectionPhase === "build"}
                    onChange={() => setInjectionPhase("build")}
                  />
                  <span>Build-time</span>
                </label>
              </div>
              <p className="text-[11px] text-on-surface-variant mt-1">
                {injectionPhase === "build"
                  ? "Build-time value becomes a build input. Changing it requires a new BuildRecord."
                  : "Runtime value is injected during deployment. Changing does not require rebuild."}
              </p>
            </div>

            <div className="flex flex-col justify-center">
              <label className="flex items-center gap-2 cursor-pointer">
                <input
                  type="checkbox"
                  checked={required}
                  onChange={(e) => setRequired(e.target.checked)}
                />
                <span className="font-semibold">Required dependency</span>
              </label>
              <p className="text-[11px] text-on-surface-variant mt-1">
                Required dependencies block deployment preflight if the target or connection is not ready.
              </p>
            </div>
          </div>

          {/* Managed PostgreSQL Settings */}
          {depKind === "postgres" ? (
            <div className="space-y-3 bg-surface-container p-4 rounded-xl border border-outline-variant/20">
              <label className="font-label-sm text-xs text-on-surface-variant block font-semibold">
                Connection Variables Mapping
              </label>
              <div className="flex items-center gap-3 text-xs">
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="pg_preset"
                    value="DATABASE_URL"
                    checked={pgPreset === "DATABASE_URL"}
                    onChange={() => setPgPreset("DATABASE_URL")}
                  />
                  <span>DATABASE_URL</span>
                </label>
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="pg_preset"
                    value="PG_CONVENTIONAL"
                    checked={pgPreset === "PG_CONVENTIONAL"}
                    onChange={() => setPgPreset("PG_CONVENTIONAL")}
                  />
                  <span>PostgreSQL variables (PGHOST, etc.)</span>
                </label>
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="pg_preset"
                    value="CUSTOM"
                    checked={pgPreset === "CUSTOM"}
                    onChange={() => setPgPreset("CUSTOM")}
                  />
                  <span>Custom mapping</span>
                </label>
              </div>

              {pgPreset === "DATABASE_URL" ? (
                <div className="pt-2">
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Environment Variable Name
                  </label>
                  <input
                    className="w-full sm:w-72 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={pgDbUrlEnv}
                    onChange={(e) => setPgDbUrlEnv(e.target.value)}
                    placeholder="APP_DATABASE_URL"
                  />
                  <p className="text-[11px] text-on-surface-variant mt-1.5 font-code-md">
                    <strong className="text-primary">{pgDbUrlEnv || "APP_DATABASE_URL"}</strong> ← PostgreSQL connection URL
                  </p>
                </div>
              ) : pgPreset === "PG_CONVENTIONAL" ? (
                <div className="pt-2 space-y-1 text-xs font-code-md text-on-surface-variant bg-surface-container-highest p-3 rounded-lg border border-outline-variant/20">
                  <p><strong className="text-primary">PGHOST</strong> ← PostgreSQL host endpoint</p>
                  <p><strong className="text-primary">PGPORT</strong> ← PostgreSQL port number</p>
                  <p><strong className="text-primary">PGDATABASE</strong> ← PostgreSQL database name</p>
                  <p><strong className="text-primary">PGUSER</strong> ← PostgreSQL credential username</p>
                  <p><strong className="text-primary">PGPASSWORD</strong> ← PostgreSQL credential password</p>
                </div>
              ) : (
                <div className="pt-2 space-y-2">
                  {customMappings.map((m, idx) => (
                    <div key={idx} className="flex items-center gap-2">
                      <input
                        className="flex-1 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface"
                        placeholder="ENV_NAME"
                        value={m.env_name}
                        onChange={(e) => updateCustomMapping(idx, "env_name", e.target.value)}
                      />
                      <span className="text-xs text-on-surface-variant">←</span>
                      <select
                        className="flex-1 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs text-on-surface font-code-md"
                        value={m.symbolic_source}
                        onChange={(e) => updateCustomMapping(idx, "symbolic_source", e.target.value)}
                      >
                        <option value="connection.url">PostgreSQL connection URL</option>
                        <option value="credential.password">PostgreSQL credential password</option>
                        <option value="credential.username">PostgreSQL credential username</option>
                        <option value="database.name">PostgreSQL database name</option>
                        <option value="endpoint.host">PostgreSQL host endpoint</option>
                        <option value="endpoint.port">PostgreSQL port number</option>
                      </select>
                      <button
                        type="button"
                        onClick={() => removeCustomMapping(idx)}
                        className="p-1.5 text-on-surface-variant hover:text-status-failed"
                      >
                        <Icon name="delete" className="text-[16px]" />
                      </button>
                    </div>
                  ))}
                  <Button size="sm" variant="secondary" onClick={addCustomMapping} type="button">
                    <Icon name="add" className="text-[14px]" /> Add mapping
                  </Button>
                </div>
              )}
            </div>
          ) : null}

          {/* Managed Valkey Settings */}
          {depKind === "valkey" ? (
            <div className="space-y-3 bg-surface-container p-4 rounded-xl border border-outline-variant/20">
              <label className="font-label-sm text-xs text-on-surface-variant block font-semibold">
                Connection Variables Mapping
              </label>
              <div className="flex items-center gap-3 text-xs">
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="valkey_preset"
                    value="REDIS_URL"
                    checked={valkeyPreset === "REDIS_URL"}
                    onChange={() => setValkeyPreset("REDIS_URL")}
                  />
                  <span>REDIS_URL</span>
                </label>
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="valkey_preset"
                    value="CUSTOM"
                    checked={valkeyPreset === "CUSTOM"}
                    onChange={() => setValkeyPreset("CUSTOM")}
                  />
                  <span>Custom mapping</span>
                </label>
              </div>

              {valkeyPreset === "REDIS_URL" ? (
                <div className="pt-2">
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Environment Variable Name
                  </label>
                  <input
                    className="w-full sm:w-72 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={valkeyUrlEnv}
                    onChange={(e) => setValkeyUrlEnv(e.target.value)}
                    placeholder="APP_REDIS_URL"
                  />
                  <p className="text-[11px] text-on-surface-variant mt-1.5 font-code-md">
                    <strong className="text-primary">{valkeyUrlEnv || "APP_REDIS_URL"}</strong> ← Valkey connection URL
                  </p>
                </div>
              ) : (
                <div className="pt-2 space-y-2">
                  {customMappings.map((m, idx) => (
                    <div key={idx} className="flex items-center gap-2">
                      <input
                        className="flex-1 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface"
                        placeholder="ENV_NAME"
                        value={m.env_name}
                        onChange={(e) => updateCustomMapping(idx, "env_name", e.target.value)}
                      />
                      <span className="text-xs text-on-surface-variant">←</span>
                      <select
                        className="flex-1 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs text-on-surface font-code-md"
                        value={m.symbolic_source}
                        onChange={(e) => updateCustomMapping(idx, "symbolic_source", e.target.value)}
                      >
                        <option value="connection.url">Valkey connection URL</option>
                        <option value="credential.password">Valkey credential password</option>
                        <option value="endpoint.host">Valkey host endpoint</option>
                        <option value="endpoint.port">Valkey port number</option>
                      </select>
                      <button
                        type="button"
                        onClick={() => removeCustomMapping(idx)}
                        className="p-1.5 text-on-surface-variant hover:text-status-failed"
                      >
                        <Icon name="delete" className="text-[16px]" />
                      </button>
                    </div>
                  ))}
                  <Button size="sm" variant="secondary" onClick={addCustomMapping} type="button">
                    <Icon name="add" className="text-[14px]" /> Add mapping
                  </Button>
                </div>
              )}
            </div>
          ) : null}

          {/* App-to-App HTTP Settings */}
          {depKind === "app_http" ? (
            <div className="space-y-4 bg-surface-container p-4 rounded-xl border border-outline-variant/20 text-xs">
              <div>
                <label className="font-label-sm text-xs text-on-surface-variant block mb-1 font-semibold">
                  Caller Access Context
                </label>
                <div className="grid grid-cols-2 gap-3">
                  <button
                    type="button"
                    onClick={() => {
                      setAccessContext("browser");
                      setStrategy("same_origin");
                    }}
                    className={`p-3 rounded-lg border text-left transition-all cursor-pointer ${
                      accessContext === "browser"
                        ? "bg-primary-container border-primary text-on-surface"
                        : "bg-surface-container-high border-outline-variant/20 text-on-surface-variant hover:text-on-surface"
                    }`}
                  >
                    <strong className="block text-primary">Browser</strong>
                    <span className="text-[11px] text-on-surface-variant block mt-0.5">
                      Request originates in end-user browser
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setAccessContext("server");
                      setStrategy("internal_http");
                    }}
                    className={`p-3 rounded-lg border text-left transition-all cursor-pointer ${
                      accessContext === "server"
                        ? "bg-primary-container border-primary text-on-surface"
                        : "bg-surface-container-high border-outline-variant/20 text-on-surface-variant hover:text-on-surface"
                    }`}
                  >
                    <strong className="block text-primary">Server</strong>
                    <span className="text-[11px] text-on-surface-variant block mt-0.5">
                      Request originates from deployed workload
                    </span>
                  </button>
                </div>
              </div>

              <div>
                <label className="font-label-sm text-xs text-on-surface-variant block mb-1 font-semibold">
                  Connection Strategy
                </label>
                <div className="grid grid-cols-3 gap-2">
                  <button
                    type="button"
                    disabled={accessContext === "server"}
                    onClick={() => setStrategy("same_origin")}
                    className={`p-2.5 rounded-lg border text-left transition-all ${
                      accessContext === "server"
                        ? "opacity-40 cursor-not-allowed border-outline-variant/10 bg-surface-container-highest"
                        : strategy === "same_origin"
                        ? "bg-primary-container border-primary text-on-surface cursor-pointer"
                        : "bg-surface-container-high border-outline-variant/20 text-on-surface-variant hover:text-on-surface cursor-pointer"
                    }`}
                  >
                    <strong className="block text-xs">Same origin</strong>
                    <span className="text-[10px] text-on-surface-variant block mt-0.5">
                      Relative route (e.g. /api)
                    </span>
                  </button>

                  <button
                    type="button"
                    disabled={accessContext === "browser"}
                    onClick={() => setStrategy("internal_http")}
                    className={`p-2.5 rounded-lg border text-left transition-all ${
                      accessContext === "browser"
                        ? "opacity-40 cursor-not-allowed border-outline-variant/10 bg-surface-container-highest"
                        : strategy === "internal_http"
                        ? "bg-primary-container border-primary text-on-surface cursor-pointer"
                        : "bg-surface-container-high border-outline-variant/20 text-on-surface-variant hover:text-on-surface cursor-pointer"
                    }`}
                  >
                    <strong className="block text-xs">Internal HTTP</strong>
                    <span className="text-[10px] text-on-surface-variant block mt-0.5">
                      Private cluster networking
                    </span>
                  </button>

                  <button
                    type="button"
                    onClick={() => setStrategy("public_http")}
                    className={`p-2.5 rounded-lg border text-left transition-all cursor-pointer ${
                      strategy === "public_http"
                        ? "bg-primary-container border-primary text-on-surface"
                        : "bg-surface-container-high border-outline-variant/20 text-on-surface-variant hover:text-on-surface"
                    }`}
                  >
                    <strong className="block text-xs">Public HTTP</strong>
                    <span className="text-[10px] text-on-surface-variant block mt-0.5">
                      Target public endpoint
                    </span>
                  </button>
                </div>
              </div>

              {strategy === "same_origin" ? (
                <div>
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Same-origin Relative Path
                  </label>
                  <input
                    className="w-full sm:w-72 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={httpPath}
                    onChange={(e) => setHttpPath(e.target.value)}
                    placeholder="/api"
                  />
                  <p className="text-[11px] text-on-surface-variant mt-1.5 font-code-md">
                    Browser requests: <strong className="text-primary">current-origin + {httpPath || "/api"}</strong> (No internal DNS exposed)
                  </p>
                </div>
              ) : (
                <div>
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Environment Variable Name
                  </label>
                  <input
                    className="w-full sm:w-72 bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={httpEnvName}
                    onChange={(e) => setHttpEnvName(e.target.value)}
                    placeholder={strategy === "internal_http" ? "API_URL" : "PUBLIC_API_URL"}
                  />
                  <p className="text-[11px] text-on-surface-variant mt-1.5 font-code-md">
                    <strong className="text-primary">{httpEnvName || "API_URL"}</strong> ← {strategy === "internal_http" ? "Internal service URL" : "Public service URL"}
                  </p>
                </div>
              )}
            </div>
          ) : null}

          {/* Optional Verification Contract */}
          <div className="space-y-3 bg-surface-container p-4 rounded-xl border border-outline-variant/20 text-xs">
            <div className="flex items-center justify-between">
              <label className="flex items-center gap-2 cursor-pointer font-semibold">
                <input
                  type="checkbox"
                  checked={enableVerification}
                  onChange={(e) => setEnableVerification(e.target.checked)}
                />
                <span>Configure Post-Deploy Assertion Contract</span>
              </label>
              <span className="text-[11px] text-on-surface-variant">Optional Consumer Probe</span>
            </div>

            {enableVerification ? (
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 pt-2">
                <div className="sm:col-span-2">
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Relative HTTP Path on Consumer
                  </label>
                  <input
                    className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={verifyPath}
                    onChange={(e) => setVerifyPath(e.target.value)}
                    placeholder="/health/dependencies/database"
                  />
                </div>
                <div>
                  <label className="font-label-sm text-xs text-on-surface-variant block mb-1">
                    Expected Status Code
                  </label>
                  <input
                    type="number"
                    className="w-full bg-surface-container-highest border border-outline-variant/30 rounded-lg p-2 text-xs font-code-md text-on-surface focus:outline-none focus:border-primary/50"
                    value={verifyStatus}
                    onChange={(e) => setVerifyStatus(Number(e.target.value))}
                    placeholder="200"
                  />
                </div>
              </div>
            ) : (
              <p className="text-[11px] text-on-surface-variant">
                Without an assertion contract, post-deploy verification will result in <strong className="text-status-warning">Partially verified</strong> upon passing connectivity probes.
              </p>
            )}
          </div>

          <div className="flex items-center justify-end gap-3 pt-3 border-t border-outline-variant/20">
            <Button onClick={onClose} variant="secondary" type="button">
              Cancel
            </Button>
            <Button
              disabled={busy || !logicalName.trim() || !targetIdentity}
              type="submit"
              variant="primary"
            >
              {busy ? "Previewing…" : "Review Dependency Contract"}
            </Button>
          </div>
        </form>
      ) : reviewResult ? (
        /* Review Screen */
        <div className="space-y-5">
          <div className="bg-surface-container p-4 rounded-xl border border-outline-variant/20 space-y-3 text-xs">
            <div className="flex items-center justify-between">
              <span className="font-semibold text-on-surface">Cloud Configuration Validation</span>
              <StatusBadge
                label={reviewResult.validation.valid ? "Validation Passed" : "Validation Failed"}
                value={reviewResult.validation.valid ? "healthy" : "failed"}
              />
            </div>

            <div className="grid grid-cols-2 gap-3 font-code-md bg-surface-container-highest p-3 rounded-lg">
              <div>
                <span className="text-[10px] text-on-surface-variant block uppercase">Current Revision</span>
                <strong>rev {reviewResult.preview.current_revision}</strong>
                <span className="block text-[10px] text-on-surface-variant truncate">
                  {reviewResult.preview.current_state_hash || "Initial"}
                </span>
              </div>
              <div>
                <span className="text-[10px] text-on-surface-variant block uppercase">Proposed Draft Hash</span>
                <span className="block text-[10px] text-primary truncate">
                  {reviewResult.preview.draft_state_hash}
                </span>
              </div>
            </div>

            {reviewResult.validation.issues?.length ? (
              <div className="space-y-1.5 pt-2">
                <span className="font-semibold text-status-failed block">Validation Issues:</span>
                <ul className="space-y-1">
                  {reviewResult.validation.issues.map((issue, idx) => (
                    <li key={idx} className="p-2.5 bg-error-container/20 rounded-lg border border-error/30 text-error flex items-start gap-2">
                      <Icon name="warning" className="text-[16px] shrink-0 mt-0.5" />
                      <div>
                        <strong>{issue.code}</strong> {issue.field ? `(${issue.field})` : ""}: {issue.message}
                      </div>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            {/* Semantic Diff */}
            <div className="pt-2">
              <span className="font-semibold text-on-surface block mb-1">Semantic Changes</span>
              {reviewResult.diff.changes.length ? (
                <ul className="space-y-1 bg-surface-container-highest p-3 rounded-lg font-code-md text-[11px]">
                  {reviewResult.diff.changes.map((c, idx) => (
                    <li key={idx} className="flex items-center gap-2">
                      <span className="text-primary font-bold uppercase">{c.action}</span>
                      <span className="text-on-surface">{c.kind}</span>
                      {c.name ? <span className="text-on-surface-variant font-semibold">({c.name})</span> : null}
                      {c.before || c.after ? (
                        <span className="text-on-surface-variant truncate">
                          {c.before || "none"} → {c.after || "none"}
                        </span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-on-surface-variant">No semantic changes detected.</p>
              )}
            </div>
          </div>

          <div className="flex items-center justify-between pt-3 border-t border-outline-variant/20">
            <Button onClick={() => setStep("form")} variant="secondary" type="button">
              Back to Edit
            </Button>
            <Button
              disabled={busy || !reviewResult.validation.valid}
              onClick={handleApply}
              variant="primary"
              type="button"
            >
              {busy ? "Applying…" : "Apply Dependency Contract"}
            </Button>
          </div>
        </div>
      ) : null}
    </dialog>
  );
}
