import { expect, test, type Route } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

const hash = (character: string) => character.repeat(64);
const digest = (character: string) => `sha256:${hash(character)}`;

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Topology reviews canonical multi-service deployments, retries only missing jobs, and restores Live from Cloud", async ({ page }) => {
  const state = fixture();
  (state.topology.assignments.find((item) => item.service_key === "api")!.exposure as { mode: string }).mode = "internal";
  const submissions: Array<{ service: string; key: string; body: Record<string, unknown> }> = [];
  const exposurePosts: string[] = [];
  let workerFailures = 1;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.includes("/exposures")) exposurePosts.push(path);
    if (route.request().method() === "POST" && path.endsWith("/deployments/preview")) {
      const request = route.request().postDataJSON() as Record<string, unknown>;
      expect(request).not.toHaveProperty("workload");
      const build = state.builds.find((item) => item.id === request.build_record_id)!;
      const service = state.services.find((item) => item.id === build.service_id)!;
      const assignment = state.topology.assignments.find((item) => item.service_key === service.name)!;
      await json(route, preview(build, service, assignment));
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/deployments")) {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      const build = state.builds.find((item) => item.id === body.build_record_id)!;
      const key = route.request().headers()["idempotency-key"] ?? "";
      submissions.push({ service: build.service_id, key, body });
      if (build.service_id === "worker" && workerFailures-- > 0) {
        await route.fulfill({ body: JSON.stringify({ code: "AGENT_UNAVAILABLE", message: "Agent temporarily unavailable" }), contentType: "application/json", status: 503 });
        return;
      }
      let job = state.deployments.find((item) => item.service_id === build.service_id);
      if (!job) { job = deployment(build); state.deployments.push(job); }
      job.status = "succeeded";
      job.rollout_state = "succeeded";
      await json(route, job, 202);
      return;
    }
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.getByLabel("api BuildRecord")).toHaveValue("build-api");
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "api" })).toContainText("pending");
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "reports" })).toContainText("No succeeded accepted BuildRecord");
  await page.getByRole("button", { name: "Review selected" }).click();
  await expect(page.getByText(digest("a"), { exact: true })).toBeVisible();
  await expect(page.getByText("probes /healthz / /healthz", { exact: true })).toBeVisible();
  await expect(page.getByText("env LOG_LEVEL=info", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("configuration revision 1", { exact: true }).first()).toBeVisible();
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "reports" })).toContainText("No succeeded accepted BuildRecord");
  await page.getByLabel("Select all placed applications").uncheck();
  await expect(page.getByRole("button", { name: "Deploy" })).toBeDisabled();
  await page.getByLabel("Select all placed applications").check();
  await page.getByRole("button", { name: "Review selected" }).click();

  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/deployments", status: 503, method: "POST" });
  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page).toHaveURL(/topologyMode=live/);
  await page.getByRole("button", { name: "Design", exact: true }).click();
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "api" })).toContainText("Running");
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "worker" })).toContainText("Failed");
  await expect(page.locator(".deploymentReviewRow").filter({ hasText: "reports" })).toContainText("blocked");

  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page).toHaveURL(/topologyMode=live/);
  expect(submissions.filter((item) => item.service === "api")).toHaveLength(1);
  const workerSubmissions = submissions.filter((item) => item.service === "worker");
  expect(workerSubmissions).toHaveLength(2);
  expect(workerSubmissions[0].key).toBe(workerSubmissions[1].key);
  expect(new Set(submissions.map((item) => item.key)).size).toBe(2);
  expect(exposurePosts).toEqual([]);

  for (const job of state.deployments) { job.status = "succeeded"; job.rollout_state = "succeeded"; }
  await page.reload();
  await expect(page.getByRole("button", { name: "Live", exact: true })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("Running", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("Workload is ready", { exact: true }).first()).toBeVisible();
});

