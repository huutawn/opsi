import { expect, test, type Page, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("P04 Design and Live keep separate authority, preserve CanvasDraft, fail future resources closed, and keep 40px controls", async ({ page }) => {
  const state = fixture();
  await mock(page, () => state);
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology&environment=env-prod&topology=service%3Adatabase");

  await expect(page.getByRole("button", { name: "Live", exact: true })).toBeVisible();
  await expectMinimumTarget(page.locator(".topologyMode button"));
  await expectMinimumTarget(page.locator(".designActions button"));
  await expect(page.getByRole("button", { name: /Unsupported resource database, kind managed-service/ })).toHaveAttribute("data-resource-state", "unsupported");

  await page.getByRole("button", { name: "Live", exact: true }).click();
  await expect(page.locator(".liveInspector")).toHaveAttribute("data-resource-state", "unsupported");
  await expect(page.getByRole("heading", { name: "database", exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Design", exact: true }).click();
  await page.getByRole("button", { name: /Application api, Assigned, unchanged/ }).click();
  await page.getByLabel("Replicas").fill("3");
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Live", exact: true }).click();
  const liveRuntime = page.locator('.topologyResourceNode[data-resource-mode="live"][data-resource-kind="server"]').filter({ hasText: "Primary runtime" });
  await expect(liveRuntime).toHaveAttribute("data-resource-state", "factual");
  await expect(page.locator('.topologyResourceNode[data-resource-mode="live"][data-draft-state]')).toHaveCount(0);
  await expectMinimumTarget(page.locator(".liveOverviewStatus button"));

  await page.getByRole("button", { name: "Design", exact: true }).click();
  await expect(page.getByLabel("Replicas")).toHaveValue("3");
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Live", exact: true }).click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("P04 Connect Server is command-first, keeps SSH advanced, and exposes accessible targets", async ({ page }) => {
  const state = fixture();
  await mock(page, () => state);
  await page.goto("/?project=proj-1&view=infrastructure&tab=bootstrap&environment=env-prod");
  await page.getByRole("button", { name: "Connect Server" }).click();
  const dialog = page.getByRole("dialog", { name: "Connect Server" });

  await expect(dialog.getByLabel("Run bootstrap command")).toBeChecked();
  await expect(dialog.getByLabel("SSH port")).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Generate bootstrap command" })).toBeVisible();
  await expect(dialog.getByText("Run on VPS", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Wait for Ready", { exact: true })).toBeVisible();
  await expectMinimumTarget(dialog.getByRole("button"));
  await expectMinimumTarget(dialog.locator("summary"));

  await dialog.getByText("Advanced: Bootstrap over SSH").click();
  await dialog.getByRole("radio", { name: /^SSH password/ }).check();
  await expect(dialog.getByLabel("SSH port")).toBeVisible();
  await expect(dialog.getByLabel("SSH username")).toBeVisible();
});

test("P04 lifecycle distinguishes Ready, Offline, and Unknown without optimistic inference", async ({ page }) => {
  const state = fixture();
  await mock(page, () => state);
  const url = "/?project=proj-1&view=infrastructure&tab=topology&topologyMode=live&environment=env-prod";

  await page.goto(url);
  await expect(page.locator(".serverLifecycle").getByText("Ready", { exact: true })).toBeVisible();
  await expect(page.locator(".serverLifecycle .statusIcon")).toBeVisible();

  state.facts.agents[0].status = "offline";
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Offline", { exact: true })).toBeVisible();
  const serverNode = page.locator('.topologyResourceNode[data-resource-mode="live"][data-resource-kind="server"]');
  await expect(serverNode).toContainText("Offline");

  state.facts.runtimes[0].status = "mystery";
  state.facts.nodes[0].status = "mystery";
  state.facts.agents[0].status = "mystery";
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Unknown", { exact: true })).toBeVisible();
  await expect(serverNode).toContainText("Facts are insufficient");
  await expect(page.getByRole("button", { name: /Server Primary runtime, Ready/ })).toHaveCount(0);
});

test("P04 bootstrap restores Waiting, Connecting, Bootstrapping, failure retry, and Ready facts", async ({ page }) => {
  const state = fixture();
  state.facts = emptyFacts();
  state.services = [];
  state.nodes = [];
  state.deployments = [];
  state.sessions = [session("waiting")];
  await mock(page, () => state);
  const url = "/?project=proj-1&view=infrastructure&tab=topology&topologyMode=live&environment=env-prod";

  await page.goto(url);
  await expect(page.locator(".serverLifecycle").getByText("Waiting", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText(/This browser only shows the command when it is issued/)).toBeVisible();
  await expect(page.getByText("btok-secret", { exact: false })).toHaveCount(0);

  state.sessions = [session("connecting")];
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Connecting", { exact: true })).toBeVisible();

  state.sessions = [{ ...session("installing_agent"), checkpoint: { plan_version: "v1", next_step_index: 3, last_completed_step: "installing_agent" } }];
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Bootstrapping", { exact: true })).toBeVisible();
  await expect(page.getByText("75% · installing_agent", { exact: true })).toBeVisible();

  state.sessions = [{ ...session("failed"), last_failure_code: "AGENT_INSTALL_FAILED", last_failure_message_redacted: "Agent registration failed" }];
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Failed", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Retry bootstrap" }).click();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Bootstrap boot-1 returned status pending/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();

  const ready = fixture();
  state.facts = ready.facts;
  state.services = ready.services;
  state.nodes = ready.nodes;
  state.sessions = [session("succeeded")];
  await page.reload();
  await expect(page.locator(".serverLifecycle").getByText("Ready", { exact: true })).toBeVisible();
});

test("P04 deployment refresh restores active, failed, exposure, and rollback eligibility facts", async ({ page }) => {
  const state = fixture();
  await mock(page, () => state);
  const url = "/?project=proj-1&view=infrastructure&tab=topology&topologyMode=live&environment=env-prod";

  await page.goto(url);
  await page.reload();
  const active = page.locator(".liveDeploymentList li").filter({ hasText: "dep-active" });
  await expect(active).toContainText("Applying");
  const failed = page.locator(".liveDeploymentList li").filter({ hasText: "dep-exposure-failed" });
  await expect(failed).toContainText("Degraded");
  await expect(failed).toContainText("Ingress reconciliation failed");
  const exposureFacts = page.getByLabel("Connections and exposure").getByText("apps.example.com/api", { exact: true });
  await expect(exposureFacts).toHaveCount(2);
  await expect(exposureFacts.first()).toBeVisible();

  const eligible = page.locator(".liveDeploymentList li").filter({ hasText: "dep-current" });
  const unavailable = page.locator(".liveDeploymentList li").filter({ hasText: "dep-old" });
  await expect(eligible.getByRole("button", { name: "Rollback" })).toBeEnabled();
  await expect(unavailable.getByRole("button", { name: "Rollback" })).toBeDisabled();
  await expect(unavailable).toContainText("no previous known-good deployment is available");
  await eligible.getByRole("button", { name: "Rollback" }).click();
  await expect(page.getByRole("dialog", { name: /rollback deployment/i })).toBeVisible();
  await page.getByRole("button", { name: "Cancel" }).click();

  Object.assign(state.deployments.find((deployment) => deployment.id === "dep-active"), { status: "succeeded", rollout_state: "succeeded" });
  await page.reload();
  await expect(page.locator(".liveDeploymentList li").filter({ hasText: "dep-active" })).toContainText("Running");
});

async function expectMinimumTarget(locator: ReturnType<Page["locator"]>) {
  const count = await locator.count();
  expect(count).toBeGreaterThan(0);
  for (let index = 0; index < count; index += 1) {
    const box = await locator.nth(index).boundingBox();
    expect(box?.height ?? 0).toBeGreaterThanOrEqual(40);
    expect(box?.width ?? 0).toBeGreaterThanOrEqual(40);
  }
}

async function mock(page: Page, current: () => State) {
  await page.route("**/api/local/**", async (route) => respond(route, current()));
}

async function respond(route: Route, state: State) {
  const path = new URL(route.request().url()).pathname;
  if (route.request().method() === "POST" && /\/bootstrap-sessions\/boot-1\/retry$/.test(path)) {
    state.sessions = [{ ...state.sessions[0], status: "pending" }];
    await json(route, state.sessions[0], 202);
    return;
  }
  let body: unknown = {};
  if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: "proj-1", capabilities: [] };
  else if (path === "/api/local/session/project") body = { status: "selected", project_id: "proj-1" };
  else if (path === "/api/local/projects") body = { projects: state.projects };
  else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
  else if (path.endsWith("/nodes")) body = { nodes: state.nodes };
  else if (path.endsWith("/services")) body = { services: state.services };
  else if (path.endsWith("/deployments")) body = { deployments: state.deployments };
  else if (/\/deployments\/[^/]+\/events$/.test(path)) body = { events: [{ id: `event-${path.split("/").at(-2)}`, step: "waiting_ready", message_redacted: "Factual rollout checkpoint", progress_percent: 50, created_at: "2026-08-10T08:02:00Z" }] };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: state.sessions };
  else if (/\/bootstrap-sessions\/[^/]+\/events$/.test(path)) body = state.bootstrapEvents;
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/support")) body = { generated_at: "2026-08-10T08:00:00Z", counts: {}, signals: [] };
  else if (path.endsWith("/topology/facts")) body = state.facts;
  else if (path.endsWith("/topology")) body = state.topology;
  else if (path.endsWith("/github/repositories")) body = { repositories: [] };
  else if (path.endsWith("/github/bindings")) body = { bindings: [] };
  else if (path.endsWith("/build-records")) body = { records: [] };
  else if (path.endsWith("/deployment-policies")) body = { policies: [] };
  else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: [] };
  else if (path.endsWith("/logs")) body = { source: "agent", payload_policy: "redacted", logs: [] };
  else if (path.includes("/telemetry/services/")) body = { project_id: "proj-1", source: "agent", payload_policy: "redacted", services: [] };
  await json(route, body);
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status });
}

