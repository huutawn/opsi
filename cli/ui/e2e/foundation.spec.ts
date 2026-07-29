import { expect, test, type Page, type Route } from "@playwright/test";

type Scenario = "healthy" | "degraded" | "unavailable" | "empty" | "long" | "failed-build";

test("workspace, grouped navigation, restoration, and back-forward behavior", async ({ page }) => {
  await mockLocalAPI(page, "healthy");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Overview" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: /Checkout Platform/ })).toBeVisible();
  await expect(page.locator(".projectRow .status").first()).toHaveText("Unknown");
  await page.getByRole("link", { name: /Checkout Platform/ }).click();
  await expect(page.getByRole("link", { name: "Overview" })).toBeVisible();
  for (const destination of ["Overview", "Services", "Delivery", "Infrastructure", "Observability", "Security"]) await expect(page.getByRole("link", { name: destination, exact: true })).toBeVisible();
  await expect(page.locator(".navSection a")).toHaveCount(6);

  await page.getByRole("link", { name: "Observability", exact: true }).click();
  await page.getByRole("link", { name: "Logs", exact: true }).click();
  await expect(page).toHaveURL(/view=observability&tab=logs/);
  await page.reload();
  await expect(page.getByRole("link", { name: "Logs", exact: true })).toHaveAttribute("aria-current", "page");
  await page.getByRole("link", { name: "Security", exact: true }).click();
  await expect(page).toHaveURL(/view=security&tab=secrets/);
  await page.goBack();
  await expect(page.getByRole("link", { name: "Logs", exact: true })).toHaveAttribute("aria-current", "page");
  await page.goForward();
  await expect(page.getByRole("link", { name: "Secrets", exact: true })).toHaveAttribute("aria-current", "page");
});

test("truthful project summaries cover required factual fixtures", async ({ page }) => {
  let scenario: Scenario = "healthy";
  await mockLocalAPI(page, () => scenario);
  for (const [next, expected] of [
    ["healthy", "Healthy"],
    ["degraded", "Degraded"],
    ["unavailable", "Unavailable"],
    ["failed-build", "Failed"],
  ] as Array<[Scenario, string]>) {
    scenario = next;
    await page.goto("/?project=proj-1&view=overview");
    await expect(page.locator(".statusLead strong")).toHaveText(expected);
    if (next === "degraded") {
      await expect(page.getByText("1/2", { exact: true })).toBeVisible();
      await expect(page.getByText(/incident/i).first()).toBeVisible();
    }
    if (next === "unavailable") {
      await expect(page.getByText("Agent unavailable", { exact: true })).toBeVisible();
      await expect(page.getByText("Unavailable", { exact: true }).first()).toBeVisible();
      await expect(page.getByText("Incident source missing", { exact: true })).toBeVisible();
      await page.getByRole("link", { name: "Services", exact: true }).click();
      await expect(page.locator(".serviceRow .status").first()).toHaveText("Unavailable");
    }
    if (next === "failed-build") await expect(page.getByText(/Latest build failed for worker/)).toBeVisible();
  }

  scenario = "empty";
  await page.goto("/?project=proj-1&view=overview");
  await expect(page.locator(".statusLead strong")).toHaveText("Unknown");
  await page.goto("/?project=proj-1&view=services");
  await expect(page.getByText("No services yet", { exact: true })).toBeVisible();
  await expect(page.getByText(/postgres|redis/i)).toHaveCount(0);

  scenario = "long";
  await page.goto("/?project=proj-1&view=services");
  await expect(page.getByText(/checkout-api-with-an-intentionally-long/)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("switching projects clears scoped service detail", async ({ page }) => {
  await mockLocalAPI(page, "healthy");
  await page.goto("/?project=proj-1&view=services");
  await page.locator(".serviceRow").first().click();
  await expect(page.getByRole("heading", { name: "api" })).toBeVisible();
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: /Payments/ }).click();
  await expect(page).toHaveURL(/project=proj-2/);
  await expect(page.locator(".detailDrawer")).toHaveCount(0);
});

test("required viewports have no horizontal overflow and produce review screenshots", async ({ page }) => {
  await mockLocalAPI(page, "degraded");
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=overview");
    await expect(page.locator(".statusLead strong")).toHaveText("Degraded");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ fullPage: true, path: `../../.tmp/ui-fe01/overview-${viewport.width}x${viewport.height}.png` });
  }
});

async function mockLocalAPI(page: Page, selected: Scenario | (() => Scenario)) {
  await page.route("**/api/local/**", async (route) => respond(route, typeof selected === "function" ? selected() : selected));
}

async function respond(route: Route, scenario: Scenario) {
  const url = new URL(route.request().url());
  const path = url.pathname;
  const projectID = path.match(/projects\/(proj-[^/]+)/)?.[1] ?? "proj-1";
  const data = fixture(projectID, scenario);
  let body: unknown;
  if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: scenario === "unavailable" ? "failed" : "ok", org_id: "org-1", project_id: projectID, capabilities: [] };
  else if (path === "/api/local/session/project") body = { status: "selected", project_id: projectID };
  else if (path === "/api/local/projects") body = { projects: data.projects };
  else if (path.endsWith("/readiness")) body = data.readiness;
  else if (path.endsWith("/nodes")) body = data.nodes;
  else if (path.endsWith("/services")) body = { services: data.services };
  else if (path.endsWith("/deployments")) body = { deployments: data.deployments };
  else if (/\/deployments\/[^/]+\/events$/.test(path)) body = { events: [] };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: [] };
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/support")) body = data.support;
  else if (path.endsWith("/topology/facts")) body = data.placement;
  else if (path.endsWith("/topology")) body = data.topology;
  else if (path.endsWith("/build-records")) body = { records: data.builds };
  else if (/\/telemetry\/services\//.test(path)) body = { project_id: projectID, source: "agent", payload_policy: "redacted", services: data.telemetry.filter((item) => path.endsWith(item.service_id)) };
  else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: data.incidents };
  else if (path.endsWith("/logs")) body = { source: "agent", payload_policy: "redacted", logs: [] };
  else body = {};
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
}