test("deployment review fails closed without a current environment and selects the exact environment assignment", async ({ page }) => {
  const state = fixture();
  state.facts.environments.push({ id: "env-stage", project_id: "proj-1", name: "Staging", type: "stage", status: "active" });
  state.facts.runtimes.push({ ...state.facts.runtimes[0], id: "runtime-stage", environment_id: "env-stage", name: "Staging runtime" });
  state.topology.assignments.push({ ...state.topology.assignments[0], environment_id: "env-stage", runtime_id: "runtime-stage" });
  const environments: string[] = [];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.endsWith("/deployments/preview")) {
      const request = route.request().postDataJSON() as { build_record_id: string; environment_id: string };
      environments.push(request.environment_id);
      const buildRecord = state.builds.find((item) => item.id === request.build_record_id)!;
      const serviceRecord = state.services.find((item) => item.id === buildRecord.service_id)!;
      const assignment = state.topology.assignments.find((item) => item.service_key === serviceRecord.name && item.environment_id === request.environment_id)!;
      await json(route, preview(buildRecord, serviceRecord, assignment));
      return;
    }
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.getByText("Choose the current environment before reviewing or deploying.")).toBeVisible();
  await page.getByLabel("Current environment").selectOption("env-stage");
  await expect(page).toHaveURL(/environment=env-stage/);
  await expect(page.getByText("Deployment authority · Staging")).toBeVisible();
  await page.getByRole("button", { name: "Review selected" }).click();
  expect(environments).toEqual(["env-stage"]);
  await expect(page.getByText("runtime-stage", { exact: false }).first()).toBeVisible();
});

test("public deployment waits for workload success and keeps route failure as a degraded second phase", async ({ page }) => {
  const state = fixture();
  const api = state.services.find((item) => item.id === "api")! as ReturnType<typeof service> & { configuration: ReturnType<typeof service>["configuration"] & { public_route?: { hostname: string; path: string } } };
  api.configuration.public_route = { hostname: "apps.example.com", path: "/api" };
  (state.topology.assignments.find((item) => item.service_key === "api")!.exposure as { mode: string }).mode = "public";
  const calls: string[] = [];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.endsWith("/deployments/preview")) {
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      const serviceRecord = state.services.find((item) => item.id === buildRecord.service_id)!;
      const assignment = state.topology.assignments.find((item) => item.service_key === serviceRecord.name)!;
      await json(route, preview(buildRecord, serviceRecord, assignment));
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/deployments")) {
      calls.push("workload-succeeded");
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      const job = { ...deployment(buildRecord), status: "succeeded", rollout_state: "succeeded", current_digest: buildRecord.build.oci_digest, snapshot: preview(buildRecord, state.services.find((item) => item.id === buildRecord.service_id)!, state.topology.assignments.find((item) => item.service_key === buildRecord.service_id)!).snapshot };
      state.deployments.push(job);
      await json(route, job, 202);
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/exposures/preview")) {
      calls.push("exposure-preview");
      const request = route.request().postDataJSON() as { exposure: Record<string, unknown> };
      await json(route, { schema_version: "opsi.exposure_preview/v1", base_deployment_job_id: "deployment-api", desired: request.exposure, changes: ["add"], eligible: true, decision_code: "EXPOSURE_READY", message: "Ready", state_hash: hash("e"), resolved_at: "2026-08-08T08:00:00Z" });
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/exposures")) {
      calls.push("exposure-failed");
      const request = route.request().postDataJSON() as { base_deployment_job_id: string; exposure: Record<string, unknown> };
      const job = { id: String(request.exposure.deployment_job_id), project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_id: "api", status: "failed", rollout_state: "failed", action: "apply", base_deployment_id: request.base_deployment_job_id, desired_digest: digest("a"), exposure_spec: request.exposure, failure_code: "EXPOSURE_APPLY_FAILED", failure_message_redacted: "Ingress reconciliation failed", rollback_eligible: false, rollback_blocked_reason: "route rollout did not succeed", created_at: "2026-08-08T08:01:00Z" };
      state.deployments.push(job as ReturnType<typeof deployment>);
      await json(route, job, 202);
      return;
    }
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology&environment=env-prod");
  await page.getByLabel("Select all placed applications").uncheck();
  await page.getByLabel("Select api").check();
  await page.getByRole("button", { name: "Review selected" }).click();
  const review = page.locator(".deploymentReviewRow").filter({ hasText: "api" });
  await expect(review).toContainText("Publish route");
  await expect(review).toContainText("apps.example.com/api");
  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page).toHaveURL(/topologyMode=live/);
  expect(calls).toEqual(["workload-succeeded", "exposure-preview", "exposure-failed"]);
  await expect(page.locator(".liveDeploymentList li").filter({ hasText: "Deploy workload" })).toContainText("Running");
  await expect(page.locator(".liveDeploymentList li").filter({ hasText: "Publish route" })).toContainText("Degraded");
  await expect(page.locator(".liveDeploymentList").getByText("apps.example.com/api", { exact: true })).toBeVisible();
});

