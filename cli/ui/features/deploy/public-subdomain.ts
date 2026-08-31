export const publicSubdomainSuffix = "test.opsidev.site";

const reservedSubdomains = new Set(["example", "internal", "invalid", "local", "localhost"]);
const dnsLabel = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

export function validatePublicSubdomain(value: string) {
  if (!value) return "A public subdomain is required.";
  if (value !== value.trim() || value.includes(".") || !dnsLabel.test(value.toLowerCase()) || reservedSubdomains.has(value.toLowerCase())) return "Enter one available DNS label without spaces, dots, or a URL.";
  return "";
}

export function publicHostname(value: string) {
  return `${value.trim().toLowerCase()}.${publicSubdomainSuffix}`;
}

export function publicSubdomainFromHostname(value?: string) {
  const suffix = `.${publicSubdomainSuffix}`;
  const hostname = value?.trim().toLowerCase() || "";
  return hostname.endsWith(suffix) ? hostname.slice(0, -suffix.length) : hostname;
}
