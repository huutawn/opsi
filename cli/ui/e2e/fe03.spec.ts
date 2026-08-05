import { expect, test, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => { watchConsoleErrors(page); await page.route("**/api/local/**", (route) => respond(route)); });
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Infrastructure uses canonical edges, truthful capacity, and URL state", async ({ page }) => {
  await page.goto("/?project=proj-1&view=infrastructure");
  await expect(page.getByRole("tab", { name: "Topology", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".breadcrumb")).toHaveText("Projects/Checkout Platform/Production/Topology");
  await expect(page.locator(".topologyEdges line")).toHaveCount(4);
  await expect(page.getByRole("heading", { name: "Unassigned services", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Design", exact: true })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Live", exact: true }).click();
  await expect(page.locator(".liveRuntimeList").getByText("agent-primary", { exact: false })).toBeVisible();
  await expect(page.getByText("node-primary (healthy)", { exact: true })).toBeVisible();
  await expect(page.getByText("dep-1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Design", exact: true }).click();
  await expect(page.getByRole("link", { name: "Support", exact: true })).toHaveCount(0);
  await page.getByRole("tab", { name: "Runtimes", exact: true }).click();
  await page.getByRole("button", { name: /Edge runtime/ }).click();
  await expect(page).toHaveURL(/runtime=runtime-edge/);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Edge runtime" })).toBeVisible();
});

test("Topology onboarding exposes the factual next action for every state", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let scenario: OnboardingScenario = "connect";
  await page.route("**/api/local/**", (route) => respond(route, scenario));
  for (const [next, state, action] of [
    ["connect", "connect", "Connect server"],
    ["bootstrap", "bootstrap", "Inspect progress"],
    ["failed", "retry", "Retry bootstrap"],
    ["application", "application", "Add application"],
    ["placement", "placement", "Plan placement"],
    ["review", "inspect", "Inspect topology"],
  ] as Array<[OnboardingScenario, string, string]>) {
    scenario = next;
    await page.goto(`/?project=proj-1&view=infrastructure&tab=topology&case=${next}`);
    await expect(page.locator(".topologyOnboarding")).toHaveAttribute("data-state", state);
    const button = page.getByRole("button", { name: action, exact: true });
    await expect(button).toBeVisible();
    if (next === "connect") { await button.click(); await expect(page.getByRole("dialog", { name: "Add server" })).toBeVisible(); await page.getByRole("button", { name: "Close add server dialog" }).click(); }
    if (next === "bootstrap") { await expect(page.locator(".topologyOnboarding").getByText(/50% · preflight/)).toBeVisible(); await button.click(); await expect(page).toHaveURL(/tab=topology/); await expect(page.getByRole("heading", { name: "203.0.113.10" })).toBeFocused(); }
    if (next === "failed") { await button.click(); await expect(page.getByRole("dialog", { name: /retry bootstrap session/i })).toBeVisible(); await page.getByRole("button", { name: "Confirm and submit" }).click(); await expect(page.getByText(/Bootstrap boot-failed returned status pending/)).toBeVisible(); await page.getByRole("button", { name: "Close" }).click(); }
    if (next === "application") { await button.click(); await expect(page.getByRole("dialog", { name: "Add service" })).toBeVisible(); await page.getByRole("button", { name: "Close add service dialog" }).click(); }
    if (next === "placement") { await button.click(); await expect(page.getByRole("dialog", { name: "Plan placement" })).toBeVisible(); await page.getByRole("button", { name: "Close placement dialog" }).click(); }
    if (next === "review") { await page.getByRole("button", { name: "Live", exact: true }).click(); await button.click(); await expect(page.getByRole("button", { name: "Design", exact: true })).toHaveAttribute("aria-pressed", "true"); }
  }
});

