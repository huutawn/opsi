import { expect, test, type Page, type Route } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

type Scenario = "default" | "ttl" | "agent-down" | "cloud-down" | "signed-out" | "audit-empty" | "audit-unavailable" | "unknown" | "unresolved" | "malformed" | "delivery-loading";

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Security Center provides read-only access and identities without secret mutation or reveal controls", async ({ page }) => {
  const requests: Array<{ path: string; body: Record<string, unknown> }> = [];
  await mockLocalAPI(page, "default", requests);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/?project=proj-1&view=security&tab=access");
  await expect(page.getByRole("heading", { name: "Access & Identities", exact: true })).toBeVisible();
  await expect(page.getByText("Authenticated Session")).toBeVisible();
  await expect(page.getByText("Authority Connections")).toBeVisible();
  await expect(page.getByText("Connected Nodes & Machine Authorities")).toBeVisible();
  await expect(page.getByText("Application Credential Status")).toBeVisible();
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe04/security-access-1440x900.png" });

  // Strictly no secret create, rotate, reveal, or TOTP controls
  await expect(page.getByRole("button", { name: /Reveal Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Rotate Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Create Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Set up TOTP/i })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: /Operation/i })).toHaveCount(0);

  // Cross-surface link navigation
  await expect(page.getByRole("button", { name: /Open Application/i }).first()).toBeVisible();

  // Signed-out transition
  await mockLocalAPI(page, "signed-out", requests);
  await page.getByRole("button", { name: "Refresh current data" }).evaluate((button: HTMLButtonElement) => button.click());
  await expect(page.getByRole("heading", { name: "Sign in to Opsi" })).toBeVisible();
  await page.getByRole("button", { name: "Continue with GitHub" }).click();
  await expect.poll(() => requests.find((item) => item.path.endsWith("/session/login/start"))?.body.project_id).toBe("proj-1");
  await expect(page).toHaveURL(/oauth_fixture=1/);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?project=proj-1&view=security&tab=access");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe04/security-access-390x844.png" });
});

