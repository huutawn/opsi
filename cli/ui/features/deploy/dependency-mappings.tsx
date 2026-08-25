import { useEffect, useId, useRef } from "react";
import { Button, Input } from "@/components/ui/primitives";
import type { DetectedDependency } from "@/lib/contracts/registry";

type Mapping = NonNullable<DetectedDependency["injections"]>[number];

const managedSources: Record<string, Array<[string, string]>> = {
  postgres: [
    ["connection.postgres.uri", "PostgreSQL URI"], ["connection.postgres.npgsql", "Npgsql connection string"],
    ["connection.postgres.jdbc", "JDBC URL"], ["connection.postgres.pdo_dsn", "PDO DSN"],
    ["resource.host", "Host"], ["resource.port", "Port"], ["credential.database", "Database"],
    ["credential.username", "Username"], ["credential.password", "Password"], ["connection.template", "Safe template"],
  ],
  redis: [
    ["connection.redis.uri", "Redis URI"], ["connection.redis.stackexchange", "StackExchange.Redis"],
    ["resource.host", "Host"], ["resource.port", "Port"], ["credential.database", "Database index"],
    ["credential.username", "Username"], ["credential.password", "Password"], ["connection.template", "Safe template"],
  ],
  nats: [["connection.nats.uri", "NATS URI"], ["resource.host", "Host"], ["resource.port", "Port"], ["connection.template", "Safe template"]],
};

const applicationSources: Record<string, Array<[string, string]>> = {
  internal_http: [["application.internal_url", "Internal URL"], ["application.internal_host", "Internal host"], ["application.internal_port", "Internal port"], ["application.path", "Path"]],
  public_http: [["application.public_url", "Public URL"], ["application.public_host", "Public host"], ["application.public_port", "Public port"], ["application.public_scheme", "Public scheme"], ["application.path", "Path"], ["application.url", "URL"]],
  same_origin: [["application.path", "Same-origin path"], ["application.url", "Same-origin URL"]],
};

export function DependencyMappings({ dependency, disabled, onChange }: { dependency: DetectedDependency; disabled: boolean; onChange: (mappings: Mapping[]) => void }) {
  const mappings = dependency.injections || [];
  const options = dependency.protocol === "http" ? applicationSources[dependency.strategy || ""] || [] : managedSources[dependency.protocol] || [];
  return <fieldset className="space-y-3 lg:col-span-4" disabled={disabled}>
    <legend className="text-sm font-medium">Injection mappings</legend>
    {mappings.length === 0 && <p className="text-sm text-on-surface-variant">No environment mapping has been selected.</p>}
    <div className="space-y-2">{mappings.map((mapping, index) => <MappingRow dependency={dependency} key={`${mapping.environment_name}-${index}`} mapping={mapping} onMapping={(next) => onChange(mappings.map((item, itemIndex) => itemIndex === index ? next : item))} onRemove={() => onChange(mappings.filter((_, itemIndex) => itemIndex !== index))} options={options} />)}</div>
    <Button onClick={() => onChange([...mappings, { environment_name: "", symbolic_source: options[0]?.[0] || "" }])} type="button" variant="secondary">Add mapping</Button>
  </fieldset>;
}

function MappingRow({ dependency, mapping, onMapping, onRemove, options }: { dependency: DetectedDependency; mapping: Mapping; onMapping: (mapping: Mapping) => void; onRemove: () => void; options: Array<[string, string]> }) {
  const id = useId();
  const templateRef = useRef<HTMLTextAreaElement>(null);
  const templateSelected = mapping.symbolic_source === "connection.template";
  const templateError = templateSelected ? connectionTemplateError(mapping.template || "") : "";
  const deprecated = mapping.symbolic_source === "connection.url" || /^resource\..+\.connection_string$/.test(mapping.symbolic_source);
  const known = options.some(([source]) => source === mapping.symbolic_source);
  useEffect(() => { if (templateSelected) templateRef.current?.focus(); }, [templateSelected]);
  return <article className="border border-outline-variant/30 bg-surface-container-lowest p-3">
    <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end">
      <label className="grid gap-1 text-sm" htmlFor={`${id}-environment`}><span className="font-medium">Environment name</span><Input id={`${id}-environment`} value={mapping.environment_name} onChange={(event) => onMapping({ ...mapping, environment_name: event.target.value })} /></label>
      <label className="grid gap-1 text-sm" htmlFor={`${id}-source`}><span className="font-medium">Dialect / value</span><select className={selectClass} id={`${id}-source`} value={mapping.symbolic_source} onChange={(event) => onMapping({ environment_name: mapping.environment_name, symbolic_source: event.target.value, ...(event.target.value === "connection.template" ? { template: "" } : {}) })}>{!known && <option value={mapping.symbolic_source}>{deprecated ? "Deprecated URI alias" : mapping.symbolic_source || "Select a dialect"}</option>}{options.map(([source, label]) => <option key={source} value={source}>{label}</option>)}</select></label>
      <Button aria-label={`Remove mapping ${mapping.environment_name || indexLabel(mapping)}`} onClick={onRemove} type="button" variant="ghost">Remove</Button>
    </div>
    {templateSelected && <label className="mt-3 grid gap-1 text-sm" htmlFor={`${id}-template`}><span className="font-medium">Safe connection template</span><textarea aria-describedby={`${id}-template-help${templateError ? ` ${id}-template-error` : ""}`} aria-invalid={Boolean(templateError)} className={`${selectClass} min-h-24 py-2 font-mono`} id={`${id}-template`} maxLength={1024} onChange={(event) => onMapping({ ...mapping, template: event.target.value })} ref={templateRef} value={mapping.template || ""} /><span className="text-xs text-on-surface-variant" id={`${id}-template-help`}>{"Use {{host}}, {{port}}, {{database}} and encoded credentials such as {{password|kv_quote}}."}</span>{templateError && <span className="text-xs text-error" id={`${id}-template-error`} role="alert">{templateError}</span>}</label>}
    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-on-surface-variant"><span>Sensitivity: <strong className="text-on-surface">{mappingSensitivity(dependency.protocol, mapping)}</strong></span><span>Example: <code>{mappingExample(dependency.protocol, mapping)}</code></span>{deprecated && <span className="text-status-warning">Deprecated URI alias; choose a typed dialect before export.</span>}</div>
  </article>;
}

