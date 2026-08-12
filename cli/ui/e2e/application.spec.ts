import { expect, test, type Page, type Route } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

type Scenario = "available" | "claimed" | "conflict" | "disconnected" | "service-failure" | "binding-failure" | "build-failure";

test.beforeEach(async ({ page }) => { watchConsoleErrors(page); });
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Source uses installation inventory, repository identity, ref, and >=40px keyboard targets", async ({ page }) => {
  const state = applicationState("available");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openWizard(page, "services");
  await expect(page.getByLabel("GitHub installation")).toHaveValue("11");
  await expect(page.getByLabel("Repository")).toHaveValue("101");
  await expect(page.getByLabel("Branch or ref")).toHaveValue("main");
  await page.keyboard.press("Tab");
  await expect(page.locator(":focus")).toBeVisible();
  for (const box of await page.getByRole("dialog", { name: "Add application" }).locator("button, input, select, summary").evaluateAll((items) => items.filter((item) => (item as HTMLElement).offsetParent !== null).map((item) => item.getBoundingClientRect().height))) expect(box).toBeGreaterThanOrEqual(40);
});

test("Connect GitHub is factual when no installation is active and mobile does not overflow", async ({ page }) => {
  const state = applicationState("disconnected");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.setViewportSize({ width: 390, height: 844 });
  await openWizard(page, "services");
  await expect(page.getByRole("heading", { name: "Connect GitHub" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("unavailable repositories are disabled with factual reasons", async ({ page }) => {
  const state = applicationState("conflict");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openWizard(page, "services");
  for (const reason of ["example/conflict: claimed by another project", "example/archived: archived", "example/disabled: disabled"]) await expect(page.getByText(reason, { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
});

for (const entry of ["services", "topology"] as const) {
  test(`the same shared wizard opens from ${entry}`, async ({ page }) => {
    const state = applicationState("claimed");
    await page.route("**/api/local/**", (route) => respond(route, state));
    await openWizard(page, entry, state);
    const steps = page.getByRole("dialog", { name: "Add application" }).locator(".applicationWizardSteps");
    for (const label of ["Source", "Application", "Build"]) await expect(steps).toContainText(label);
  });
}

test("root repository defaults to Automatic, app root '.', context '.', and no required runtime tuning", async ({ page }) => {
  const state = applicationState("claimed");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openApplicationStep(page);
  await expect(page.getByLabel("Application root", { exact: true })).toHaveValue(".");
  await expect(page.getByLabel("Automatic Recommended")).toBeChecked();
  await page.getByText("Advanced build settings", { exact: true }).click();
  await expect(page.getByLabel("Build context")).toHaveValue(".");
  await expect(page.getByLabel("Container port")).not.toBeVisible();
  await expect(page.getByText("Opsi uses a Dockerfile when one is available; otherwise it builds source with Cloud Native Buildpacks.")).toBeVisible();
});

test("monorepo app root stays separate from build context and Automatic binding is canonical", async ({ page }) => {
  const state = applicationState("available");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await reviewApplication(page, { root: "apps/api", context: ".", strategy: "auto" });
  const review = page.getByRole("dialog", { name: "create application" });
  await expect(review.getByText("Create Application api: example/web@main, root apps/api, runtime Web service", { exact: true })).toBeVisible();
  await expect(review.getByText("Bind source: context ., build Automatic", { exact: true })).toBeVisible();
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
  await review.getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("heading", { name: "Application ready" })).toBeVisible();
  await expect(page.getByText("Unplaced", { exact: true }).last()).toBeVisible();
  expect(state.bindingBodies[0]).toEqual({ service_id: "svc-api", repository_id: 101, service_key: "api", config_path: ".opsi/opsi-cd.yaml", selected_ref: "main", application_root: "apps/api", build_context: ".", build_strategy: "auto" });
  expect(state.unexpectedWrites).toEqual([]);
});

for (const [strategy, expected] of [["dockerfile", { build_strategy: "dockerfile", dockerfile_path: "apps/api/Dockerfile" }], ["buildpack", { build_strategy: "buildpack" }]] as const) {
  test(`explicit ${strategy} sends only the canonical strategy fields`, async ({ page }) => {
    const state = applicationState("claimed");
    await page.route("**/api/local/**", (route) => respond(route, state));
    await reviewApplication(page, { root: "apps/api", context: ".", strategy });
    const review = page.getByRole("dialog", { name: "create application" });
    await review.getByRole("button", { name: "Confirm and submit" }).click();
    await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
    expect(state.bindingBodies[0]).toMatchObject(expected);
    if (strategy === "buildpack") expect(state.bindingBodies[0]).not.toHaveProperty("dockerfile_path");
  });
}

test("invalid source path remains visible and backend error is not normalized", async ({ page }) => {
  const state = applicationState("claimed");
  state.bindingError = { code: "BUILD_SOURCE_INVALID", message: "application_root must be a canonical repository path", nextAction: "Check Application root, Build context, and Dockerfile path without changing their intended scope." };
  await page.route("**/api/local/**", (route) => respond(route, state));
  await reviewApplication(page, { root: "../api", context: ".", strategy: "auto" });
  const review = page.getByRole("dialog", { name: "create application" });
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/github/bindings", status: 422, method: "POST" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByText("application_root must be a canonical repository path", { exact: true })).toBeVisible();
  expect(state.bindingBodies[0].application_root).toBe("../api");
});

test("missing Dockerfile is rejected before persistence with actionable guidance", async ({ page }) => {
  const state = applicationState("claimed");
  state.buildFailure = { code: "DOCKERFILE_NOT_FOUND", message: "The selected Dockerfile does not exist at the resolved commit." };
  await page.route("**/api/local/**", (route) => respond(route, state));
  await createAndOpenBuild(page, state, { root: "apps/api", context: ".", strategy: "dockerfile" });
  await page.getByRole("button", { name: "Build application" }).click();
  const review = page.getByRole("dialog", { name: "build BuildJob" });
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/applications/svc-api/build-jobs", status: 422, method: "POST" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByText("The selected Dockerfile does not exist at the resolved commit.", { exact: true })).toBeVisible();
  await expect(review.getByText("Choose an existing Dockerfile path or switch Build method to Automatic or Buildpacks.", { exact: true })).toBeVisible();
  expect(state.jobs).toEqual([]);
});

test("Buildpacks shared workspace failure shows the required monorepo action", async ({ page }) => {
  const state = applicationState("claimed");
  state.buildFailure = { code: "BUILDPACK_MONOREPO_UNSUPPORTED", message: "shared workspace unsupported" };
  await page.route("**/api/local/**", (route) => respond(route, state));
  await createAndOpenBuild(page, state, { root: "apps/api", context: ".", strategy: "buildpack" });
  await buildApplication(page);
  await expect(page.getByText("This application depends on files outside its application root. Use a Dockerfile build or choose a self-contained application directory.", { exact: true })).toBeVisible();
});

test("partial source binding recovery and retry reuse one Application", async ({ page }) => {
  const state = applicationState("binding-failure");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await reviewApplication(page, { root: "apps/api", context: ".", strategy: "auto" });
  const review = page.getByRole("dialog", { name: "create application" });
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/github/bindings", status: 503, method: "POST" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await review.getByRole("button", { name: "Retry same attempt" }).click();
  await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
  expect(state.services).toHaveLength(1);
  expect(state.serviceBodies).toHaveLength(2);
  expect(new Set(state.mutationKeys).size).toBe(1);
});

test("refresh resumes missing source binding without duplicate Application", async ({ page }) => {
  const state = applicationState("claimed");
  state.services.push(service());
  state.facts.services.push({ id: "svc-api", project_id: "proj-1", key: "api" });
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openApplicationStep(page);
  await page.getByLabel("Application name").fill("api");
  await expect(page.getByRole("status").getByText("Resume source binding", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Resume source binding" }).click();
  const review = page.getByRole("dialog", { name: "create application" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
  expect(state.serviceBodies).toHaveLength(0);
  expect(state.services).toHaveLength(1);
});

test("explicit Build creates one BuildJob, shows lifecycle/exact SHA, and never deploys", async ({ page }) => {
  const state = applicationState("claimed");
  await page.route("**/api/local/**", (route) => respond(route, state));
  await createAndOpenBuild(page, state, { root: ".", context: ".", strategy: "auto" });
  await expect(page.getByText("No build has been requested.")).toBeVisible();
  await buildApplication(page);
  await expect(page.getByText("ready", { exact: true })).toBeVisible();
  await expect(page.getByText(state.sha, { exact: true })).toBeVisible();
  expect(state.buildJobCreates).toBe(1);
  expect(state.unexpectedWrites.filter((path) => path.includes("deploy"))).toEqual([]);
});

test("succeeded BuildJob shows immutable accepted BuildRecord and exact commit", async ({ page }) => {
  const state = applicationState("claimed");
  state.buildLifecycle = "succeeded";
  await page.route("**/api/local/**", (route) => respond(route, state));
  await createAndOpenBuild(page, state, { root: "apps/api", context: ".", strategy: "auto" });
  await buildApplication(page);
  await expect(page.getByText("Accepted BuildRecord", { exact: true })).toBeVisible();
  await expect(page.getByText(state.sha, { exact: true })).toBeVisible();
  await expect(page.getByText(state.digest, { exact: true })).toBeVisible();
  await expect(page.getByText("Buildpacks", { exact: true })).toBeVisible();
  await expect(page.getByText("web", { exact: true })).toBeVisible();
});

test("existing accepted BuildRecord is reused without forcing rebuild", async ({ page }) => {
  const state = applicationState("claimed");
  state.services.push(service());
  state.bindings.push(binding());
  state.records.push(buildRecord(state));
  await page.route("**/api/local/**", (route) => respond(route, state));
  await openApplicationStep(page);
  await page.getByLabel("Application name").fill("api");
  await page.getByRole("button", { name: "Continue to build" }).click();
  await expect(page.getByText("Accepted BuildRecord", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Build application" })).toHaveCount(0);
  expect(state.buildJobCreates).toBe(0);
});

async function openWizard(page: Page, entry: "services" | "topology", state?: ReturnType<typeof applicationState>) {
  if (entry === "topology" && state) {
    state.facts.environments = [{ id: "env-1", project_id: "proj-1", name: "Production", type: "prod", status: "active" }];
    state.facts.runtimes = [{ id: "runtime-1", project_id: "proj-1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }];
    state.facts.nodes = [{ id: "node-1", project_id: "proj-1", runtime_id: "runtime-1", status: "ready" }];
    state.facts.agents = [{ id: "agent-1", project_id: "proj-1", runtime_id: "runtime-1", node_id: "node-1", status: "active", capabilities: {} }];
  }
  await page.goto(entry === "services" ? "/?project=proj-1&view=services" : "/?project=proj-1&view=infrastructure&tab=topology&topologyMode=design");
  if (entry === "services") await page.locator(".pageHeader").getByRole("button", { name: "Add application" }).click();
  else await page.getByRole("button", { name: "Add application" }).click();
  await expect(page.getByRole("dialog", { name: "Add application" })).toBeVisible();
}

async function openApplicationStep(page: Page) { await openWizard(page, "services"); await page.getByRole("button", { name: "Continue" }).click(); }

async function reviewApplication(page: Page, options: { root: string; context: string; strategy: "auto" | "dockerfile" | "buildpack" }) {
  await openApplicationStep(page);
  await page.getByLabel("Application name").fill("api");
  await page.getByLabel("Application root", { exact: true }).fill(options.root);
  if (options.strategy === "dockerfile") { await page.getByRole("radio", { name: /^Dockerfile / }).check(); await page.getByRole("textbox", { name: "Dockerfile path" }).fill("apps/api/Dockerfile"); }
  if (options.strategy === "buildpack") await page.getByRole("radio", { name: /^Buildpacks / }).check();
  await page.getByText("Advanced build settings", { exact: true }).click();
  await page.getByLabel("Build context").fill(options.context);
  await page.getByRole("button", { name: "Review application" }).click();
}

async function createAndOpenBuild(page: Page, state: ReturnType<typeof applicationState>, options: { root: string; context: string; strategy: "auto" | "dockerfile" | "buildpack" }) {
  await reviewApplication(page, options);
  const review = page.getByRole("dialog", { name: "create application" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
  await review.getByRole("button", { name: "Close" }).click();
  await expect(page.getByRole("heading", { name: "Application ready" })).toBeVisible();
  expect(state.services).toHaveLength(1);
}

async function buildApplication(page: Page) {
  await page.getByRole("button", { name: /Build application|Build again/ }).click();
  const review = page.getByRole("dialog", { name: "build BuildJob" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(review.getByRole("button", { name: "Close" })).toBeVisible();
  await review.getByRole("button", { name: "Close" }).click();
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
    scenario, installations: scenario === "disconnected" ? [] : [installation], repositories,
    services: [] as Array<Record<string, unknown>>, bindings: [] as Array<Record<string, unknown>>, jobs: [] as Array<Record<string, unknown>>, records: [] as Array<Record<string, unknown>>,
    facts: { project_id: "proj-1", environments: [], runtimes: [], nodes: [], agents: [], services: [] as Array<Record<string, unknown>> },
    topology: { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: "state-1", plan_hash: "plan-1", created_by: "owner", applied_by: "owner", created_at: "2026-08-06T00:00:00Z", applied_at: "2026-08-06T00:00:00Z", assignments: [] },
    sha: "0123456789abcdef0123456789abcdef01234567", digest: `sha256:${"a".repeat(64)}`,
    claimCount: scenario === "claimed" ? 1 : 0, failedOnce: false, buildJobCreates: 0, buildLifecycle: "ready", buildFailure: null as null | { code: string; message: string }, bindingError: null as null | { code: string; message: string; nextAction: string },
    mutationKeys: [] as string[], serviceBodies: [] as Array<Record<string, unknown>>, bindingBodies: [] as Array<Record<string, unknown>>, unexpectedWrites: [] as string[],
  };
}

async function respond(route: Route, state: ReturnType<typeof applicationState>) {
  const request = route.request(); const path = new URL(request.url()).pathname; const method = request.method(); const key = request.headers()["idempotency-key"] ?? "";
  if (method !== "GET") {
    if (path.endsWith("/github/repositories/101/claim")) { state.mutationKeys.push(key); state.claimCount = 1; state.repositories[0].claim_status = "active"; return fulfill(route, { repository_id: 101, project_id: "proj-1", status: "active" }); }
    if (path.endsWith("/services")) {
      state.mutationKeys.push(key); const body = request.postDataJSON() as Record<string, unknown>; state.serviceBodies.push(body);
      if (state.scenario === "service-failure" && !state.failedOnce) { state.failedOnce = true; return fail(route, "SERVICE_UNAVAILABLE", "service creation failed"); }
      if (!state.services.length) { const created = { ...body, id: "svc-api", status: "draft" }; state.services.push(created); state.facts.services.push({ id: "svc-api", project_id: "proj-1", key: "api" }); }
      return fulfill(route, state.services[0], 201);
    }
    if (path.endsWith("/github/bindings")) {
      state.mutationKeys.push(key); const body = request.postDataJSON() as Record<string, unknown>; state.bindingBodies.push(body);
      if (state.bindingError) return fulfill(route, { error: { code: state.bindingError.code, message: state.bindingError.message, next_action: state.bindingError.nextAction } }, 422);
      if (state.scenario === "binding-failure" && !state.failedOnce) { state.failedOnce = true; return fail(route, "GITHUB_BINDING_UNAVAILABLE", "binding creation failed"); }
      if (!state.bindings.length) state.bindings.push({ ...body, id: "binding-api", project_id: "proj-1", installation_id: 11, status: "active" });
      return fulfill(route, state.bindings[0], 201);
    }
    if (path.endsWith("/build-jobs")) {
      state.mutationKeys.push(key); state.buildJobCreates += 1;
      if (state.buildFailure?.code === "DOCKERFILE_NOT_FOUND") return fulfill(route, { error: state.buildFailure }, 422);
      const job = buildJob(state, state.buildFailure ? "failed" : state.buildLifecycle);
      state.jobs = [job];
      if (state.buildLifecycle === "succeeded" && !state.buildFailure) state.records = [buildRecord(state)];
      return fulfill(route, job, 201);
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
  if (path.endsWith("/build-jobs")) return fulfill(route, { build_jobs: state.jobs });
  if (path.includes("/build-records")) return fulfill(route, { records: state.records });
  if (path.endsWith("/deployment-policies")) return fulfill(route, { policies: [] });
  if (path.endsWith("/incidents")) return fulfill(route, { incidents: [] });
  return fulfill(route, {});
}

function service() { return { id: "svc-api", name: "api", type: "application", status: "draft", source_type: "git", repo_url: "https://github.com/example/web", branch: "main", configuration: { schema_version: "opsi.service_configuration/v1", revision: 0, state_hash: "empty", bindings: [] } }; }
function binding() { return { id: "binding-api", project_id: "proj-1", service_id: "svc-api", repository_id: 101, installation_id: 11, service_key: "api", config_path: ".opsi/opsi-cd.yaml", selected_ref: "main", application_root: ".", build_context: ".", build_strategy: "auto", status: "active" }; }
function buildJob(state: ReturnType<typeof applicationState>, status: string) { return { id: "job-api", project_id: "proj-1", environment_id: "env-1", application_id: "svc-api", source: { binding_id: "binding-api", binding_updated_at: "2026-08-12T00:00:00Z", github_installation_id: 11, repository_id: 101, repository_owner_id: 12, repository_full_name: "example/web", selected_ref: "main", resolved_commit_sha: state.sha, application_root: "apps/api", build_context: "." }, requested_build_strategy: "auto", resolved_build_strategy: state.buildFailure?.code === "DOCKERFILE_NOT_FOUND" ? "" : "buildpack", status, failure_code: state.buildFailure?.code, failure_message_redacted: state.buildFailure?.message, build_record_id: status === "succeeded" ? "br-api" : undefined, created_by: "owner", created_at: "2026-08-12T00:00:00Z", updated_at: "2026-08-12T00:01:00Z" }; }
function buildRecord(state: ReturnType<typeof applicationState>) { return { schema_version: "opsi.build_record/v1", id: "br-api", project_id: "proj-1", repository_id: 101, repository_owner_id: 12, active_binding_id: "binding-api", service_id: "svc-api", service_key: "api", created_at: "2026-08-12T00:01:00Z", workload: { issuer: "https://token.actions.githubusercontent.com", subject: "repo:example/web:ref:refs/heads/main", repository_id: 101, repository_owner_id: 12, ref: "refs/heads/main", sha: state.sha, event_name: "workflow_dispatch", workflow: "build", workflow_ref: "example/web/.github/workflows/build.yml@refs/heads/main", run_id: 1, run_attempt: 1 }, build: { config_hash: "config", platform: "linux/amd64", oci_repository: "ghcr.io/example/web/api", oci_digest: state.digest, build_job_id: "job-api", build_strategy: "buildpack", builder_identity: "paketobuildpacks/builder-jammy-base", builder_version: "0.4.0", builder: { pack_version: "0.38.2", processes: [{ type: "web", default: true }] }, status: "succeeded" } }; }
function fulfill(route: Route, body: unknown, status = 200) { return route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status }); }
function fail(route: Route, code: string, message: string) { return fulfill(route, { error: { code, message, next_action: "Retry the same reviewed attempt." } }, 503); }