test("Topology polling moves an active bootstrap to ready and keeps the five latest events inline", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let sessionReads = 0;
  let ready = false;
  const events = Array.from({ length: 7 }, (_, index) => ({ id: `event-${index}`, step: `step-${index}`, message_redacted: `Event ${index}`, progress_percent: index * 10, created_at: `2026-07-30T08:${String(index).padStart(2, "0")}:00Z` }));
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/bootstrap-sessions") && route.request().method() === "GET") {
      sessionReads += 1;
      if (sessionReads > 1) { await new Promise((resolve) => setTimeout(resolve, 250)); ready = true; }
      const session = { id: "boot-1", status: ready ? "succeeded" : "installing", public_host: "203.0.113.10", role: "first_server", checkpoint: { plan_version: "v1", next_step_index: ready ? 4 : 2, last_completed_step: ready ? "agent_ready" : "preflight" }, created_at: "2026-07-30T08:10:00Z" };
      await route.fulfill({ body: JSON.stringify({ sessions: [session] }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/bootstrap-sessions/boot-1/events")) { await route.fulfill({ body: JSON.stringify(events), contentType: "application/json", status: 200 }); return; }
    const data = onboardingFixture(fixture(), ready ? "application" : "bootstrap");
    data.sessions = [{ id: "boot-1", status: ready ? "succeeded" : "installing", public_host: "203.0.113.10", role: "first_server", created_at: "2026-07-30T08:10:00Z" }];
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.getByText("Bootstrapping", { exact: true })).toBeVisible();
  await expect(page.getByText("Ready", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add application" })).toBeVisible();
  await expect(page.locator(".serverLifecycle > .eventTimeline li")).toHaveCount(5);
  await expect(page.getByText("7 total", { exact: true })).toBeVisible();
  await expect(page.getByText("Open full bootstrap details", { exact: true })).toBeVisible();
});

test("bootstrap polling is sequential and stops after a project switch", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let projectOneReads = 0;
  let activeRequests = 0;
  let maxActiveRequests = 0;
  const projects = [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }, { id: "proj-2", org_id: "org-1", name: "Second Project", slug: "second", status: "ready" }];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const projectID = path.includes("/proj-2/") ? "proj-2" : "proj-1";
    if (path === "/api/local/projects") { await route.fulfill({ body: JSON.stringify({ projects }), contentType: "application/json", status: 200 }); return; }
    if (projectID === "proj-1" && path.endsWith("/bootstrap-sessions") && route.request().method() === "GET") {
      projectOneReads += 1;
      activeRequests += 1;
      maxActiveRequests = Math.max(maxActiveRequests, activeRequests);
      await new Promise((resolve) => setTimeout(resolve, 120));
      activeRequests -= 1;
      await route.fulfill({ body: JSON.stringify({ sessions: [{ id: "boot-1", status: "installing", role: "first_server", created_at: "2026-07-30T08:10:00Z" }] }), contentType: "application/json", status: 200 });
      return;
    }
    const data = onboardingFixture(fixture(), projectID === "proj-1" ? "bootstrap" : "application");
    data.projects = projects;
    await respondWithData(route, data, projectID);
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.getByText("Bootstrapping", { exact: true })).toBeVisible();
  await page.waitForTimeout(4_300);
  expect(maxActiveRequests).toBe(1);
  expect(projectOneReads).toBeGreaterThanOrEqual(3);
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: /Second Project/ }).click();
  await expect(page.locator(".breadcrumb")).toContainText("Second Project");
  const readsAfterSwitch = projectOneReads;
  await page.waitForTimeout(4_300);
  expect(projectOneReads).toBe(readsAfterSwitch);
});

test("confirmed service creation refreshes Topology onboarding without a page reload", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let created = false;
  let navigationRequests = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/services") && route.request().method() === "POST") {
      created = true;
      await route.fulfill({ body: JSON.stringify({ id: "api", name: "api", type: "application", status: "ready", source_type: "image" }), contentType: "application/json", status: 200 });
      return;
    }
    const data = onboardingFixture(fixture(), created ? "placement" : "application");
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  page.on("request", (request) => { if (request.isNavigationRequest()) navigationRequests += 1; });
  await page.getByRole("button", { name: "Add application" }).click();
  await page.getByLabel("Name", { exact: true }).fill("api");
  await page.getByRole("button", { name: "Review service" }).click();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Service api created with status ready/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("button", { name: "Plan placement" })).toBeVisible();
  expect(navigationRequests).toBe(0);
});

