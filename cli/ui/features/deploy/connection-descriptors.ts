export type ConnectionProtocol = "postgres" | "redis" | "nats" | "http";
export type MappingValue = { environment_name: string; symbolic_source: string; template?: string };

type Sensitivity = "secret" | "non-secret" | "template";
export type SourceDescriptor = { source: string; label: string; sensitivity: Sensitivity; example?: string };
type ProtocolDescriptor = { credentialTemplates: boolean; sources: SourceDescriptor[] };

const atomic = {
  host: { source: "resource.host", label: "Host", sensitivity: "non-secret" },
  port: { source: "resource.port", label: "Port", sensitivity: "non-secret" },
  database: { source: "credential.database", label: "Database", sensitivity: "non-secret" },
  username: { source: "credential.username", label: "Username", sensitivity: "secret" },
  password: { source: "credential.password", label: "Password", sensitivity: "secret" },
  template: { source: "connection.template", label: "Safe template", sensitivity: "template" },
} satisfies Record<string, SourceDescriptor>;

export const connectionProtocols: ConnectionProtocol[] = ["postgres", "redis", "nats", "http"];

export const connectionCatalog: Record<Exclude<ConnectionProtocol, "http">, ProtocolDescriptor> = {
  postgres: { credentialTemplates: true, sources: [
    { source: "connection.postgres.uri", label: "PostgreSQL URI", sensitivity: "secret" },
    { source: "connection.postgres.npgsql", label: "Npgsql connection string", sensitivity: "secret" },
    { source: "connection.postgres.jdbc", label: "JDBC URL", sensitivity: "non-secret", example: "jdbc:postgresql://resource.internal:5432/app" },
    { source: "connection.postgres.pdo_dsn", label: "PDO DSN", sensitivity: "non-secret", example: "pgsql:host=resource.internal;port=5432;dbname=app" },
    atomic.host, atomic.port, atomic.database, atomic.username, atomic.password, atomic.template,
  ] },
  redis: { credentialTemplates: true, sources: [
    { source: "connection.redis.uri", label: "Redis URI", sensitivity: "secret" },
    { source: "connection.redis.stackexchange", label: "StackExchange.Redis", sensitivity: "secret" },
    atomic.host, atomic.port, { ...atomic.database, label: "Database index" }, atomic.username, atomic.password, atomic.template,
  ] },
  nats: { credentialTemplates: false, sources: [
    { source: "connection.nats.uri", label: "NATS URI", sensitivity: "non-secret", example: "nats://resource.internal:4222" },
    atomic.host, atomic.port, atomic.template,
  ] },
};

const applicationCatalog: Record<string, SourceDescriptor[]> = {
  internal_http: [source("application.internal_url", "Internal URL"), source("application.internal_host", "Internal host"), source("application.internal_port", "Internal port"), source("application.path", "Path")],
  public_http: [source("application.public_url", "Public URL"), source("application.public_host", "Public host"), source("application.public_port", "Public port"), source("application.public_scheme", "Public scheme"), source("application.path", "Path"), source("application.url", "URL")],
  same_origin: [source("application.path", "Same-origin path"), source("application.url", "Same-origin URL")],
};

function source(value: string, label: string): SourceDescriptor {
  return { source: value, label, sensitivity: "non-secret" };
}

export function sourceOptions(protocol: string, strategy?: string): SourceDescriptor[] {
  if (protocol === "http") return applicationCatalog[strategy || ""] || [];
  return connectionCatalog[protocol as Exclude<ConnectionProtocol, "http">]?.sources || [];
}

export function transitionMappings(mappings: MappingValue[], protocol: string, strategy?: string): MappingValue[] {
  const descriptor = protocol === "http" ? undefined : connectionCatalog[protocol as Exclude<ConnectionProtocol, "http">];
  const allowed = new Set(sourceOptions(protocol, strategy).map((item) => item.source));
  return mappings.map((mapping) => {
    if (!allowed.has(mapping.symbolic_source)) return { environment_name: mapping.environment_name, symbolic_source: "" };
    if (mapping.symbolic_source === "connection.template" && connectionTemplateError(mapping.template || "", descriptor?.credentialTemplates ?? false)) {
      return { environment_name: mapping.environment_name, symbolic_source: "" };
    }
    return mapping;
  });
}

type TemplateToken = { literal?: string; placeholder?: string; encoder?: string };
const assignmentEnd = /(username|user|uid|password|passwd|pwd|secret|token)\s*[:=]\s*$/i;
const assignmentAny = /(username|user|uid|password|passwd|pwd|secret|token)\s*[:=]/i;

