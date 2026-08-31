import { useEffect, useId, useRef } from "react";
import { Button, Input } from "@/components/ui/primitives";
import {
  connectionCatalog,
  connectionProtocols,
  connectionTemplateError,
  mappingPreview,
  mappingSensitivity,
  sourceOptions,
  type MappingValue,
  type SourceDescriptor,
} from "@/features/deploy/connection-descriptors";
import type { DetectedDependency } from "@/lib/contracts/registry";

type Mapping = NonNullable<DetectedDependency["injections"]>[number];
type RowErrors = { environment?: string; source?: string; template?: string };

export { connectionProtocols, connectionTemplateError };

export function DependencyMappings({ dependencies, dependency, disabled, onChange }: { dependencies: DetectedDependency[]; dependency: DetectedDependency; disabled: boolean; onChange: (mappings: Mapping[]) => void }) {
  const mappings = dependency.injections || [];
  const options = sourceOptions(dependency.protocol, dependency.strategy);
  return <fieldset className="space-y-3 lg:col-span-4" disabled={disabled}>
    <legend className="text-sm font-medium">Injection mappings</legend>
    {mappings.length === 0 && <p className="text-sm text-on-surface-variant">No environment mapping has been selected.</p>}
    <div className="space-y-2">{mappings.map((mapping, index) => <MappingRow dependency={dependency} errors={mappingErrors(dependencies, dependency, mapping)} key={`${mapping.environment_name}-${index}`} mapping={mapping} onMapping={(next) => onChange(mappings.map((item, itemIndex) => itemIndex === index ? next : item))} onRemove={() => onChange(mappings.filter((_, itemIndex) => itemIndex !== index))} options={options} />)}</div>
    <Button onClick={() => onChange([...mappings, { environment_name: "", symbolic_source: options[0]?.source || "" }])} type="button" variant="secondary">Add mapping</Button>
  </fieldset>;
}

function MappingRow({ dependency, errors, mapping, onMapping, onRemove, options }: { dependency: DetectedDependency; errors: RowErrors; mapping: Mapping; onMapping: (mapping: Mapping) => void; onRemove: () => void; options: SourceDescriptor[] }) {
  const id = useId();
  const sourceRef = useRef<HTMLSelectElement>(null);
  const templateRef = useRef<HTMLTextAreaElement>(null);
  const templateSelected = mapping.symbolic_source === "connection.template";
  useEffect(() => {
    if (!mapping.environment_name) document.getElementById(`${id}-environment`)?.focus();
    else if (!mapping.symbolic_source) sourceRef.current?.focus();
    else if (templateSelected) templateRef.current?.focus();
  }, [id, mapping.environment_name, mapping.symbolic_source, templateSelected]);
  return <article className="border border-outline-variant/30 bg-surface-container-lowest p-3">
    <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end">
      <label className="grid gap-1 text-sm" htmlFor={`${id}-environment`}><span className="font-medium">Environment name</span><Input aria-describedby={errors.environment ? `${id}-environment-error` : undefined} aria-invalid={Boolean(errors.environment)} id={`${id}-environment`} value={mapping.environment_name} onChange={(event) => onMapping({ ...mapping, environment_name: event.target.value })} />{errors.environment && <span className="text-xs text-error" id={`${id}-environment-error`} role="alert">{errors.environment}</span>}</label>
      <label className="grid gap-1 text-sm" htmlFor={`${id}-source`}><span className="font-medium">Dialect / value</span><select aria-describedby={errors.source ? `${id}-source-error` : undefined} aria-invalid={Boolean(errors.source)} className={selectClass} id={`${id}-source`} ref={sourceRef} value={mapping.symbolic_source} onChange={(event) => onMapping({ environment_name: mapping.environment_name, symbolic_source: event.target.value, ...(event.target.value === "connection.template" ? { template: "" } : {}) })}><option value="">Select a dialect</option>{options.map(({ source, label }) => <option key={source} value={source}>{label}</option>)}</select>{errors.source && <span className="text-xs text-error" id={`${id}-source-error`} role="alert">{errors.source}</span>}</label>
      <Button aria-label={`Remove mapping ${mapping.environment_name || mapping.symbolic_source || "row"}`} onClick={onRemove} type="button" variant="ghost">Remove</Button>
    </div>
    {templateSelected && <label className="mt-3 grid gap-1 text-sm" htmlFor={`${id}-template`}><span className="font-medium">Safe connection template</span><textarea aria-describedby={`${id}-template-help${errors.template ? ` ${id}-template-error` : ""}`} aria-invalid={Boolean(errors.template)} className={`${selectClass} min-h-24 py-2 font-mono`} id={`${id}-template`} maxLength={1024} onChange={(event) => onMapping({ ...mapping, template: event.target.value })} ref={templateRef} value={mapping.template || ""} /><span className="text-xs text-on-surface-variant" id={`${id}-template-help`}>{dependency.protocol === "nats" ? "Use only {{host}}, {{port}} and {{database}}; managed NATS has no credentials." : "Use {{host}}, {{port}}, {{database}} and encoded credentials such as {{password|kv_quote}}."}</span>{errors.template && <span className="text-xs text-error" id={`${id}-template-error`} role="alert">{errors.template}</span>}</label>}
    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-on-surface-variant"><span>Sensitivity: <strong className="text-on-surface">{mappingSensitivity(dependency.protocol, mapping)}</strong></span><span>Example: <code>{mappingPreview(dependency.protocol, mapping)}</code></span></div>
  </article>;
}

export function deploymentMappingError(dependencies: DetectedDependency[]): boolean {
  if (dependencies.some((dependency) => !connectionProtocols.includes(dependency.protocol as (typeof connectionProtocols)[number]))) return true;
  return dependencies.some((dependency) => (dependency.injections || []).some((mapping) => Object.keys(mappingErrors(dependencies, dependency, mapping)).length > 0));
}

function mappingErrors(dependencies: DetectedDependency[], dependency: DetectedDependency, mapping: MappingValue): RowErrors {
  const result: RowErrors = {};
  const environment = mapping.environment_name.trim();
  if (!environment) result.environment = "Environment name is required.";
  else if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(environment)) result.environment = "Use a letter or underscore first, followed by at most 127 letters, digits, or underscores.";
  else if (reservedEnvironment(environment)) result.environment = "This environment name is reserved by the platform.";
  else if (dependencies.flatMap((item) => item.injections || []).filter((item) => item.environment_name.trim() === environment).length > 1) result.environment = "Environment names must be unique across dependency mappings.";
  const options = sourceOptions(dependency.protocol, dependency.strategy);
  if (!mapping.symbolic_source) result.source = "Select a dialect or value source.";
  else if (!options.some((item) => item.source === mapping.symbolic_source)) result.source = "This source is not valid for the selected protocol and strategy. Select another source.";
  if (mapping.symbolic_source === "connection.template") {
    const descriptor = connectionCatalog[dependency.protocol as keyof typeof connectionCatalog];
    const templateError = connectionTemplateError(mapping.template || "", descriptor?.credentialTemplates ?? false);
    if (templateError) result.template = templateError;
  }
  return result;
}

function reservedEnvironment(name: string): boolean {
  const upper = name.toUpperCase();
  return ["PORT", "HOSTNAME", "HOST_IP", "POD_NAME", "POD_NAMESPACE", "POD_IP"].includes(upper) || upper.startsWith("OPSI_") || upper.startsWith("KUBERNETES_");
}

const selectClass = "min-h-10 w-full border border-outline-variant/40 bg-surface-container-lowest px-3 text-sm text-on-surface focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary";