test("Observability preserves factual semantics, text rendering, evidence, and URL filters", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=health");
  await expect(page.getByText("Degraded", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("Operational evidence")).toBeVisible();
  await page.getByRole("tab", { name: "Metrics", exact: true }).click();
  await expect(page.getByText(/timestamps not reported/i)).toBeVisible();
  await expect(page.getByText(/time axis/i)).toBeVisible();
  await page.getByRole("tab", { name: "Logs", exact: true }).click();
  await expect(page.getByText("<script>alert('x')</script> token=should-hide", { exact: true })).toHaveCount(0);
  await expect(page.getByText(/redaction contract violation/i)).toBeVisible();
  const resume = page.getByRole("button", { name: "Resume periodic refresh" });
  await resume.click();
  await expect(page.getByRole("button", { name: "Pause periodic refresh" })).toHaveAttribute("aria-pressed", "false");
  await page.getByRole("button", { name: "Pause periodic refresh" }).click();
  await page.getByLabel("Search loaded page").fill("timeout");
  await expect(page).toHaveURL(/query=timeout/);
  await page.getByRole("tab", { name: "Incidents", exact: true }).click();
  await page.getByRole("button", { name: /inc-1/ }).click();
  await expect(page.getByText("Partial evidence")).toBeVisible();
  await expect(page.getByText("Continue in CLI")).toBeVisible();
  await expect(page.getByText("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")).toBeVisible();
});

test("Corrupt incident evidence fails closed", async ({ page }) => {
  await page.route("**/incidents/inc-1/evidence", async (route) => { await route.fulfill({ body: "{}", contentType: "application/json", status: 200 }); });
  await page.goto("/?project=proj-1&view=observability&tab=incidents&incident=inc-1");
  await expect(page.getByText("Evidence unavailable")).toBeVisible();
  await expect(page.getByText(/structural validation/i)).toBeVisible();
  await expect(page.getByText("Partial evidence")).toHaveCount(0);
});

test("FE-03 visual acceptance screenshots and overflow", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  for (const [url, path] of [
    ["/?project=proj-1&view=infrastructure&tab=topology", "../../.tmp/ui-fe03/topology-1440x900.png"],
    ["/?project=proj-1&view=infrastructure&tab=topology&topology=agent%3Aagent-stale", "../../.tmp/ui-fe03/topology-degraded-1440x900.png"],
    ["/?project=proj-1&view=infrastructure&tab=bootstrap&session=boot-1", "../../.tmp/ui-fe03/bootstrap-1440x900.png"],
    ["/?project=proj-1&view=observability&tab=health", "../../.tmp/ui-fe03/health-1440x900.png"],
    ["/?project=proj-1&view=observability&tab=metrics", "../../.tmp/ui-fe03/metrics-1440x900.png"],
    ["/?project=proj-1&view=observability&tab=incidents&incident=inc-1", "../../.tmp/ui-fe03/incident-evidence-1440x900.png"],
  ]) {
    await page.goto(url);
    await page.waitForLoadState("networkidle");
    if (url.includes("incidents")) await expect(page.getByText("Partial evidence")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ fullPage: true, path });
  }
  await page.setViewportSize({ width: 1024, height: 768 });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.locator(".topologyTree")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.locator(".topologyTree")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe03/infrastructure-mobile-390x844.png" });
  await page.goto("/?project=proj-1&view=observability&tab=health");
  await expect(page.getByText("Service health matrix")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe03/observability-mobile-390x844.png" });
});

type OnboardingScenario = "base" | "connect" | "bootstrap" | "failed" | "application" | "placement" | "review";