test("Agent availability, audit explorer, and unavailable history stay factual", async ({ page }) => {
  await mockLocalAPI(page, "default");
  await page.goto("/?project=proj-1&view=security&tab=audit");
  await expect(page.getByRole("button", { name: /Human actor/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Agent/ })).toBeVisible();
  await page.getByLabel("Search audit trail").fill("deploy");
  await expect(page.getByRole("button", { name: /deploy/ })).toBeVisible();
  await page.getByRole("combobox", { name: "Outcome" }).selectOption("denied");
  await expect(page.getByRole("button", { name: /deploy\.failed.*denied/i })).toBeVisible();
  await page.getByLabel("From date").fill("2026-07-31");
  await expect(page.getByText("No loaded audit events match these filters.", { exact: true })).toBeVisible();
  await page.getByLabel("From date").fill("2026-07-30");
  await expect(page.getByText("req-audit-2", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("nested redacted", { exact: true })).toHaveCount(0);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe04/audit-1440x900.png" });

  await page.goto("/?project=proj-1&view=security&tab=access");
  await expect(page.getByRole("heading", { name: "Access & Identities", exact: true })).toBeVisible();

  await mockLocalAPI(page, "audit-empty");
  await page.goto("/?project=proj-1&view=security&tab=audit");
  await expect(page.getByText("No audit events were returned.", { exact: true })).toBeVisible();
  await mockLocalAPI(page, "audit-unavailable");
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/audit", status: 503, method: "GET" });
  await page.reload();
  await expect(page.getByRole("heading", { name: "Request failed" })).toBeVisible();
  await expect(page.getByText("audit unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("No audit events were returned.", { exact: true })).toHaveCount(0);
});

test("Settings exposes local facts, PAT review, capability limits, and connection degradation", async ({ page }) => {
  await mockLocalAPI(page, "default");
  await page.goto("/?project=proj-1&view=settings");
  await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "General", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "System", exact: true }).click();
  await expect(page.getByRole("heading", { name: "System", exact: true })).toBeVisible();
  await page.getByText("Capability limits", { exact: true }).click();
  await expect(page.getByText("organization listing", { exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Authentication", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Authentication", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review PAT rotation" }).click();
  await expect(page.getByText(/replace the PAT in the OS secure store/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Local API receipt: rotated true/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await page.getByRole("button", { name: "Review revoke and sign out" }).click();
  await page.getByLabel(/Type REVOKE/).fill("REVOKE");
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/PAT revoked true/)).toBeVisible();

  await mockLocalAPI(page, "cloud-down");
  await page.goto("/?project=proj-1&view=settings&tab=integrations");
  await expect(page.getByText("Cloud connection", { exact: true })).toBeVisible();
  await expect(page.locator(".settingsFacts > div").filter({ hasText: "Cloud connection" }).getByText("Unavailable", { exact: true })).toBeVisible();
  await mockLocalAPI(page, "agent-down");
  await page.reload();
  await expect(page.getByText("Agent connection", { exact: true })).toBeVisible();
  await expect(page.locator(".settingsFacts > div").filter({ hasText: "Agent connection" }).getByText("Unavailable", { exact: true })).toBeVisible();

  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=settings");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ fullPage: true, path: `../../.tmp/ui-fe04/settings-${viewport.width}x${viewport.height}.png` });
  }
});

test("Delivery and Observability stabilization never invents conclusions", async ({ page }) => {
  await mockLocalAPI(page, "delivery-loading");
  await page.goto("/?project=proj-1&view=delivery&tab=pipeline");
  await expect(page.getByText("Loading delivery pipeline", { exact: true })).toBeVisible();
  await expect(page.getByText("Source not configured", { exact: true })).toHaveCount(0);
  await expect(page.getByText("No trusted BuildRecord received", { exact: true })).toHaveCount(0);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe04/delivery-loading-1440x900.png" });
  await page.waitForTimeout(1_000);

  await mockLocalAPI(page, "unknown");
  await page.goto("/?project=proj-1&view=observability&tab=metrics");
  for (const title of ["Restarts", "Recent errors"]) {
    const panel = page.getByRole("heading", { name: title, exact: true }).locator("..");
    await expect(panel.locator(".status.unknown").first()).toBeVisible();
    await expect(panel.locator(".status.healthy")).toHaveCount(0);
  }

  await mockLocalAPI(page, "unresolved");
  await page.goto("/?project=proj-1&view=observability&tab=health");
  await expect(page.getByText("Unresolved identity", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Incidents", exact: true }).click();
  await page.getByRole("button", { name: /inc-1/ }).click();
  await expect(page.getByText("Evidence unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("opsi action preflight", { exact: false })).toBeVisible();
  await expect(page.getByText("opsi incident preflight", { exact: false })).toHaveCount(0);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe04/incident-handoff-1440x900.png" });

  await mockLocalAPI(page, "malformed");
  await page.goto("/?project=proj-1&view=observability&tab=incidents&incident=inc-1");
  await expect(page.getByText("Evidence unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText("Partial evidence", { exact: true })).toHaveCount(0);
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ fullPage: true, path: `../../.tmp/ui-fe04/observability-${viewport.width}x${viewport.height}.png` });
  }
});

async function mockLocalAPI(page: Page, scenario: Scenario, requests: Array<{ path: string; body: Record<string, unknown> }> = []) {
  await page.unroute("**/api/local/**").catch(() => undefined);
  await page.route("**/api/local/**", (route) => respond(route, scenario, requests));
}

async function respond(route: Route, scenario: Scenario, requests: Array<{ path: string; body: Record<string, unknown> }>) {
  const url = new URL(route.request().url());
  const path = url.pathname;
  const projectID = path.match(/projects\/([^/]+)/)?.[1] ?? "proj-1";
  if (scenario === "delivery-loading" && path.endsWith("/build-records")) await new Promise((resolve) => setTimeout(resolve, 900));
  if (path === "/api/local/session") return json(route, { authenticated: scenario !== "signed-out", cloud_connected: scenario === "cloud-down" ? "failed" : "ok", agent_connected: scenario === "agent-down" ? "failed" : "ok", org_id: "org-1", project_id: projectID, token_status: scenario === "signed-out" ? "invalid" : "valid" });
  if (path.endsWith("/session/login/start")) {
    const body = JSON.parse(route.request().postData() || "{}") as Record<string, unknown>;
    requests.push({ path, body });
    return json(route, { auth_url: "/?oauth_fixture=1", status: "pending" });
  }
  if (path === "/api/local/settings") return json(route, { version: "0.8.0", revision: "fe04-fixture", go_version: "go1.26.4", cloud_authority: "github", cloud_configured: true, agent_configured: scenario !== "agent-down", agent_tls_pinned: true, config_selected: true, ui_assets: "opsi-ui", backend_gaps: [{ capability: "organization listing", status: "unsupported", roadmap: "R5-017" }] });
  if (path === "/api/local/projects") return json(route, { projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }, { id: "proj-2", org_id: "org-1", name: "Payments", slug: "payments", status: "ready" }] });
  if (path.endsWith("/session/project")) return json(route, { status: "selected", project_id: projectID });
  if (path.endsWith("/session/token/rotate")) return json(route, { rotated: true, revoked_old: true });
  if (path.endsWith("/session/token/revoke")) return json(route, { authenticated: false, revoked: true });
  if (path.endsWith("/readiness")) return json(route, { project_id: projectID, status: "ready", can_deploy: true });
  if (path.endsWith("/nodes")) return json(route, { nodes: [] });
  if (path.endsWith("/services")) return json(route, { services: [{ id: "svc-web", name: "web", type: "application", status: "ready", source_type: "image", replicas: 2 }] });
  if (path.endsWith("/deployments")) return json(route, { deployments: [] });
  if (path.endsWith("/bootstrap-sessions")) return json(route, { sessions: [] });
  if (path.endsWith("/bootstrap-sessions/")) return json(route, []);
  if (path.endsWith("/audit")) {
    if (scenario === "audit-unavailable") return json(route, { message: "audit unavailable" }, 503);
    return json(route, { events: scenario === "audit-empty" ? [] : auditEvents });
  }
  if (path.endsWith("/support")) return json(route, support);
  if (path.endsWith("/topology/facts")) return json(route, placement);
  if (path.endsWith("/topology")) return json(route, topology);
  if (path.endsWith("/build-records")) return json(route, { records: [] });
  if (path.endsWith("/deployment-policies")) return json(route, { policies: [] });
  if (path.endsWith("/telemetry/summary")) return json(route, { project_id: projectID, source: "agent", payload_policy: "redacted", end_unix: 1785290900, health: "unknown" });
  if (path.includes("/telemetry/services/")) {
    const telemetry = scenario === "unresolved" ? [{ service_id: "display-name-only", health: "healthy", pod_count: 1, ready_pods: 1 }] : scenario === "unknown" ? [{ service_id: "svc-web", health: "healthy", pod_count: 1, ready_pods: 1 }] : [{ service_id: "svc-web", health: "healthy", pod_count: 1, ready_pods: 1, restart_count: 0, recent_error_count: 0 }];
    return json(route, { project_id: projectID, source: "agent", payload_policy: "redacted", services: telemetry });
  }
  if (path.endsWith("/incidents/inc-1/evidence")) return json(route, scenario === "malformed" || scenario === "unresolved" ? {} : evidence);
  if (path.endsWith("/incidents/inc-1")) return json(route, { source: "agent", payload_policy: "redacted", incident: incident });
  if (path.endsWith("/incidents")) return json(route, { source: "agent", payload_policy: "redacted", incidents: [incident] });
  return json(route, {});
}

async function json(route: Route, body: unknown, status = 200) { await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status }); }

const incident = { incident_id: "inc-1", project_id: "proj-1", service_id: "svc-web", status: "open", severity: "warning", anomaly_type: "readiness", created_at_unix: 1785290100 };
const auditEvents = [
  { id: "audit-human", actor_user_id: "user-1", actor_type: "human", action: "service.create", resource_type: "service", resource_id: "svc-web", result: "success", metadata_redacted: { request_id: "req-audit-1", nested: { hidden: true } }, created_at: "2026-07-30T09:00:00Z" },
  { id: "audit-machine", actor_type: "agent", action: "deploy.failed", resource_type: "deployment", resource_id: "dep-1", result: "denied", metadata_redacted: { request_id: "req-audit-2", reason: "policy" }, created_at: "2026-07-30T09:01:00Z" },
];
const evidence = { schema_version: "opsi.incident_evidence/v1", identity: incident, generated_at_unix: 1785290900, observation_window: { start_unix: 1785290000, end_unix: 1785290900 }, deployment: {}, rollout: {}, coverage: [{ source: "rollout", status: "available", item_count: 1, truncated: false }], content_sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" };
const placement = { project_id: "proj-1", environments: [], runtimes: [], nodes: [], agents: [], services: [] };
const topology = { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: "proj-1", revision: 1, state_hash: "state", plan_hash: "plan", created_by: "fixture", applied_by: "fixture", created_at: "2026-07-30T09:00:00Z", applied_at: "2026-07-30T09:00:00Z", assignments: [] };
const support = { generated_at: "2026-07-30T09:00:00Z", readiness: { project_id: "proj-1", status: "ready", can_deploy: true }, counts: { nodes: 0, healthy_nodes: 0, services: 1, deployment_jobs: 0, failed_deployments: 0, bootstrap_sessions: 0, open_bootstrap_jobs: 0, audit_events: 2 }, dashboard: { title: "Fixture", datasource: "local", refresh: "30s", panels: [] }, signals: [], active_alerts: [], configured_alerts: [], production_gates: [], break_glass_policy: { time_limited: true, approval_required: true, reason_required: true, audited: true, secret_reveal_by_default: false, owner_notification: "required" }, runbooks: [], recent_request_ids: [] };
