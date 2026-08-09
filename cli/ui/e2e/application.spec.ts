import { expect, test, type Page, type Route } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

type Scenario = "available" | "claimed" | "conflict" | "disconnected" | "service-failure" | "binding-failure";

test.beforeEach(async ({ page }) => { watchConsoleErrors(page); });
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("application wizard offers Connect GitHub when no installation is connected", async ({ page }) => {
  const state = applicationState("disconnected");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.setViewportSize({ width: 390, height: 844 });
  await openWizard(page);
  await expect(page.getByRole("heading", { name: "Connect GitHub" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Connect GitHub" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("application wizard blocks unavailable repositories and explains conflicts", async ({ page }) => {
  const state = applicationState("conflict");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openWizard(page);
  await expect(page.getByText("example/conflict: claimed by another project", { exact: true })).toBeVisible();
  await expect(page.getByText("example/archived: archived", { exact: true })).toBeVisible();
  await expect(page.getByText("example/disabled: disabled", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
  for (const option of await page.getByLabel("Repository").locator("option").all()) {
    if (await option.getAttribute("value")) expect(await option.evaluate((element) => (element as HTMLOptionElement).disabled)).toBe(true);
  }
});

test("available repository is claimed and creates one factual unplaced GitHub-bound application", async ({ page }) => {
  const state = applicationState("available");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await reviewApplication(page);
  const review = page.getByRole("dialog", { name: "create application" });
  await expect(review.getByText("Claim repository example/web for this project", { exact: true })).toBeVisible();
  await expect(review.getByText(/Create application identity web: git https:\/\/github.com\/example\/web#main, context apps\/web, Dockerfile deploy\/Dockerfile, port 8080, health \/healthz/)).toBeVisible();
  await expect(review.getByText("Create GitHub service binding: repository 101, service web, config .opsi/opsi-cd.yaml", { exact: true })).toBeVisible();
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review).toHaveCount(0);
  await expect(page).toHaveURL(/view=infrastructure.*tab=topology.*topology=service%3Aweb/);
  await expect(page.getByRole("button", { name: /Application web, Unplaced, unchanged/ })).toBeVisible();
  const inspector = page.locator(".canvasInspector");
  await expect(inspector.getByText("GitHub bound", { exact: true })).toBeVisible();
  await expect(inspector.getByText("example/web", { exact: true })).toBeVisible();
  await expect(inspector.getByText("main", { exact: true })).toBeVisible();
  await expect(inspector.getByText("apps/web", { exact: true })).toBeVisible();
  await expect(inspector.getByText("deploy/Dockerfile", { exact: true })).toBeVisible();
  await expect(inspector.getByText("No accepted build yet", { exact: true })).toBeVisible();
  expect(state.repositoryClaimCount).toBe(1);
  expect(state.services).toHaveLength(1);
  expect(state.bindings).toHaveLength(1);
  expect(state.serviceBodies[0]).toMatchObject({ name: "web", type: "application", source_type: "git", repo_url: "https://github.com/example/web", branch: "main", build_context: "apps/web", dockerfile: "deploy/Dockerfile", container_port: 8080, health_path: "/healthz" });
  expect(state.bindingBodies[0]).toEqual({ service_id: "svc-web", repository_id: 101, service_key: "web", config_path: ".opsi/opsi-cd.yaml" });
  expect(new Set(state.mutationKeys).size).toBe(1);
  expect(state.unexpectedWrites).toEqual([]);
});

test("repository already claimed by the project is not claimed again", async ({ page }) => {
  const state = applicationState("claimed");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await reviewApplication(page);
  const review = page.getByRole("dialog", { name: "create application" });
  await expect(review.getByText(/Claim repository/)).toHaveCount(0);
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review).toHaveCount(0);
  expect(state.claimRequests).toBe(0);
  expect(state.services).toHaveLength(1);
  expect(state.bindings).toHaveLength(1);
});

test("refresh resumes a missing source binding without creating a duplicate service", async ({ page }) => {
	const state = applicationState("claimed");
	state.services.push({ id: "svc-web", name: "web", type: "application", status: "draft", source_type: "git", repo_url: "https://github.com/example/web", branch: "main", container_port: 8080, configuration: { schema_version: "opsi.service_configuration/v1", revision: 0, state_hash: "empty", bindings: [] } });
	state.facts.services.push({ id: "svc-web", project_id: "proj-1", key: "web" });
	await page.route("**/api/local/**", (route) => respond(route, state));
	await page.goto("/?project=proj-1&view=services");
	await page.reload();
	await page.locator(".pageHeader").getByRole("button", { name: "Add application" }).click();
	await page.getByRole("button", { name: "Continue" }).click();
	await page.getByLabel("Service key").fill("web");
	await expect(page.getByText("Resume source binding", { exact: true }).first()).toBeVisible();
	await page.getByLabel("Container port").fill("8080");
	await page.getByRole("button", { name: "Resume source binding" }).click();
	const review = page.getByRole("dialog", { name: "create application" });
	await expect(review.getByText(/Create application identity/)).toHaveCount(0);
	await review.getByRole("button", { name: "Confirm and submit" }).click();
	await expect(review).toHaveCount(0);
	expect(state.serviceBodies).toHaveLength(0);
	expect(state.services).toHaveLength(1);
	expect(state.bindings).toHaveLength(1);
});

for (const [scenario, failedPath] of [["service-failure", "/api/local/projects/proj-1/services"], ["binding-failure", "/api/local/projects/proj-1/github/bindings"]] as const) {
  test(`${scenario} retries the same reviewed operation without duplicate durable mutations`, async ({ page }) => {
    const state = applicationState(scenario);
    await page.route("**/api/local/**", (route) => respond(route, state));
    await reviewApplication(page);
    const review = page.getByRole("dialog", { name: "create application" });
    expectHTTPFailure(page, { path: failedPath, status: 503, method: "POST" });
    await review.getByRole("button", { name: "Confirm and submit" }).click();
    await expect(review.getByRole("button", { name: "Retry same attempt" })).toBeVisible();
    await review.getByRole("button", { name: "Retry same attempt" }).click();
    await expect(review).toHaveCount(0);
    expect(state.repositoryClaimCount).toBe(1);
    expect(state.services).toHaveLength(1);
    expect(state.bindings).toHaveLength(1);
    expect(new Set(state.mutationKeys).size).toBe(1);
  });
}

async function openWizard(page: Page) {
  await page.goto("/?project=proj-1&view=services");
  await page.locator(".pageHeader").getByRole("button", { name: "Add application" }).click();
  await expect(page.getByRole("dialog", { name: "Add application" })).toBeVisible();
}

async function reviewApplication(page: Page) {
  await openWizard(page);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Service key").fill("web");
  await page.getByLabel("Project path / build context").fill("apps/web");
  await page.getByLabel("Dockerfile path").fill("deploy/Dockerfile");
  await page.getByLabel("Container port").fill("8080");
  await page.getByLabel("Health path").fill("/healthz");
  await page.getByRole("button", { name: "Review application" }).click();
  await expect(page.getByRole("dialog", { name: "Add application" })).toHaveCount(0);
}

function applicationState(scenario: Scenario) {
  const installation = { installation_id: 11, account_login: "example", status: "active", suspended: false };
  const available = { repository_id: 101, installation_id: 11, full_name: "example/web", default_branch: "main", status: "active", claim_status: scenario === "claimed" ? "active" : "available" };
  const repositories = scenario === "conflict" ? [
    { ...available, repository_id: 102, full_name: "example/conflict", claim_status: "conflict", claimed_project_id: "other-project" },
    { ...available, repository_id: 103, full_name: "example/archived", archived: true },
    { ...available, repository_id: 104, full_name: "example/disabled", disabled: true },
  ] : scenario === "disconnected" ? [] : [available];
  return {
    scenario,
    installations: scenario === "disconnected" ? [] : [installation], repositories,
    services: [] as Array<Record<string, unknown>>, bindings: [] as Array<Record<string, unknown>>,
    facts: { project_id: "proj-1", environments: [], runtimes: [], nodes: [], agents: [], services: [] as Array<Record<string, unknown>> },
    topology: { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: "state-1", plan_hash: "plan-1", created_by: "owner", applied_by: "owner", created_at: "2026-08-06T00:00:00Z", applied_at: "2026-08-06T00:00:00Z", assignments: [] },
    claimRequests: 0, repositoryClaimCount: scenario === "claimed" ? 1 : 0, failedOnce: false,
    mutationKeys: [] as string[], serviceBodies: [] as Array<Record<string, unknown>>, bindingBodies: [] as Array<Record<string, unknown>>, unexpectedWrites: [] as string[],
  };
}

async function respond(route: Route, state: ReturnType<typeof applicationState>) {
  const request = route.request();
  const path = new URL(request.url()).pathname;
  const method = request.method();
  const key = request.headers()["idempotency-key"] ?? "";
  if (method !== "GET") {
    if (path.endsWith("/github/repositories/101/claim")) {
      state.claimRequests += 1; state.mutationKeys.push(key);
      if (!state.repositoryClaimCount) state.repositoryClaimCount = 1;
      state.repositories[0].claim_status = "active";
      return fulfill(route, { repository_id: 101, project_id: "proj-1", status: "active" });
    }
    if (path.endsWith("/services")) {
      state.mutationKeys.push(key);
      const body = request.postDataJSON() as Record<string, unknown>;
      state.serviceBodies.push(body);
      if (state.scenario === "service-failure" && !state.failedOnce) { state.failedOnce = true; return fail(route, "SERVICE_UNAVAILABLE", "service creation failed"); }
      if (!state.services.length) {
        const service = { ...body, id: "svc-web", status: "ready" };
        state.services.push(service);
        state.facts.services.push({ id: "svc-web", project_id: "proj-1", key: "web" });
      }
      return fulfill(route, state.services[0], 201);
    }
    if (path.endsWith("/github/bindings")) {
      state.mutationKeys.push(key);
      const body = request.postDataJSON() as Record<string, unknown>;
      state.bindingBodies.push(body);
      if (state.scenario === "binding-failure" && !state.failedOnce) { state.failedOnce = true; return fail(route, "GITHUB_BINDING_UNAVAILABLE", "binding creation failed"); }
      if (!state.bindings.length) state.bindings.push({ ...body, id: "binding-web", project_id: "proj-1", installation_id: 11, status: "active" });
      return fulfill(route, state.bindings[0], 201);
    }
    state.unexpectedWrites.push(`${method} ${path}`);
  }
  if (path === "/api/local/session") return fulfill(route, { authenticated: true, cloud_connected: "ok", agent_connected: "unavailable", org_id: "org-1", project_id: "proj-1" });
  if (path === "/api/local/projects") return fulfill(route, { projects: [{ id: "proj-1", org_id: "org-1", name: "Example", slug: "example", status: "ready" }] });
  if (path.endsWith("/readiness")) return fulfill(route, { project_id: "proj-1", status: "ready", can_deploy: false });
  if (path.endsWith("/nodes")) return fulfill(route, { nodes: [] });
  if (path.endsWith("/services")) return fulfill(route, { services: state.services });
  if (path.endsWith("/deployments")) return fulfill(route, { deployments: [] });
  if (path.endsWith("/bootstrap-sessions")) return fulfill(route, { sessions: [] });
  if (path.endsWith("/audit")) return fulfill(route, { events: [] });
  if (path.endsWith("/support")) return fulfill(route, {});
  if (path.endsWith("/topology/facts")) return fulfill(route, state.facts);
  if (path.endsWith("/topology")) return fulfill(route, state.topology);
  if (path.endsWith("/github/installations")) return fulfill(route, { installations: state.installations });
  if (path.endsWith("/github/repositories")) return fulfill(route, { repositories: state.repositories });
  if (path.endsWith("/github/bindings")) return fulfill(route, { bindings: state.bindings });
  if (path.includes("/build-records")) return fulfill(route, { records: [] });
  if (path.endsWith("/deployment-policies")) return fulfill(route, { policies: [] });
  if (path.endsWith("/incidents")) return fulfill(route, { incidents: [] });
  return fulfill(route, {});
}

function fulfill(route: Route, body: unknown, status = 200) { return route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status }); }
function fail(route: Route, code: string, message: string) { return fulfill(route, { error: { code, message, next_action: "Retry the same reviewed attempt." } }, 503); }
