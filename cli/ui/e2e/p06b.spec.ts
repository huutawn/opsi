import { expect, test, type Page, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => { watchConsoleErrors(page); });
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("catalog renders factual Applications, independent states, local filters, and shared actions", async ({ page }) => {
  const state = fixtures();
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.goto("/?project=proj-1&view=services");

  const api = card(page, "api");
  await expect(api).toContainText("Primary · node-1");
  await expect(api).toContainText("example/web");
  await expect(api).toContainText("main");
  await expect(api).toContainText(state.sha);
  await expect(api).toContainText(state.digest);
  await expect(api).toContainText("Succeeded");
  await expect(api).toContainText("public.example.test/");

  const worker = card(page, "worker");
  await expect(worker).toContainText("Unplaced");
  await expect(worker).toContainText("Succeeded");
  await expect(worker).toContainText("Not deployed");
  await expect(card(page, "fresh")).toContainText("Not built yet");

  await page.getByLabel("Placement").selectOption("unplaced");
  await expect(api).toHaveCount(0);
  await expect(worker).toBeVisible();
  await page.getByLabel("Build state").selectOption("not_built");
  await expect(card(page, "fresh")).toBeVisible();
  await expect(worker).toHaveCount(0);
  await page.getByLabel("Search Applications").fill("fresh");
  await expect(card(page, "fresh")).toBeVisible();
  await expect(page.getByRole("button", { name: "Add Application" })).toBeVisible();
  await expect(card(page, "fresh").getByRole("button", { name: "Open in Topology", exact: true })).toBeVisible();
  await expect(card(page, "fresh").getByRole("button", { name: "Deploy", exact: true })).toHaveCount(0);
});