export function deploymentMappingError(dependencies: DetectedDependency[]): boolean {
  return dependencies.some((dependency) => (dependency.injections || []).some((mapping) => !mapping.environment_name.trim() || !mapping.symbolic_source || mapping.symbolic_source === "connection.template" && Boolean(connectionTemplateError(mapping.template || ""))));
}

export function connectionTemplateError(template: string): string {
  if (!template) return "Template is required.";
  if (new TextEncoder().encode(template).length > 1024) return "Template must not exceed 1 KiB.";
  if (template.includes("${") || template.includes("$(") || template.includes("`")) return "Environment and command substitution are not allowed.";
  if (/(?:username|user|uid|password|passwd|pwd|secret|token)\s*[=:]\s*(?!\{\{)/i.test(template)) return "Literal credential values are not allowed.";
  const userinfo = template.match(/:\/\/([^/@]*)@/);
  if (userinfo && userinfo[1].replace(/\{\{[^{}]+\}\}/g, "").replaceAll(":", "").trim()) return "Literal URL credentials are not allowed.";
  const placeholders = [...template.matchAll(/\{\{([^{}]+)\}\}/g)];
  const withoutPlaceholders = template.replace(/\{\{[^{}]+\}\}/g, "");
  if (withoutPlaceholders.includes("{{") || withoutPlaceholders.includes("}}")) return "Template braces are not balanced.";
  for (const placeholder of placeholders) {
    const [name, encoder = ""] = placeholder[1].split("|").map((part) => part.trim());
    if (!["host", "port", "database", "username", "password"].includes(name)) return `Placeholder ${name || "(empty)"} is not allowed.`;
    if (["username", "password"].includes(name) && !["url_userinfo", "url_query", "kv_quote"].includes(encoder)) return `${name} requires url_userinfo, url_query, or kv_quote.`;
    if (!["username", "password"].includes(name) && encoder) return `${name} does not accept an encoder.`;
  }
  return "";
}

function mappingSensitivity(protocol: string, mapping: Mapping): "secret" | "non-secret" {
  if (["credential.username", "credential.password", "connection.postgres.uri", "connection.postgres.npgsql", "connection.redis.uri", "connection.redis.stackexchange"].includes(mapping.symbolic_source)) return "secret";
  if ((mapping.symbolic_source === "connection.url" || mapping.symbolic_source.endsWith(".connection_string")) && protocol !== "nats") return "secret";
  if (mapping.symbolic_source === "connection.template" && /\{\{\s*(username|password)\|/.test(mapping.template || "")) return "secret";
  return "non-secret";
}

function mappingExample(protocol: string, mapping: Mapping): string {
  if (mappingSensitivity(protocol, mapping) === "secret") return `[${mapping.symbolic_source} · redacted]`;
  const examples: Record<string, string> = { "resource.host": "resource.internal", "resource.port": protocol === "nats" ? "4222" : protocol === "redis" ? "6379" : "5432", "credential.database": protocol === "redis" ? "0" : "app", "connection.postgres.jdbc": "jdbc:postgresql://resource.internal:5432/app", "connection.postgres.pdo_dsn": "pgsql:host=resource.internal;port=5432;dbname=app", "connection.nats.uri": "nats://resource.internal:4222" };
  return examples[mapping.symbolic_source] || (mapping.symbolic_source === "connection.template" ? "Template output (credentials redacted)" : "Resolved at deploy time");
}

function indexLabel(mapping: Mapping) { return mapping.symbolic_source || "row"; }
const selectClass = "min-h-10 w-full border border-outline-variant/40 bg-surface-container-lowest px-3 text-sm text-on-surface focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary";