function fixture(projectID: string, scenario: Scenario) {
  const long = scenario === "long";
  const empty = scenario === "empty";
  const degraded = scenario === "degraded";
  const services = empty ? [] : [
    { id: "api", name: long ? "checkout-api-with-an-intentionally-long-production-service-name-that-must-wrap-safely" : "api", type: "application", status: "ready", source_type: "image", replicas: 2 },
    { id: "worker", name: "worker", type: "application", status: "ready", source_type: "image", replicas: 2 },
  ];
  const telemetry = services.map((service) => ({ service_id: service.id, health: degraded && service.id === "worker" ? "degraded" : "healthy", pod_count: 2, ready_pods: degraded && service.id === "worker" ? 1 : 2, last_seen_unix: 1785290400 }));
  const deployments = empty ? [] : [
    deployment(projectID, "dep-3", "worker", "succeeded", "2026-07-29T03:00:00Z"),
    deployment(projectID, "dep-2", "api", "failed", "2026-07-28T02:00:00Z"),
    deployment(projectID, "dep-1", "api", "succeeded", "2026-07-27T01:00:00Z"),
  ];
  const builds = empty ? [] : [build(projectID, "api", "succeeded", "2026-07-29T01:00:00Z"), ...(scenario === "failed-build" ? [build(projectID, "worker", "failed", "2026-07-29T04:00:00Z")] : [])];
  const nodes = empty ? [] : [{ id: "node-1", name: "primary-node", role: "worker", status: "healthy", last_seen_at: "2026-07-29T04:00:00Z" }];
  const placement = { project_id: projectID, environments: [{ id: "env-1", project_id: projectID, name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: projectID, environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }], nodes: nodes.map((node) => ({ id: node.id, project_id: projectID, runtime_id: "runtime-1", status: node.status, last_seen_at: node.last_seen_at })), agents: empty ? [] : [{ id: "agent-1", project_id: projectID, runtime_id: "runtime-1", node_id: "node-1", status: "active", capabilities: { deploy: true } }], services: services.map((service) => ({ id: service.id, project_id: projectID, key: service.name })) };
  return {
    projects: [{ id: "proj-1", org_id: "org-1", name: long ? "Checkout Platform With A Very Long Project Name That Must Never Break The Workspace Layout" : "Checkout Platform", slug: "checkout", status: "ready" }, { id: "proj-2", org_id: "org-1", name: "Payments", slug: "payments", status: "ready" }],
    readiness: { project_id: projectID, status: "ready", can_deploy: !empty },
    nodes,
    services,
    deployments,
    builds,
    telemetry,
    incidents: degraded ? [{ incident_id: "inc-1", project_id: projectID, service_id: "worker", status: "open", severity: "warning", anomaly_type: "readiness", created_at_unix: 1785290000 }] : [],
    placement,
    topology: { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: projectID, revision: 1, state_hash: "state", plan_hash: "plan", created_by: "user", applied_by: "user", created_at: "2026-07-27T01:00:00Z", applied_at: "2026-07-27T01:00:00Z", assignments: services.map((service) => ({ service_key: service.name, environment_id: "env-1", runtime_id: "runtime-1", replicas: 2, cpu_request_millicores: 100, memory_request_bytes: 134217728, exposure: { mode: "none" } })) },
    support: { generated_at: "2026-07-29T04:00:00Z", readiness: { project_id: projectID, status: "ready", can_deploy: !empty }, counts: { nodes: nodes.length, healthy_nodes: nodes.length, services: services.length, deployment_jobs: deployments.length, failed_deployments: 1, bootstrap_sessions: 0, open_bootstrap_jobs: 0, audit_events: 0 }, dashboard: { title: "Opsi", datasource: "local", refresh: "30s", panels: [] }, signals: [], active_alerts: [], configured_alerts: [], production_gates: [], runbooks: [], break_glass_policy: { time_limited: true, approval_required: true, reason_required: true, audited: true, secret_reveal_by_default: false, owner_notification: "required" } },
  };
}

function deployment(projectID: string, id: string, serviceID: string, status: string, createdAt: string) { return { id, project_id: projectID, service_id: serviceID, status, created_at: createdAt, updated_at: createdAt, current_digest: `sha256:${id.padEnd(64, "a")}` }; }
function build(projectID: string, serviceKey: string, status: string, createdAt: string) { return { schema_version: "opsi.build_record/v1", id: `build-${serviceKey}-${status}`, project_id: projectID, repository_id: 101, repository_owner_id: 42, active_binding_id: `binding-${serviceKey}`, service_id: serviceKey, service_key: serviceKey, created_at: createdAt, workload: { issuer: "https://token.actions.githubusercontent.com", subject: "repo:example/app", repository_id: 101, repository_owner_id: 42, ref: "refs/heads/main", sha: "abcdef0123456789", event_name: "push", workflow: "build", workflow_ref: "example/app/.github/workflows/build.yml@refs/heads/main", run_id: 7, run_attempt: 1 }, build: { config_hash: "config", plan_hash: "plan", platform: "linux/amd64", oci_repository: `registry.example.test/app/${serviceKey}`, oci_digest: `sha256:${serviceKey.padEnd(64, "b")}`, status } }; }