test("public deployment failure never starts Exposure", async ({ page }) => {
  const state = fixture();
  const api = state.services.find((item) => item.id === "api")! as ReturnType<typeof service> & { configuration: ReturnType<typeof service>["configuration"] & { public_route?: { hostname: string; path: string } } };
  api.configuration.public_route = { hostname: "apps.example.com", path: "/api" };
  (state.topology.assignments.find((item) => item.service_key === "api")!.exposure as { mode: string }).mode = "public";
  const calls: string[] = [];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.endsWith("/deployments/preview")) {
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      const serviceRecord = state.services.find((item) => item.id === buildRecord.service_id)!;
      const assignment = state.topology.assignments.find((item) => item.service_key === serviceRecord.name)!;
      await json(route, preview(buildRecord, serviceRecord, assignment));
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/deployments")) {
      calls.push("workload-failed");
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      await json(route, { ...deployment(buildRecord), status: "failed", rollout_state: "failed", failure_code: "WORKLOAD_FAILED", failure_message_redacted: "Workload rollout failed" }, 202);
      return;
    }
    if (route.request().method() === "POST" && path.includes("/exposures")) calls.push("exposure-started");
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology&environment=env-prod");
  await page.getByLabel("Select all placed applications").uncheck();
  await page.getByLabel("Select api").check();
  await page.getByRole("button", { name: "Review selected" }).click();
  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page.getByText("Workload did not succeed; route was not published.")).toBeVisible();
  expect(calls).toEqual(["workload-failed"]);
});

test("unchanged public route skips the Exposure apply rollout", async ({ page }) => {
  const state = fixture();
  const api = state.services.find((item) => item.id === "api")! as ReturnType<typeof service> & { configuration: ReturnType<typeof service>["configuration"] & { public_route?: { hostname: string; path: string } } };
  api.configuration.public_route = { hostname: "apps.example.com", path: "/" };
  (state.topology.assignments.find((item) => item.service_key === "api")!.exposure as { mode: string }).mode = "public";
  let exposureApplies = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.endsWith("/deployments/preview")) {
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      await json(route, preview(buildRecord, state.services.find((item) => item.id === buildRecord.service_id)!, state.topology.assignments.find((item) => item.service_key === buildRecord.service_id)!));
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/deployments")) {
      const buildRecord = state.builds.find((item) => item.id === (route.request().postDataJSON() as { build_record_id: string }).build_record_id)!;
      const job = { ...deployment(buildRecord), status: "succeeded", rollout_state: "succeeded" };
      state.deployments.push(job);
      await json(route, job, 202);
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/exposures/preview")) {
      const request = route.request().postDataJSON() as { exposure: Record<string, unknown> };
      await json(route, { desired: request.exposure, changes: ["unchanged"], eligible: true, decision_code: "EXPOSURE_READY", message: "Already configured", state_hash: hash("e"), resolved_at: "2026-08-08T08:00:00Z" });
      return;
    }
    if (route.request().method() === "POST" && path.endsWith("/exposures")) exposureApplies++;
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology&environment=env-prod");
  await page.getByLabel("Select all placed applications").uncheck();
  await page.getByLabel("Select api").check();
  await page.getByRole("button", { name: "Review selected" }).click();
  await page.getByRole("button", { name: "Deploy" }).click();
  await expect(page).toHaveURL(/topologyMode=live/);
  expect(exposureApplies).toBe(0);
  expect(state.deployments).toHaveLength(1);
});