test("deep-linked Application detail separates selected ref, BuildJobs, BuildRecord, and deployment", async ({ page }) => {
  const state = fixtures();
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.goto("/?project=proj-1&view=services&service=svc-api");

  const dialog = page.getByRole("dialog", { name: "api" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("tab", { name: "Source" })).toBeVisible();
  await expect(dialog.getByRole("tabpanel").getByText("Build", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("tabpanel").getByText("Topology", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("tabpanel").getByText("Runtime", { exact: true })).toBeVisible();

  await dialog.getByRole("tab", { name: "Source" }).click();
  await expect(dialog.getByText("main", { exact: true }).first()).toBeVisible();
  await expect(dialog.getByText(state.sha, { exact: true })).toBeVisible();
  await expect(dialog.getByText("apps/api", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Cloud Native Buildpacks", { exact: true })).toBeVisible();

  await dialog.getByRole("tab", { name: "Builds" }).click();
  const jobs = dialog.locator(".buildFacts:not(.acceptedBuild) .buildStateHeading code");
  await expect(jobs.nth(0)).toHaveText("job-new");
  await expect(jobs.nth(1)).toHaveText("job-old");
  await expect(dialog.getByText("Accepted BuildRecord", { exact: true })).toBeVisible();
  await expect(dialog.getByText(state.digest, { exact: true })).toBeVisible();
  await expect(dialog.getByText("ghcr.io/example/web/api", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Copy digest" })).toBeVisible();
  await expect(dialog.getByText(/Buildpacks · Buildpacks build failed/)).toBeVisible();
  await expect(dialog.getByText("Application compile failed.", { exact: true })).toBeVisible();

  await dialog.getByRole("tab", { name: "Runtime / Deployment" }).click();
  await expect(dialog.getByText("deploy-api", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Primary · k3s", { exact: true })).toBeVisible();
  await expect(dialog.getByRole("tabpanel").getByRole("button", { name: "View deployment" })).toBeVisible();
});

test("Dockerfile BuildRecord presents Dockerfile as the method and BuildKit only as executor metadata", async ({ page }) => {
  const state = fixtures();
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.goto("/?project=proj-1&view=services&service=svc-worker");
  const dialog = page.getByRole("dialog", { name: "worker" });
  await dialog.getByRole("tab", { name: "Builds" }).click();
  await expect(dialog.getByText("Dockerfile", { exact: true }).first()).toBeVisible();
  await dialog.getByText("Technical executor", { exact: true }).click();
  await expect(dialog.getByText(/Executor: moby\/buildkit/)).toBeVisible();
});

test("incomplete source binding resumes the exact ApplicationWizard without creating another Application", async ({ page }) => {
  const state = fixtures();
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.goto("/?project=proj-1&view=services&service=svc-broken");
  const detail = page.getByRole("dialog", { name: "broken" });
  await detail.getByRole("tab", { name: "Builds" }).click();
  await expect(detail.getByText("Source binding incomplete", { exact: true })).toBeVisible();
  await expect(detail.getByRole("button", { name: "Build", exact: true })).toHaveCount(0);
  await detail.getByRole("button", { name: "Resume source binding" }).click();
  await page.getByRole("dialog", { name: "Add application" }).getByRole("button", { name: "Close application wizard" }).click();
  await card(page, "broken").getByRole("button", { name: "Open", exact: true }).click();
  const reopened = page.getByRole("dialog", { name: "broken" });
  await reopened.getByRole("tab", { name: "Source" }).click();
  await expect(reopened.getByText("Source binding incomplete", { exact: true })).toBeVisible();
  await reopened.getByRole("button", { name: "Resume source binding" }).click();
  const wizard = page.getByRole("dialog", { name: "Add application" });
  await expect(wizard).toBeVisible();
  await expect(wizard.getByLabel("Application name")).toHaveValue("broken");
  await expect(wizard.getByRole("status").getByText("Resume source binding", { exact: true })).toBeVisible();
  expect(state.serviceCreates).toBe(0);
});

test("refresh recovers an active BuildJob and accepted BuildRecord", async ({ page }) => {
  const state = fixtures();
  state.jobs["svc-fresh"] = [job(state, "job-active", "running", "svc-fresh", "fresh", "2026-08-12T03:00:00Z")];
  state.recoverFresh = true;
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.goto("/?project=proj-1&view=services&service=svc-fresh");
  const dialog = page.getByRole("dialog", { name: "fresh" });
  await dialog.getByRole("tab", { name: "Builds" }).click();
  await expect(dialog.getByText("running", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Accepted BuildRecord", { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(dialog.getByText(state.digest, { exact: true })).toBeVisible();
  expect(state.buildCreates).toBe(0);
});

test("empty catalog and mobile controls remain factual, keyboard reachable, and at least 40px", async ({ page }) => {
  const state = fixtures();
  state.services = [];
  state.bindings = [];
  state.records = [];
  state.jobs = {};
  await page.route("**/api/local/**", (route) => respond(route, state));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?project=proj-1&view=services");
  await expect(page.getByText("No Applications yet", { exact: true })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(page.locator(":focus")).toBeVisible();
  for (const height of await page.locator("button, input, select").evaluateAll((items) => items.filter((item) => (item as HTMLElement).offsetParent !== null).map((item) => item.getBoundingClientRect().height))) expect(height).toBeGreaterThanOrEqual(40);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

function card(page: Page, name: string) { return page.locator(".applicationCard").filter({ has: page.getByRole("heading", { name, exact: true }) }); }

function fixtures() {
  const sha = "a94e76c0123456789abcdef0123456789abcdef0";
  const digest = `sha256:${"d".repeat(64)}`;
  const services = [service("api", { container_port: 8080, health_path: "/health" }), service("worker"), service("fresh"), service("broken", { repo_url: "https://github.com/example/web", branch: "main" })];
  const bindings = [binding("api", 101, "apps/api", "buildpack"), binding("worker", 102, ".", "dockerfile"), binding("fresh", 103, ".", "auto")];
  const state = {
    sha, digest, services, bindings,
    installations: [{ installation_id: 11, account_login: "example", status: "active", suspended: false }],
    repositories: [repository(101, "example/web"), repository(102, "example/worker"), repository(103, "example/fresh")],
    facts: { project_id: "proj-1", environments: [{ id: "env-1", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: "proj-1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }], nodes: [{ id: "node-1", project_id: "proj-1", runtime_id: "runtime-1", status: "ready" }], agents: [], services: services.map((item) => ({ id: item.id, project_id: "proj-1", key: item.name })) },
    topology: { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 2, state_hash: "state", plan_hash: "plan", created_by: "owner", applied_by: "owner", created_at: "2026-08-12T00:00:00Z", applied_at: "2026-08-12T00:00:00Z", assignments: [{ service_key: "api", environment_id: "env-1", runtime_id: "runtime-1", replicas: 2, cpu_request_millicores: 500, memory_request_bytes: 536870912, exposure: { mode: "public" } }] },
    records: [] as Array<Record<string, unknown>>,
    jobs: {} as Record<string, Array<Record<string, unknown>>>,
    deployments: [] as Array<Record<string, unknown>>,
    exposures: [] as Array<Record<string, unknown>>,
    recoverFresh: false, freshReads: 0, serviceCreates: 0, buildCreates: 0, unexpectedWrites: [] as string[],
  };
  state.jobs["svc-api"] = [job(state, "job-new", "succeeded", "svc-api", "api", "2026-08-12T02:00:00Z"), job(state, "job-old", "failed", "svc-api", "api", "2026-08-12T01:00:00Z")];
  state.records = [record(state, "record-api", "svc-api", "api", "job-new", "buildpack"), record(state, "record-worker", "svc-worker", "worker", "job-worker", "dockerfile")];
  state.jobs["svc-worker"] = [job(state, "job-worker", "succeeded", "svc-worker", "worker", "2026-08-12T01:30:00Z", "dockerfile")];
  state.jobs["svc-fresh"] = [];
  state.jobs["svc-broken"] = [];
  const deployment = { id: "deploy-api", project_id: "proj-1", environment_id: "env-1", runtime_id: "runtime-1", service_id: "svc-api", status: "succeeded", rollout_state: "succeeded", current_digest: digest, created_at: "2026-08-12T03:00:00Z", exposure_spec: { hostname: "public.example.test", path: "/" } };
  state.deployments = [deployment];
  state.exposures = [deployment];
  return state;
}

async function respond(route: Route, state: ReturnType<typeof fixtures>) {
  const request = route.request(); const path = new URL(request.url()).pathname; const method = request.method();
  if (method !== "GET") {
    if (path.endsWith("/build-jobs")) { state.buildCreates++; return fulfill(route, job(state, `job-${state.buildCreates}`, "ready", path.includes("svc-fresh") ? "svc-fresh" : "svc-api", "fresh", new Date().toISOString()), 201); }
    if (path.endsWith("/services")) state.serviceCreates++;
    state.unexpectedWrites.push(`${method} ${path}`);
  }
  if (path === "/api/local/session") return fulfill(route, { authenticated: true, cloud_connected: "ok", agent_connected: "unavailable", org_id: "org-1", project_id: "proj-1" });
  if (path === "/api/local/projects") return fulfill(route, { projects: [{ id: "proj-1", org_id: "org-1", name: "Example", slug: "example", status: "ready" }] });
  if (path.endsWith("/readiness")) return fulfill(route, { project_id: "proj-1", status: "ready", can_deploy: true });
  if (path.endsWith("/nodes")) return fulfill(route, { nodes: [] });
  if (path.endsWith("/services")) return fulfill(route, { services: state.services });
  if (path.endsWith("/deployments")) return fulfill(route, { deployments: state.deployments });
  if (path.endsWith("/exposures")) return fulfill(route, { exposures: state.exposures });
  if (path.endsWith("/bootstrap-sessions")) return fulfill(route, { sessions: [] });
  if (path.endsWith("/audit")) return fulfill(route, { events: [] });
  if (path.endsWith("/support")) return fulfill(route, {});
  if (path.endsWith("/topology/facts")) return fulfill(route, state.facts);
  if (path.endsWith("/topology")) return fulfill(route, state.topology);
  if (path.endsWith("/github/installations")) return fulfill(route, { installations: state.installations });
  if (path.endsWith("/github/repositories")) return fulfill(route, { repositories: state.repositories });
  if (path.endsWith("/github/bindings")) return fulfill(route, { bindings: state.bindings });
  if (path.endsWith("/deployment-policies")) return fulfill(route, { policies: [] });
  if (path.includes("/build-records")) return fulfill(route, { records: state.records });
  if (path.endsWith("/build-jobs")) {
    const serviceID = path.split("/applications/")[1].split("/")[0];
    if (serviceID === "svc-fresh" && state.recoverFresh && ++state.freshReads >= 2) {
      state.jobs[serviceID] = [job(state, "job-active", "succeeded", "svc-fresh", "fresh", "2026-08-12T03:00:00Z")];
      if (!state.records.some((item) => item.service_id === "svc-fresh")) state.records.push(record(state, "record-fresh", "svc-fresh", "fresh", "job-active", "buildpack"));
    }
    return fulfill(route, { build_jobs: state.jobs[serviceID] ?? [] });
  }
  if (path.endsWith("/incidents")) return fulfill(route, { incidents: [] });
  return fulfill(route, {});
}

function service(name: string, extra = {}) { return { id: `svc-${name}`, name, type: "application", status: "draft", source_type: "git", repo_url: `https://github.com/example/${name}`, branch: "main", configuration: { schema_version: "opsi.service_configuration/v1", revision: 1, state_hash: "a".repeat(64), bindings: [] }, ...extra }; }
function repository(id: number, full_name: string) { return { repository_id: id, installation_id: 11, full_name, default_branch: "main", status: "active", claim_status: "active" }; }
function binding(name: string, repository_id: number, application_root: string, build_strategy: string) { return { id: `binding-${name}`, project_id: "proj-1", service_id: `svc-${name}`, repository_id, installation_id: 11, service_key: name, config_path: ".opsi/opsi-cd.yaml", selected_ref: "main", application_root, build_context: ".", build_strategy, ...(build_strategy === "dockerfile" ? { dockerfile_path: "Dockerfile" } : {}), status: "active" }; }
function job(state: { sha: string }, id: string, status: string, application_id: string, name: string, created_at: string, strategy = "buildpack") { return { id, project_id: "proj-1", environment_id: "env-1", application_id, source: { binding_id: `binding-${name}`, binding_updated_at: created_at, github_installation_id: 11, repository_id: 101, repository_owner_id: 12, repository_full_name: `example/${name}`, selected_ref: "main", resolved_commit_sha: state.sha, application_root: name === "api" ? "apps/api" : ".", build_context: "." }, requested_build_strategy: strategy, resolved_build_strategy: strategy, ...(strategy === "dockerfile" ? { dockerfile_path: "Dockerfile" } : {}), status, failure_code: status === "failed" ? "BUILDPACK_BUILD_FAILED" : undefined, failure_message_redacted: status === "failed" ? "Application compile failed." : undefined, build_record_id: status === "succeeded" ? id.replace("job", "record") : undefined, created_by: "owner", created_at, updated_at: created_at }; }
function record(state: { sha: string; digest: string }, id: string, service_id: string, service_key: string, build_job_id: string, strategy: string) { return { schema_version: "opsi.build_record/v1", id, project_id: "proj-1", repository_id: 101, repository_owner_id: 12, active_binding_id: `binding-${service_key}`, service_id, service_key, created_at: "2026-08-12T02:05:00Z", workload: { issuer: "github", subject: `repo:example/${service_key}`, repository_id: 101, repository_owner_id: 12, ref: "refs/heads/main", sha: state.sha, event_name: "workflow_dispatch", workflow: "build", workflow_ref: "workflow", run_id: 1, run_attempt: 1 }, build: { config_hash: "config", platform: "linux/amd64", oci_repository: `ghcr.io/example/${service_key === "api" ? "web/api" : service_key}`, oci_digest: state.digest, build_job_id, build_strategy: strategy, builder_identity: strategy === "dockerfile" ? "moby/buildkit" : "paketobuildpacks/builder-jammy-base", builder_version: "1.0", builder: strategy === "buildpack" ? { builder_image: "paketobuildpacks/builder-jammy-base", lifecycle_version: "0.20.0", pack_version: "0.38.2", processes: [{ type: "web", default: true }] } : undefined, status: "succeeded" } }; }
function fulfill(route: Route, body: unknown, status = 200) { return route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status }); }
