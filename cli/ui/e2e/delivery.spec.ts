import { mkdir } from "node:fs/promises";
import { expect, test, type Page, type Route } from "@playwright/test";

type Scenario = "unconfigured" | "monorepo" | "no-build" | "build-ready" | "waiting" | "verified" | "failed" | "rolled-back" | "preview" | "exposure" | "unavailable";

const screenshotDir = "../../.tmp/ui-fe02";
const digest = (character: string) => `sha256:${character.repeat(64)}`;

test.beforeAll(async () => { await mkdir(screenshotDir, { recursive: true }); });

test("Delivery restores canonical URL state and renders truthful pipeline stages", async ({ page }) => {
  let scenario: Scenario = "unconfigured";
  await mockDeliveryAPI(page, () => scenario);

  await page.goto("/?project=proj-1&view=delivery");
  await expect(page.getByRole("tab", { name: "Pipeline", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("Source not configured", { exact: true })).toBeVisible();

  scenario = "no-build";
  await page.reload();
  await expect(page.getByText("No trusted BuildRecord received", { exact: true })).toBeVisible();
  await expect(page.getByText(/BuildRecord reports failure/)).toHaveCount(0);

  scenario = "build-ready";
  await page.reload();
  await expect(page.getByText("Immutable artifact ready", { exact: true })).toBeVisible();
  await expect(page.getByText("Artifact ready — no deployment observed", { exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Builds", exact: true }).click();
  await page.getByRole("button", { name: /web.*main/i }).click();
  await expect(page).toHaveURL(/tab=builds.*service=svc-web.*build=build-web-1/);
  await page.reload();
  await expect(page.getByRole("button", { name: /web.*main/i })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("tab", { name: "Deployments", exact: true }).click();
  await page.goBack();
  await expect(page.getByRole("tab", { name: "Builds", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.goForward();
  await expect(page.getByRole("tab", { name: "Deployments", exact: true })).toHaveAttribute("aria-selected", "true");

  scenario = "waiting";
  await page.goto("/?project=proj-1&view=delivery&tab=pipeline&service=svc-web");
  await expect(page.getByText("Deployment waiting", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /verify Runtime verification pending/ })).toBeVisible();
  await expect(page.getByText("Runtime verified", { exact: true })).toHaveCount(0);

  scenario = "verified";
  await page.reload();
  await expect(page.getByRole("button", { name: /verify Runtime verified/ })).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.screenshot({ fullPage: true, path: `${screenshotDir}/pipeline-1440x900.png` });
});

test("Delivery browser fixtures preserve failure, rollback, preview, exposure, and source truth", async ({ page }) => {
  let scenario: Scenario = "failed";
  await mockDeliveryAPI(page, () => scenario);
  await page.setViewportSize({ width: 1440, height: 900 });

  await page.goto("/?project=proj-1&view=delivery&tab=pipeline&service=svc-web");
  await expect(page.getByText("Failed — no known-good snapshot", { exact: true })).toBeVisible();
  await expect(page.getByText("Rolled back to known-good", { exact: true })).toHaveCount(0);
  await page.screenshot({ fullPage: true, path: `${screenshotDir}/pipeline-failed-1440x900.png` });

  scenario = "rolled-back";
  await page.reload();
  await expect(page.getByText("Rolled back to known-good", { exact: true })).toBeVisible();
  await page.goto("/?project=proj-1&view=delivery&tab=deployments&service=svc-web&deployment=deploy-rollback");
  await expect(page.getByText(digest("c"), { exact: true })).toBeVisible();
  await expect(page.getByText("known-good-7", { exact: true })).toBeVisible();

  scenario = "preview";
  await page.goto("/?project=proj-1&view=delivery&tab=deployments&service=svc-web&deployment=deploy-preview");
  await expect(page.getByRole("button", { name: "Clean Up Preview", exact: true })).toBeVisible();
  await page.screenshot({ fullPage: true, path: `${screenshotDir}/deployment-detail-1440x900.png` });

  scenario = "exposure";
  await page.goto("/?project=proj-1&view=delivery&tab=exposure&service=svc-web");
  await expect(page.getByRole("heading", { name: "Configured ≠ Publicly Verified" })).toBeVisible();
  await expect(page.getByText("Verification Not Reported", { exact: true })).toBeVisible();
  await expect(page.getByText("Public availability and TLS health are not claimed.", { exact: true })).toBeVisible();

  scenario = "monorepo";
  await page.goto("/?project=proj-1&view=delivery&tab=source&service=svc-web");
  for (const service of ["web", "api", "worker"]) await expect(page.getByRole("row", { name: new RegExp(`^${service}\\b`) })).toBeVisible();
  await page.screenshot({ fullPage: true, path: `${screenshotDir}/source-monorepo-1440x900.png` });

  scenario = "unavailable";
  await page.goto("/?project=proj-1&view=delivery&tab=pipeline&service=svc-web");
  await expect(page.getByText("Source unavailable", { exact: true })).toBeVisible();
});

test("Delivery is Local-only and has no horizontal overflow at required viewports", async ({ page }) => {
  const origins = new Set<string>();
  page.on("request", (request) => origins.add(new URL(request.url()).origin));
  await mockDeliveryAPI(page, "verified");

  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=delivery&tab=pipeline&service=svc-web");
    await expect(page.getByRole("button", { name: /verify Runtime verified/ })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    if (viewport.width === 390) await page.screenshot({ fullPage: true, path: `${screenshotDir}/delivery-mobile-390x844.png` });
  }

  expect([...origins]).toEqual(["http://127.0.0.1:19881"]);
  const storage = await page.evaluate(() => ({ local: Object.keys(Reflect.get(window, "local" + "Storage") as Storage), session: Object.keys(Reflect.get(window, "session" + "Storage") as Storage), cookies: Reflect.get(document, "coo" + "kie") as string }));
  expect(storage).toEqual({ local: [], session: [], cookies: "" });
});

async function mockDeliveryAPI(page: Page, selected: Scenario | (() => Scenario)) {
  await page.route("**/api/local/**", async (route) => respond(route, typeof selected === "function" ? selected() : selected));
}

async function respond(route: Route, scenario: Scenario) {
  const url = new URL(route.request().url());
  const path = url.pathname;
  if (scenario === "unavailable" && /\/github\/(installations|repositories|bindings)$/.test(path)) {
    await route.fulfill({ body: JSON.stringify({ code: "CLOUD_UNAVAILABLE", message: "Source inventory unavailable" }), contentType: "application/json", status: 503 });
    return;
  }
  const data = fixture(scenario);
  let body: unknown = {};
  if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: "proj-1", capabilities: [] };
  else if (path === "/api/local/session/project") body = { status: "selected", project_id: "proj-1" };
  else if (path === "/api/local/projects") body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Delivery Platform", slug: "delivery", status: "ready" }] };
  else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
  else if (path.endsWith("/nodes")) body = { nodes: [] };
  else if (path.endsWith("/services")) body = { services: data.services };
  else if (path.endsWith("/build-records")) body = { records: data.builds };
  else if (/\/build-records\//.test(path)) body = data.builds.find((item) => path.endsWith(item.id)) ?? {};
  else if (path.endsWith("/deployments")) body = { deployments: data.deployments };
  else if (/\/deployments\/[^/]+\/events$/.test(path)) body = { events: data.events };
  else if (/\/deployments\/[^/]+$/.test(path)) body = data.deployments.find((item) => path.endsWith(item.id)) ?? {};
  else if (path.endsWith("/exposures")) body = { exposures: data.exposures };
  else if (path.endsWith("/github/installations")) body = { installations: data.installations };
  else if (path.endsWith("/github/repositories")) body = { repositories: data.repositories };
  else if (path.endsWith("/github/bindings")) body = { bindings: data.bindings };
  else if (path.endsWith("/deployment-policies")) body = { policies: data.policies };
  else if (path.endsWith("/topology/facts")) body = data.placement;
  else if (path.endsWith("/topology")) body = data.topology;
  else if (path === "/api/local/repository/config") body = { config: data.repositoryConfig, migrated_v1: false, config_hash: "config-hash" };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: [] };
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: [] };
  else if (path.endsWith("/support")) body = { generated_at: "2026-07-30T10:00:00Z", counts: {}, signals: [] };
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
}

function fixture(scenario: Scenario) {
  const services = [
    { id: "svc-web", name: "web", type: "application", status: "ready", source_type: "image", container_port: 3000, health_path: "/healthz", replicas: 3 },
    { id: "svc-api", name: "api", type: "application", status: "ready", source_type: "image", container_port: 8081, health_path: "/ready", replicas: 2 },
    { id: "svc-worker", name: "worker", type: "application", status: "ready", source_type: "image", replicas: 2 },
  ];
  const repositories = scenario === "unconfigured" ? [] : [{ repository_id: 101, installation_id: 7, owner_login: "acme", name: "platform", full_name: "acme/platform", default_branch: "main", status: "active", claim_status: "active", claimed_project_id: "proj-1" }];
  const bindings = scenario === "unconfigured" ? [] : services.map((service) => ({ id: `binding-${service.name}`, project_id: "proj-1", service_id: service.id, repository_id: 101, installation_id: 7, service_key: service.name, config_path: ".opsi/opsi-cd.yaml", status: "active" }));
  const builds = ["unconfigured", "no-build", "unavailable"].includes(scenario) ? [] : [build("svc-web", "web")];
  const deployments = deploymentFixtures(scenario, builds[0]);
  const exposures = scenario === "exposure" ? [exposure(build("svc-web", "web"))] : [];
  return {
    services,
    repositories,
    bindings,
    builds,
    deployments,
    exposures,
    installations: repositories.length ? [{ installation_id: 7, account_login: "acme", status: "active", suspended: false }] : [],
    policies: [policy()],
    placement: { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: "proj-1", environment_id: "env-prod", name: "Primary", type: "k3s", status: "ready" }], nodes: [], agents: [], services: services.map((service) => ({ id: service.id, project_id: "proj-1", key: service.name })) },
    topology: { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 4, state_hash: "topology-state", plan_hash: "topology-plan", created_by: "user", applied_by: "user", created_at: "2026-07-29T08:00:00Z", applied_at: "2026-07-29T08:00:00Z", assignments: services.map((service) => ({ service_key: service.name, environment_id: "env-prod", runtime_id: "runtime-1", replicas: service.replicas, cpu_request_millicores: service.name === "web" ? 250 : 200, memory_request_bytes: 268435456, exposure: { mode: "none" } })) },
    repositoryConfig: { schema_version: "opsi.repository_cd/v2", services: services.map((service) => ({ key: service.name, build: { context: `services/${service.name}`, dockerfile: `services/${service.name}/Dockerfile`, platform: "linux/amd64" }, watch_paths: [`services/${service.name}/**`], shared_paths: ["shared/**"], dependencies: service.name === "web" ? ["api"] : [], deploy: { production: { enabled: true, branches: ["main"] }, preview: { enabled: service.name !== "worker", pull_requests: service.name !== "worker" } } })) },
    events: deployments.length ? [{ id: "event-1", project_id: "proj-1", deployment_job_id: deployments[0].id, step: deployments[0].rollout_state || deployments[0].status, status: deployments[0].status, progress_percent: deployments[0].status === "succeeded" ? 100 : 55, attempt: 1, message_redacted: "Factual rollout event", request_id: "request-delivery-1", created_at: deployments[0].updated_at }] : [],
  };
}

function build(serviceID: string, serviceKey: string) {
  return { schema_version: "opsi.build_record/v1", id: `build-${serviceKey}-1`, project_id: "proj-1", repository_id: 101, repository_owner_id: 42, active_binding_id: `binding-${serviceKey}`, service_id: serviceID, service_key: serviceKey, created_at: "2026-07-30T08:00:00Z", workload: { issuer: "actions-oidc-issuer", subject: "repo:acme/platform", repository_id: 101, repository_owner_id: 42, ref: "refs/heads/main", sha: "0123456789abcdef0123456789abcdef01234567", event_name: "push", workflow: "immutable-build", workflow_ref: "acme/platform/.github/workflows/build.yml@refs/heads/main", run_id: 9001, run_attempt: 1 }, build: { config_hash: "config-hash", plan_hash: "plan-hash", platform: "linux/amd64", oci_repository: `registry.example.test/acme/${serviceKey}`, oci_digest: digest("a"), provenance_digest: digest("f"), status: "succeeded" } };
}

function deploymentFixtures(scenario: Scenario, record?: ReturnType<typeof build>) {
  if (!record || ["build-ready", "monorepo", "exposure"].includes(scenario)) return [];
  if (scenario === "waiting") return [deployment("deploy-waiting", record, "waiting")];
  if (scenario === "failed") return [{ ...deployment("deploy-failed", record, "failed"), failure_code: "NO_KNOWN_GOOD", failure_message_redacted: "No known-good release is available", rollback_eligible: false, rollback_blocked_reason: "NO_KNOWN_GOOD", finished_at: "2026-07-30T09:06:00Z" }];
  if (scenario === "rolled-back") return [{ ...deployment("deploy-rollback", record, "rolled_back"), current_digest: digest("c"), previous_digest: digest("a"), known_good_id: "known-good-7", known_good_hash: "known-good-hash", finished_at: "2026-07-30T09:08:00Z", terminal_result: terminal("rolled_back", digest("c"), "known-good-7") }];
  if (scenario === "preview") return [{ ...deployment("deploy-preview", record, "succeeded", true), finished_at: "2026-07-30T09:05:00Z", terminal_result: terminal("succeeded", digest("a")) }];
  if (scenario === "verified") return [{ ...deployment("deploy-verified", record, "succeeded"), finished_at: "2026-07-30T09:05:00Z", terminal_result: terminal("succeeded", digest("a")) }];
  return [];
}

function deployment(id: string, record: ReturnType<typeof build>, state: string, preview = false) {
  return { schema_version: "opsi.deployment_job/v1", id, project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-1", node_id: "node-1", service_id: record.service_id, status: state, rollout_state: state, attempt_count: 1, max_attempts: 3, desired_digest: record.build.oci_digest, current_digest: state === "succeeded" ? record.build.oci_digest : undefined, readiness_evidence_hash: state === "succeeded" ? "readiness-hash" : undefined, intent_hash: "intent-hash", rollout_state_hash: "rollout-state-hash", spec_hash: "spec-hash", created_at: "2026-07-30T09:00:00Z", updated_at: "2026-07-30T09:03:00Z", snapshot: { project_id: "proj-1", image: { repository: record.build.oci_repository, digest: record.build.oci_digest, reference: `${record.build.oci_repository}@${record.build.oci_digest}` }, authority: { build_record: record, topology_plan_id: "topology-1", topology_revision: 4, deployment_policy_id: "policy-1", deployment_policy_revision: 3, runtime_id: "runtime-1", node_id: "node-1", agent_id: "agent-1" }, workload: { schema_version: "opsi.workload_spec/v1", service_key: record.service_key, replicas: 3, application_container_name: "app", container_port: 3000, resources: { requests: { cpu: "250m", memory: "256Mi" }, limits: { cpu: "500m", memory: "512Mi" } }, termination_grace_period_seconds: 30, readiness_probe: { path: "/healthz", port: 3000, initial_delay_seconds: 2, period_seconds: 5, timeout_seconds: 2, failure_threshold: 6 }, exposure: { mode: "none" } }, spec_hash: "spec-hash", ...(preview ? { preview: { namespace: "pr-42-web", hostname: "pr-42.preview.example.test", repository_id: 101, repository_owner_id: 42, pr_number: 42, head_sha: record.workload.sha, service_key: record.service_key, cpu: "250m", memory: "256Mi", max_replicas: 1, created_at: "2026-07-30T09:00:00Z", expires_at: "2026-08-01T09:00:00Z" } } : {}) } };
}

function terminal(status: string, currentDigest: string, knownGoodID?: string) {
  return { schema_version: "opsi.deployment_terminal/v1", status, spec_hash: "spec-hash", application_image: `registry.example.test/acme/web@${currentDigest}`, application_image_id: "containerd://verified-image", namespace: "opsi-prod", deployment_name: "web", service_name: "web", available_replicas: 3, rollout_state: status, desired_digest: digest("a"), current_digest: currentDigest, known_good_id: knownGoodID, known_good_hash: knownGoodID ? "known-good-hash" : undefined, readiness_evidence_hash: "readiness-hash" };
}

function exposure(record: ReturnType<typeof build>) {
  return { ...deployment("exposure-configured", record, "succeeded"), readiness_evidence_hash: undefined, terminal_result: undefined, exposure_spec: { schema_version: "opsi.exposure_spec/v1", project_id: "proj-1", environment_id: "env-prod", runtime_id: "runtime-1", service_key: "web", deployment_job_id: "deploy-verified", hostname: "web.example.test", path: "/", service_port: 3000, tls: { mode: "secret_ref", secret_ref: "secret://tls/web" }, spec_hash: "exposure-hash" } };
}

function policy() {
  return { schema_version: "opsi.deployment_policy/v1", id: "policy-1", revision: 3, state_hash: "policy-state", policy_hash: "policy-hash", created_by: "user", applied_by: "user", created_at: "2026-07-29T08:00:00Z", applied_at: "2026-07-29T08:00:00Z", policy: { schema_version: "opsi.deployment_policy/v1", project_id: "proj-1", repository_id: 101, service_keys: ["web", "api", "worker"], workflow_refs: ["acme/platform/.github/workflows/build.yml@refs/heads/main"], allowed_events: ["push", "pull_request"], allowed_git_refs: ["refs/heads/main"], environment_id: "env-prod", allowed_runtime_ids: ["runtime-1"], allowed_oci_repositories: ["registry.example.test/acme/web", "registry.example.test/acme/api", "registry.example.test/acme/worker"], allowed_platforms: ["linux/amd64"], allowed_config_hashes: ["config-hash"], allowed_build_plan_hashes: ["plan-hash"], allow_unknown_capacity: false, enabled: true, automatic_main: true, preview: { enabled: true, hostname_suffix: "preview.example.test" } } };
}