test("rollback is Cloud-gated, idempotent, and refresh restores the factual known-good endpoint", async ({ page }) => {
  const state = fixture();
  const snapshot = preview(state.builds.find((item) => item.id === "build-api")!, state.services[0], state.topology.assignments[0]).snapshot;
  const first = { ...deployment(state.builds[1]), id: "dep-first", status: "succeeded", rollout_state: "succeeded", current_digest: digest("1"), rollback_eligible: false, rollback_blocked_reason: "no previous known-good deployment is available", snapshot, created_at: "2026-08-08T07:00:00Z" };
  const current = { ...deployment(state.builds[1]), id: "dep-current", status: "succeeded", rollout_state: "succeeded", current_digest: digest("2"), previous_digest: digest("1"), rollback_eligible: true, snapshot, exposure_spec: { schema_version: "opsi.exposure_spec/v1", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", service_key: "api", deployment_job_id: "dep-current", hostname: "apps.example.com", path: "/api", service_port: 8080, tls: { mode: "disabled" }, spec_hash: hash("f") }, created_at: "2026-08-08T08:00:00Z" };
  state.deployments.push(first, current);
  const keys: string[] = [];
  let attempts = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST" && path.endsWith("/dep-current/rollback")) {
      keys.push(route.request().headers()["idempotency-key"] ?? "");
      if (attempts++ === 0) {
        await route.fulfill({ body: JSON.stringify({ code: "AGENT_UNAVAILABLE", message: "Agent temporarily unavailable" }), contentType: "application/json", status: 503 });
        return;
      }
      const queued = { ...current, id: "dep-rollback", status: "queued", rollout_state: "prepared", action: "rollback", base_deployment_id: current.id, rollback_eligible: false, rollback_blocked_reason: "rollback job cannot be rolled back", created_at: "2026-08-08T08:02:00Z" };
      state.deployments.push(queued);
      await json(route, queued, 202);
      Object.assign(queued, { status: "rolled_back", rollout_state: "rolled_back", current_digest: digest("1") });
      return;
    }
    await respond(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology&topologyMode=live&environment=env-prod");
  const firstCard = page.locator(".liveDeploymentList li").filter({ hasText: "dep-first" });
  await expect(firstCard.getByRole("button", { name: "Rollback" })).toBeDisabled();
  await expect(firstCard).toContainText("no previous known-good deployment is available");
  const currentCard = page.locator(".liveDeploymentList li").filter({ hasText: "dep-current" });
  await currentCard.getByRole("button", { name: "Rollback" }).click();
  await expect(page.getByText(`current digest: ${digest("2")}`)).toBeVisible();
  await expect(page.getByText(`previous known-good digest: ${digest("1")}`)).toBeVisible();
  await page.getByLabel(/Type dep-current to confirm/).fill("dep-current");
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/deployments/dep-current/rollback", status: 503, method: "POST" });
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.locator(".errorBox")).toContainText("Agent temporarily unavailable");
  await Promise.all([
    page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/dep-current/rollback") && response.status() === 202),
    page.getByRole("button", { name: "Retry same attempt" }).click(),
  ]);
  await page.reload();
  expect(keys).toHaveLength(2);
  expect(keys[0]).toBe(keys[1]);
  const rollbackCard = page.locator(".liveDeploymentList li").filter({ hasText: "dep-rollback" });
  await expect(rollbackCard).toContainText("Rollback");
  await expect(rollbackCard).toContainText("Running");
  await expect(rollbackCard).toContainText(digest("1"));
  await expect(rollbackCard).toContainText("apps.example.com/api");
});

type State = ReturnType<typeof fixture>;

async function respond(route: Route, state: State) {
  const path = new URL(route.request().url()).pathname;
  let body: unknown = {};
  if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: "proj-1" };
  else if (path === "/api/local/projects") body = { projects: state.projects };
  else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
  else if (path.endsWith("/nodes")) body = { nodes: state.nodes };
  else if (path.endsWith("/services")) body = { services: state.services };
  else if (path.endsWith("/deployments")) body = { deployments: state.deployments };
  else if (/\/deployments\/[^/]+$/.test(path)) body = state.deployments.find((item) => item.id === path.split("/").at(-1));
  else if (/\/deployments\/[^/]+\/events$/.test(path)) body = { events: [{ id: `event-${path.split("/").at(-2)}`, step: "waiting_ready", message_redacted: "Workload is ready", progress_percent: 100, created_at: "2026-08-08T08:00:00Z" }] };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: [] };
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/support")) body = { generated_at: "2026-08-08T08:00:00Z", counts: {}, signals: [] };
  else if (path.endsWith("/topology/facts")) body = state.facts;
  else if (path.endsWith("/topology")) body = state.topology;
  else if (path.endsWith("/github/repositories")) body = { repositories: [] };
  else if (path.endsWith("/github/bindings")) body = { bindings: [] };
  else if (path.endsWith("/build-records")) body = { records: state.builds };
  else if (path.endsWith("/deployment-policies")) body = { policies: state.policies };
  else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: [] };
  await json(route, body);
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status });
}