export function connectionTemplateError(template: string, credentialTemplates = true): string {
  if (!template) return "Template is required.";
  if (new TextEncoder().encode(template).length > 1024) return "Template must not exceed 1 KiB.";
  if (template.includes("${") || template.includes("$(") || template.includes("`")) return "Environment and command substitution are not allowed.";
  const tokens: TemplateToken[] = [];
  let remaining = template;
  while (remaining) {
    const start = remaining.indexOf("{{");
    if (start < 0) {
      if (remaining.includes("}}")) return "Template braces are not balanced.";
      tokens.push({ literal: remaining });
      break;
    }
    if (remaining.slice(0, start).includes("}}")) return "Template braces are not balanced.";
    if (start > 0) tokens.push({ literal: remaining.slice(0, start) });
    const end = remaining.indexOf("}}", start + 2);
    if (end < 0) return "Template braces are not balanced.";
    const parts = remaining.slice(start + 2, end).trim().split("|");
    if (parts.length > 2) return "A placeholder may contain only one encoder segment.";
    const name = parts[0].trim();
    const encoder = parts[1]?.trim() || "";
    if (!["host", "port", "database", "username", "password"].includes(name)) return "Template contains an unsupported placeholder.";
    if (["username", "password"].includes(name) && !["url_userinfo", "url_query", "kv_quote"].includes(encoder)) return `${name} requires url_userinfo, url_query, or kv_quote.`;
    if (!["username", "password"].includes(name) && encoder) return `${name} does not accept an encoder.`;
    tokens.push({ placeholder: name, encoder });
    remaining = remaining.slice(end + 2);
  }
  for (let index = 0; index < tokens.length; index += 1) {
    const literal = tokens[index].literal;
    if (literal === undefined) continue;
    const match = literal.match(assignmentEnd);
    if (!match) {
      if (assignmentAny.test(literal)) return "Literal credential assignments are not allowed.";
      continue;
    }
    if (assignmentAny.test(literal.slice(0, match.index))) return "Literal credential assignments are not allowed.";
    const next = tokens[index + 1];
    const name = match[1].toLowerCase();
    const expected = ["username", "user", "uid"].includes(name) ? "username" : "password";
    const following = tokens[index + 2];
    const terminates = !following || following.literal !== undefined && (!following.literal || ";,&@\r\n".includes(following.literal[0]));
    if (next?.placeholder !== expected || !["url_userinfo", "url_query", "kv_quote"].includes(next.encoder || "") || !terminates) {
      return "Credential assignments must use the matching encoded placeholder as the entire value.";
    }
  }
  const rendered = tokens.map((token) => token.literal ?? `\0${token.placeholder}|${token.encoder}\0`).join("");
  for (let offset = 0; offset < rendered.length;) {
    const scheme = rendered.indexOf("://", offset);
    if (scheme < 0) break;
    const start = scheme + 3;
    const at = rendered.indexOf("@", start);
    if (at < 0) break;
    const userinfo = rendered.slice(start, at);
    const username = "\0username|url_userinfo\0";
    const password = "\0password|url_userinfo\0";
    if (userinfo !== username && userinfo !== `${username}:${password}` && userinfo !== `:${password}`) return "URL userinfo must use exact encoded credential placeholders.";
    offset = at + 1;
  }
  const sensitive = tokens.some((token) => token.placeholder === "username" || token.placeholder === "password");
  if (sensitive && !credentialTemplates) return "NATS templates cannot reference credential placeholders.";
  return "";
}

export function mappingSensitivity(protocol: string, mapping: MappingValue): "secret" | "non-secret" {
  const descriptor = sourceOptions(protocol).find((item) => item.source === mapping.symbolic_source);
  if (descriptor?.sensitivity === "secret") return "secret";
  if (descriptor?.sensitivity === "template" && /\{\{\s*(username|password)\s*\|/.test(mapping.template || "")) return "secret";
  return "non-secret";
}

export function mappingPreview(protocol: string, mapping: MappingValue): string {
  if (mappingSensitivity(protocol, mapping) === "secret") return `[${mapping.symbolic_source} · redacted]`;
  const descriptor = sourceOptions(protocol).find((item) => item.source === mapping.symbolic_source);
  if (descriptor?.example) return descriptor.example;
  if (mapping.symbolic_source === "resource.host") return "resource.internal";
  if (mapping.symbolic_source === "resource.port") return protocol === "nats" ? "4222" : protocol === "redis" ? "6379" : "5432";
  if (mapping.symbolic_source === "credential.database") return protocol === "redis" ? "0" : "app";
  return mapping.symbolic_source === "connection.template" ? "Template output (credentials redacted)" : "Resolved at deploy time";
}