function fixture() {
  const services = [
    { id: "api", name: "api", type: "application", status: "ready", source_type: "image", container_port: 8080, configuration: { schema_version: "opsi.service_configuration/v1", revision: 3, state_hash: "api-state", bindings: [{ kind: "internal_http", target_service_id: "worker", target_service_key: "worker", env_prefix: "WORKER" }], public_route: { hostname: "apps.example.com", path: "/api" } } },
    { id: "worker", name: "worker", type: "application", status: "ready", source_type: "image", container_port: 9000, configuration: { schema_version: "opsi.service_configuration/v1", revision: 2, state_hash: "worker-state", bindings: [] } },
    { id: "database", name: "database", type: "managed-service", status: "ready", source_type: "managed", container_port: 5432, configuration: { schema_version: "opsi.service_configuration/v1", revision: 1, state_hash: "database-state", bindings: [] } },
  ];
  const facts = { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-primary", project_id: "proj-1", environment_id: "env-prod", name: "Primary runtime", type: "k3s", status: "ready" }], nodes: [{ id: "node-primary", project_id: "proj-1", runtime_id: "runtime-primary", status: "healthy", cpu_cores: 8, memory_mb: 16384, last_seen_at: "2026-08-10T08:00:00Z" }], agents: [{ id: "agent-primary", project_id: "proj-1", runtime_id: "runtime-primary", node_id: "node-primary", status: "active", capabilities: { deploy: true }, last_seen_at: "2026-08-10T08:00:00Z" }], services: services.map((service) => ({ id: service.id, project_id: "proj-1", key: service.name })) };
  const digest = (character: string) => `sha256:${character.repeat(64)}`;
  const deployments = [
    { id: "dep-old", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "api", status: "succeeded", rollout_state: "succeeded", current_digest: digest("1"), rollback_eligible: false, rollback_blocked_reason: "no previous known-good deployment is available", created_at: "2026-08-10T07:00:00Z" },
    { id: "dep-current", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "api", status: "succeeded", rollout_state: "succeeded", current_digest: digest("2"), previous_digest: digest("1"), rollback_eligible: true, exposure_spec: { schema_version: "opsi.exposure_spec/v1", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_key: "api", deployment_job_id: "dep-current", hostname: "apps.example.com", path: "/api", service_port: 8080, tls: { mode: "disabled" }, spec_hash: "exposure-current" }, created_at: "2026-08-10T08:00:00Z" },
    { id: "dep-exposure-failed", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "api", status: "failed", rollout_state: "failed", base_deployment_id: "dep-current", action: "apply", desired_digest: digest("2"), rollback_eligible: false, rollback_blocked_reason: "route rollout did not succeed", failure_code: "EXPOSURE_APPLY_FAILED", failure_message_redacted: "Ingress reconciliation failed", exposure_spec: { schema_version: "opsi.exposure_spec/v1", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_key: "api", deployment_job_id: "dep-exposure-failed", hostname: "apps.example.com", path: "/api", service_port: 8080, tls: { mode: "disabled" }, spec_hash: "exposure-failed" }, created_at: "2026-08-10T08:03:00Z" },
    { id: "dep-active", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "worker", status: "applying", rollout_state: "applying", desired_digest: digest("3"), rollback_eligible: false, rollback_blocked_reason: "deployment is not terminal", created_at: "2026-08-10T08:04:00Z" },
    { id: "dep-managed", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "database", status: "succeeded", rollout_state: "succeeded", current_digest: digest("4"), rollback_eligible: false, created_at: "2026-08-10T08:05:00Z" },
  ];
  return {
    projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }],
    services,
    nodes: [{ id: "node-primary", name: "Primary node", role: "server", status: "healthy", public_host: "203.0.113.10", cpu_cores: 8, memory_mb: 16384, agent_id: "agent-primary", agent_version: "1.4.0", last_seen_at: "2026-08-10T08:00:00Z" }],
    facts,
    topology: { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 4, state_hash: "topology-state", plan_hash: "topology-plan", created_by: "owner", applied_by: "owner", created_at: "2026-08-10T07:00:00Z", applied_at: "2026-08-10T07:00:00Z", assignments: services.map((service) => ({ service_key: service.name, environment_id: "env-prod", runtime_id: "runtime-primary", replicas: 2, cpu_request_millicores: 250, memory_request_bytes: 268435456, exposure: { mode: service.id === "api" ? "public" : "none" } })) },
    deployments,
    sessions: [] as Array<ReturnType<typeof session>>,
    bootstrapEvents: [{ id: "bootstrap-event", step: "installing_agent", message_redacted: "Installing Agent", progress_percent: 75, created_at: "2026-08-10T08:01:00Z" }],
  };
}

function emptyFacts() {
  return { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [], nodes: [], agents: [], services: [] };
}

function session(status: string) {
  return { id: "boot-1", status, public_host: "203.0.113.10", role: "first_server", auth_method: "command", attempt_count: 1, max_attempts: 3, created_at: "2026-08-10T08:00:00Z" };
}

type State = ReturnType<typeof fixture>;