async function respond(route: Route, scenario: OnboardingScenario = "base") {
  await respondWithData(route, onboardingFixture(fixture(), scenario), "proj-1");
}

async function respondWithData(route: Route, data: ReturnType<typeof fixture>, projectID: string) {
  const url = new URL(route.request().url());
  const path = url.pathname;
  let body: unknown = {};
  if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: projectID };
  else if (path === "/api/local/projects") body = { projects: data.projects };
  else if (path.endsWith("/readiness")) body = { project_id: projectID, status: "degraded", can_deploy: true };
  else if (path.endsWith("/nodes")) body = data.nodes;
  else if (/\/nodes\/[^/]+$/.test(path)) body = { node: data.nodes.find((item) => path.endsWith(item.id)), open_bootstrap_events: data.bootstrapEvents, recent_deployment_jobs: data.deployments };
  else if (path.endsWith("/services")) body = { services: data.services };
  else if (path.endsWith("/deployments")) body = { deployments: data.deployments };
  else if (/\/bootstrap-sessions\/[^/]+\/retry$/.test(path)) body = { ...data.sessions.find((session) => path.includes(session.id)), status: "pending" };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: data.sessions };
  else if (/\/bootstrap-sessions\/[^/]+\/events$/.test(path)) body = data.bootstrapEvents;
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/support")) body = data.support;
  else if (path.endsWith("/topology/facts")) body = data.facts;
  else if (path.endsWith("/topology")) body = data.topology;
  else if (path.endsWith("/github/repositories")) body = { repositories: [] };
  else if (path.endsWith("/github/bindings")) body = { bindings: [] };
  else if (path.endsWith("/build-records")) body = { records: [] };
  else if (path.endsWith("/deployment-policies")) body = { policies: [] };
  else if (path.endsWith("/telemetry/summary")) body = { project_id: projectID, since_unix: 0, chunk_count: 1, record_count: 8, start_unix: 1785290000, end_unix: 1785290900, done: true, source: "agent", payload_policy: "redacted", health: "degraded", metric_count: 6, log_count: 2, error_count: 1, service_count: 2 };
  else if (/\/telemetry\/services\//.test(path)) body = { project_id: projectID, source: "agent", payload_policy: "redacted", services: data.telemetry.filter((item) => path.endsWith(item.service_id)) };
  else if (path.endsWith("/logs")) body = { project_id: projectID, source: "agent", payload_policy: "redacted", logs: data.logs };
  else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: data.incidents };
  else if (path.endsWith("/incidents/inc-1/evidence")) body = { ...data.evidence, content_sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" };
  else if (path.endsWith("/incidents/inc-1")) body = { source: "agent", payload_policy: "redacted", incident: data.incidents[0] };
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
}

function onboardingFixture(data: ReturnType<typeof fixture>, scenario: OnboardingScenario) {
  if (scenario === "base") return data;
  if (scenario === "connect" || scenario === "bootstrap") return { ...data, services: [], nodes: [], facts: { ...data.facts, runtimes: [], nodes: [], agents: [], services: [] }, topology: null, sessions: scenario === "bootstrap" ? data.sessions.slice(0, 1) : [] };
  if (scenario === "failed") return { ...data, services: [], nodes: [], facts: { ...data.facts, runtimes: [], nodes: [], agents: [], services: [] }, topology: null, sessions: data.sessions.slice(1) };
  if (scenario === "application") return { ...data, services: [], facts: { ...data.facts, services: [] }, topology: null, sessions: [] };
  if (scenario === "placement") return { ...data, topology: null, sessions: [] };
  return { ...data, services: data.services.slice(0, 2), facts: { ...data.facts, services: data.facts.services.slice(0, 2) }, sessions: [] };
}