function fixture() {
  const services = [service("api", 8080, "/healthz", "1"), service("worker", 9000, "/ready", "2"), service("reports", 7000, "/health", "3")];
  const assignments = services.map((item, index) => ({ service_key: item.name, environment_id: "env-prod", runtime_id: "runtime-primary", replicas: index + 1, cpu_request_millicores: 250, memory_request_bytes: 268435456, exposure: { mode: "none" as const } }));
  return {
    projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout", slug: "checkout", status: "ready" }],
    services,
    nodes: [{ id: "node-primary", name: "Primary", role: "server", status: "healthy", agent_id: "agent-primary", k3s_status: "ready" }],
    facts: { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-primary", project_id: "proj-1", environment_id: "env-prod", name: "Primary", type: "k3s", status: "ready" }], nodes: [{ id: "node-primary", project_id: "proj-1", runtime_id: "runtime-primary", status: "healthy", cpu_cores: 4, memory_mb: 8192 }], agents: [{ id: "agent-primary", project_id: "proj-1", runtime_id: "runtime-primary", node_id: "node-primary", status: "active", capabilities: { deploy: true } }], services: services.map((item) => ({ id: item.id, project_id: "proj-1", key: item.name })) },
    topology: { schema_version: "opsi.topology_plan/v1" as const, id: "topology-1", project_id: "proj-1", revision: 4, state_hash: hash("4"), plan_hash: hash("5"), created_by: "owner", applied_by: "owner", created_at: "2026-08-08T07:00:00Z", applied_at: "2026-08-08T07:00:00Z", assignments },
    builds: [{ ...build("api", "d"), id: "build-api-old", created_at: "2026-08-08T06:30:00Z" }, build("api", "a"), build("worker", "b")],
    policies: [{ schema_version: "opsi.deployment_policy/v1", id: "policy-1", revision: 2, state_hash: hash("6"), policy_hash: hash("7"), policy: { schema_version: "opsi.deployment_policy/v1", project_id: "proj-1", repository_id: 7, service_keys: ["api", "worker"], workflow_refs: ["workflow"], allowed_events: ["push"], allowed_git_refs: ["refs/heads/main"], environment_id: "env-prod", allowed_runtime_ids: ["runtime-primary"], allowed_oci_repositories: ["registry.test/api", "registry.test/worker"], allowed_platforms: ["linux/amd64"], allowed_config_hashes: [hash("8")], allowed_build_plan_hashes: [hash("9")], allow_unknown_capacity: false, enabled: true }, created_by: "owner", applied_by: "owner", created_at: "2026-08-08T07:00:00Z", applied_at: "2026-08-08T07:00:00Z" }],
    deployments: [] as Array<ReturnType<typeof deployment>>,
  };
}

function service(name: string, port: number, path: string, character: string) {
  return { id: name, name, type: "application", status: "ready", source_type: "image", container_port: port, health_path: path, configuration: { schema_version: "opsi.service_configuration/v1" as const, revision: 1, state_hash: hash(character), environment: [{ name: "LOG_LEVEL", value: "info" }], bindings: [] } };
}

function build(serviceID: string, character: string) {
  return { schema_version: "opsi.build_record/v1" as const, id: `build-${serviceID}`, project_id: "proj-1", repository_id: 7, repository_owner_id: 8, active_binding_id: `binding-${serviceID}`, service_id: serviceID, service_key: serviceID, created_at: "2026-08-08T07:30:00Z", workload: { issuer: "issuer", subject: "subject", repository_id: 7, repository_owner_id: 8, ref: "refs/heads/main", sha: character.repeat(40), event_name: "push", workflow: "workflow", workflow_ref: "workflow", run_id: 1, run_attempt: 1 }, build: { config_hash: hash("8"), plan_hash: hash("9"), platform: "linux/amd64", oci_repository: `registry.test/${serviceID}`, oci_digest: digest(character), status: "succeeded" } };
}

function preview(buildRecord: ReturnType<typeof build>, serviceRecord: ReturnType<typeof service>, assignment: ReturnType<typeof fixture>["topology"]["assignments"][number]) {
  const probe = { path: serviceRecord.health_path, port: serviceRecord.container_port, initial_delay_seconds: 2, period_seconds: 5, timeout_seconds: 2, failure_threshold: 6 };
  const workloadExposure = assignment.exposure.mode === "none" ? "none" : "internal";
  return { schema_version: "opsi.deployment_job/v1", snapshot: { project_id: "proj-1", image: { repository: buildRecord.build.oci_repository, digest: buildRecord.build.oci_digest, reference: `${buildRecord.build.oci_repository}@${buildRecord.build.oci_digest}` }, authority: { build_record: buildRecord, topology_plan_id: "topology-1", topology_revision: 4, topology_hash: hash("5"), service_configuration_revision: 1, service_configuration_state_hash: serviceRecord.configuration.state_hash, deployment_policy_id: "policy-1", deployment_policy_revision: 2, deployment_policy_hash: hash("7"), runtime_id: assignment.runtime_id, node_id: "node-primary", agent_id: "agent-primary" }, workload: { schema_version: "opsi.workload_spec/v1", service_key: serviceRecord.name, replicas: assignment.replicas, application_container_name: "app", container_port: serviceRecord.container_port, readiness_probe: probe, liveness_probe: probe, resources: { requests: { cpu: "250m", memory: "256Mi" }, limits: { cpu: "250m", memory: "256Mi" } }, termination_grace_period_seconds: 30, environment: serviceRecord.configuration.environment, exposure: { mode: workloadExposure } }, spec_hash: hash("c") }, changes: ["workload_spec", "image_digest"], eligible: true, decision_code: "ELIGIBLE", message: "Eligible", resolved_at: "2026-08-08T07:45:00Z" };
}

function deployment(buildRecord: ReturnType<typeof build>) {
  return { id: `deployment-${buildRecord.service_id}`, project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-primary", node_id: "node-primary", agent_id: "agent-primary", service_id: buildRecord.service_id, status: "queued", rollout_state: "queued", created_at: "2026-08-08T08:00:00Z" };
}