function fixture() {
  const services = [{ id: "api", name: "api", type: "application", status: "ready", source_type: "image", replicas: 2 }, { id: "worker", name: "worker", type: "application", status: "ready", source_type: "image", replicas: 2 }, { id: "reports", name: "reports", type: "application", status: "ready", source_type: "image" }];
  const nodes = [{ id: "node-primary", name: "Primary node", role: "server", status: "healthy", cpu_cores: 4, memory_mb: 8192, disk_total_gb: 80, k3s_status: "ready", agent_id: "agent-primary", agent_version: "1.8.0", last_seen_at: "2026-07-30T09:00:00Z" }, { id: "node-edge", name: "Edge node", role: "worker", status: "stale", k3s_status: "ready", agent_id: "agent-stale", agent_version: "1.7.4", last_seen_at: "2026-07-30T08:20:00Z" }];
  const facts = { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-primary", project_id: "proj-1", environment_id: "env-prod", name: "Primary runtime", type: "k3s", status: "ready" }, { id: "runtime-edge", project_id: "proj-1", environment_id: "env-prod", name: "Edge runtime", type: "k3s", status: "degraded" }], nodes: [{ id: "node-primary", project_id: "proj-1", runtime_id: "runtime-primary", status: "healthy", cpu_cores: 4, memory_mb: 8192, last_seen_at: nodes[0].last_seen_at }, { id: "node-edge", project_id: "proj-1", runtime_id: "runtime-edge", status: "stale", last_seen_at: nodes[1].last_seen_at }], agents: [{ id: "agent-primary", project_id: "proj-1", runtime_id: "runtime-primary", node_id: "node-primary", status: "active", capabilities: { deploy: true }, last_seen_at: nodes[0].last_seen_at }, { id: "agent-stale", project_id: "proj-1", runtime_id: "runtime-edge", node_id: "node-edge", status: "stale", capabilities: { deploy: true }, last_seen_at: nodes[1].last_seen_at }], services: services.map((item) => ({ id: item.id, project_id: "proj-1", key: item.name })) };
  const topology = { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: "proj-1", revision: 4, state_hash: "topology-state", plan_hash: "topology-plan", created_by: "owner", applied_by: "owner", created_at: "2026-07-30T08:00:00Z", applied_at: "2026-07-30T08:00:00Z", assignments: [{ service_key: "api", environment_id: "env-prod", runtime_id: "runtime-primary", replicas: 2, cpu_request_millicores: 250, memory_request_bytes: 268435456, exposure: { mode: "none" } }, { service_key: "worker", environment_id: "env-prod", runtime_id: "runtime-edge", replicas: 2, cpu_request_millicores: 200, memory_request_bytes: 268435456, exposure: { mode: "internal" } }] };
  const incidents = [{ incident_id: "inc-1", project_id: "proj-1", service_id: "worker", node_id: "node-edge", pod_id: "worker-7", status: "open", severity: "warning", anomaly_type: "readiness", created_at_unix: 1785290100 }];
  return { projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }], services, nodes, facts, topology, telemetry: [{ service_id: "api", health: "healthy", pod_count: 2, ready_pods: 2, cpu_cores: 0.4, memory_bytes: 268435456, restart_count: 0, recent_error_count: 0, last_seen_unix: 1785290900 }, { service_id: "worker", health: "degraded", pod_count: 2, ready_pods: 1, cpu_cores: 0.2, memory_bytes: 201326592, restart_count: 3, recent_error_count: 1, last_seen_unix: 1785290800 }], incidents, logs: [{ service_id: "worker", pod_id: "worker-7", namespace: "opsi-prod", level: "error", message: "request timeout after 30s", fingerprint: "fp-timeout", observed_unix: 1785290800 }, { service_id: "api", pod_id: "api-9", namespace: "opsi-prod", level: "warning", message: "<script>alert('x')</script> token=should-hide", fingerprint: "fp-untrusted", observed_unix: 1785290700 }], sessions: [{ id: "boot-1", status: "installing", public_host: "203.0.113.10", role: "worker", attempt_count: 1, max_attempts: 3, checkpoint: { plan_version: "v1", next_step_index: 2, last_completed_step: "preflight" }, created_at: "2026-07-30T08:10:00Z" }, { id: "boot-failed", status: "failed", public_host: "203.0.113.11", role: "worker", attempt_count: 3, max_attempts: 3, last_failure_code: "SSH_HOST_KEY_MISMATCH", last_failure_message_redacted: "Pinned host key did not match", created_at: "2026-07-30T07:00:00Z" }], bootstrapEvents: [{ id: "be-1", step: "preflight", message_redacted: "Host identity verified", progress_percent: 0, created_at: "2026-07-30T08:12:00Z" }, { id: "be-2", step: "installing", message_redacted: "Installing K3s and Agent", progress_percent: 0, created_at: "2026-07-30T08:14:00Z" }], deployments: [{ id: "dep-1", service_id: "worker", status: "failed", created_at: "2026-07-30T08:00:00Z" }], support: { generated_at: "2026-07-30T09:00:00Z", readiness: { project_id: "proj-1", status: "degraded", can_deploy: true }, counts: { nodes: 2, healthy_nodes: 1, services: 3, deployment_jobs: 1, failed_deployments: 1, bootstrap_sessions: 2, open_bootstrap_jobs: 1, audit_events: 0 }, dashboard: { title: "Runtime evidence", datasource: "agent", refresh: "30s", panels: [{ id: "ordered", title: "Backend ordered samples", kind: "series", unit: "count", query: "agent.samples", series: [{ name: "worker", status: "degraded", value: 3, points: [1, 2, 3] }] }] }, signals: [{ name: "readiness", status: "warning", value: "3/4", target: "4/4" }], active_alerts: [{ id: "alert-1", severity: "warning", status: "active", title: "Worker readiness degraded", resource_id: "worker", runbook_id: "runbook-1" }], configured_alerts: [{ id: "rule-1", severity: "warning", title: "Readiness", metric: "ready_pods", runbook_id: "runbook-1" }], production_gates: [{ name: "runtime readiness", passed: false, detail: "worker has 1/2 ready pods" }], break_glass_policy: { time_limited: true, approval_required: true, reason_required: true, audited: true, secret_reveal_by_default: false, owner_notification: "required" }, runbooks: [{ id: "runbook-1", title: "Worker readiness", symptoms: "pod not ready", impact: "jobs delayed", dashboard_query: "worker", immediate_mitigation: "inspect rollout", long_term_fix: "fix readiness", customer_communication: "status page", escalation_path: "on-call" }], recent_request_ids: ["request-1"] }, evidence: { schema_version: "opsi.incident_evidence/v1", identity: incidents[0], generated_at_unix: 1785290900, observation_window: { start_unix: 1785290000, end_unix: 1785290900 }, deployment: { desired_digest: "sha256:desired", observed_digest: "sha256:observed" }, rollout: { rollout_id: "rollout-1", state: "failed", failure_code: "READINESS_FAILED", readiness_hash: "readiness-hash" }, timeline: [{ observed_at_unix: 1785290200, source: "kubernetes", kind: "readiness", detail: "probe failed", untrusted_content: true }], pods: [{ namespace: "opsi-prod", pod_id: "worker-7", node_id: "node-edge", ready_containers: 0, total_containers: 1, restart_count: 3 }], kubernetes_events: [], log_fingerprints: [{ fingerprint: "fp-timeout", level: "error", count: 3, first_observed_unix: 1785290200, last_observed_unix: 1785290800, excerpt: "request timeout", untrusted_content: true }], audit_references: [], coverage: [{ source: "rollout", status: "available", item_count: 1, truncated: false }, { source: "kubernetes", status: "partial", reason_code: "EVENT_LIMIT", item_count: 1, truncated: true }], truncations: [{ section: "kubernetes_events", omitted_items: 4, utf8_safe: true }], content_sha256: "content-hash-1" } };
}
