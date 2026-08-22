import { expect, test, type Page, type Route } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

type FixtureBinding = { kind: "internal_http" | "browser_http"; target_service_id: string; target_service_key: string; env_prefix?: string; env_name?: string; path?: string };
type FixtureConfiguration = { schema_version: "opsi.service_configuration/v1"; revision?: number; state_hash?: string; environment?: Array<{ name: string; value: string }>; public_route?: { hostname: string; path: string }; bindings?: FixtureBinding[] };
type FixtureService = { id: string; name: string; type: string; status: string; source_type: string; replicas?: number; container_port: number; configuration: FixtureConfiguration };

test.beforeEach(async ({ page }) => { watchConsoleErrors(page); await page.route("**/api/local/**", (route) => respond(route)); });
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Design renders applied placement, unplaced applications, factual servers, and URL selection", async ({ page }) => {
  await page.goto("/?project=proj-1&view=topology");
  await expect(page.locator(".breadcrumb")).toHaveText("Projects/Checkout Platform/Production/Topology");
  await expect(page.getByRole("button", { name: /Server Primary runtime, Ready, Agent active/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application api, Assigned, unchanged/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application worker, Assigned, unchanged/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application reports, Unplaced, unchanged/ })).toBeVisible();
  await expect(page.getByText("4 cores", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("8192 MiB", { exact: true }).first()).toBeVisible();
  const viewport = page.locator(".react-flow__viewport");
  const transform = await viewport.evaluate((element) => { element.setAttribute("data-remount-probe", "stable"); return getComputedStyle(element).transform; });
  await page.getByRole("button", { name: /Application worker, Assigned, unchanged/ }).click();
  await expect(viewport).toHaveAttribute("data-remount-probe", "stable");
  await expect.poll(() => viewport.evaluate((element) => getComputedStyle(element).transform)).toBe(transform);
  await page.getByRole("button", { name: /Application api, Assigned, unchanged/ }).press("Enter");
  await expect(page).toHaveURL(/topology=service%3Aapi/);
  await expect(page.getByRole("heading", { name: "api", exact: true })).toBeFocused();
  await expect(page.getByRole("button", { name: "Design", exact: true })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Live", exact: true }).click();
  const liveCanvas = page.getByLabel("Read-only factual topology canvas");
  const liveServer = liveCanvas.locator('.topologyResourceNode[data-resource-mode="live"][data-resource-kind="server"]').filter({ hasText: "Primary runtime" });
  await expect(liveServer).toContainText("Primary runtime");
  await expect(liveServer).toContainText("Ready");
  await expect(liveServer).toContainText("agent-primary · active");
  await expect(liveServer).toContainText("node-primary · healthy");
  await expect(page.locator(".liveDeploymentList").getByText("dep-1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Design", exact: true }).click();
  await expect(page.getByRole("link", { name: "Support", exact: true })).toHaveCount(0);
  await page.goto("/?project=proj-1&view=infrastructure&tab=runtimes");
  await page.getByRole("button", { name: /Edge runtime/ }).click();
  await expect(page).toHaveURL(/runtime=runtime-edge/);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Edge runtime" })).toBeVisible();
});

test("Topology keeps the primary action and canvas above the fold without horizontal overflow", async ({ page }) => {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1280, height: 800 }, { width: 1024, height: 768 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=topology");
    const action = page.locator(".topologyPrimaryAction");
    const canvas = page.locator(".topologyCanvas");
    const [actionBox, canvasBox] = await Promise.all([action.boundingBox(), canvas.boundingBox()]);
    expect(actionBox).not.toBeNull();
    expect(canvasBox).not.toBeNull();
    expect(actionBox!.y).toBeLessThan(viewport.height);
    expect(canvasBox!.y).toBeLessThan(viewport.height);
    await expect(page.locator("html")).toHaveJSProperty("scrollWidth", viewport.width);
  }
});

test("Deployment Review stays collapsed until placement or unpublished canvas changes exist", async ({ page }) => {
  await page.unroute("**/api/local/**");
  const data = fixture();
  data.topology = { ...data.topology, assignments: [] };
  await page.route("**/api/local/**", (route) => respondWithData(route, data, "proj-1"));

  await page.goto("/?project=proj-1&view=topology");
  await expect(page.getByRole("heading", { name: /Review deployment/i })).toHaveCount(0);
  await expect(page.locator(".topologyDeploymentHint")).toBeVisible();

  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await expect(page.getByRole("heading", { name: /Review deployment/i })).toBeVisible();
});

test("Design moves an applied application through Unplaced without a backend write", async ({ page }) => {
  let applyRequests = 0;
  page.on("request", (request) => { if (new URL(request.url()).pathname.endsWith("/topology/apply")) applyRequests += 1; });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");

  await dragNode(page, /Application api, Assigned, unchanged/, /Unplaced applications, 1 applications/);
  await expect(page.getByRole("button", { name: /Application api, Unplaced, pending removal/ })).toBeVisible();
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();

  await dragNode(page, /Application api, Unplaced, pending removal/, /Server Primary runtime/);
  await expect(page.getByRole("button", { name: /Application api, Assigned, unchanged/ })).toBeVisible();
  await expect(page.getByText("0 unpublished changes", { exact: true })).toBeVisible();
  expect(applyRequests).toBe(0);
});

test("Design edits resources, reviews through Cloud, survives Live, and Reset avoids apply", async ({ page }) => {
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  const reviewRequests: string[] = [];
  let applyRequests = 0;
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (/\/topology\/(plan|validate|diff)$/.test(path)) reviewRequests.push(path.split("/").at(-1) ?? "");
    if (path.endsWith("/topology/apply")) applyRequests += 1;
  });

  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application reports, Assigned, new placement/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "reports", exact: true })).toBeFocused();
  await expect(page.getByLabel("Replicas")).toHaveValue("1");
  await expect(page.getByLabel("CPU request (millicores)")).toHaveValue("100");
  await expect(page.getByLabel("Memory (MiB)")).toHaveValue("128");
  await expect(page.getByLabel("Exposure")).toHaveValue("none");
  await page.getByLabel("Replicas").fill("3");
  await page.getByLabel("CPU request (millicores)").fill("350");
  await page.getByLabel("Memory (MiB)").fill("512");
  await page.getByLabel("Exposure").selectOption("public");

  await page.getByRole("button", { name: "Review draft" }).click();
  const cloudReview = page.getByLabel("Cloud topology review");
  await expect(page.getByRole("heading", { name: "Cloud topology review" })).toBeVisible();
  await expect(cloudReview.getByText("Cloud semantic diff", { exact: true })).toBeVisible();
  await expect(cloudReview.getByText("reports", { exact: true })).toBeVisible();
  await expect(page.getByText("Requested 1550m / 2048 MiB", { exact: false })).toContainText("Available 4000m CPU / 8192 MiB memory");
  await expect(cloudReview.getByText("topology-state", { exact: true })).toBeVisible();
  await expect(cloudReview.getByText("proposal-hash", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Apply topology" })).toBeEnabled();
  expect(reviewRequests).toEqual(["plan", "validate", "diff"]);
  await page.getByRole("button", { name: "Live", exact: true }).click();
  await page.getByRole("button", { name: "Design", exact: true }).click();
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  expect(applyRequests).toBe(0);

  await page.getByRole("button", { name: "Reset changes" }).click();
  await expect(page.getByText("0 unpublished changes", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application reports, Unplaced, unchanged/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application worker, Assigned, unchanged/ })).toBeVisible();
});

test("Application edges review internal and same-origin browser HTTP without creating deployment state", async ({ page }) => {
	await page.unroute("**/api/local/**");
	const data = fixture();
	const applyBodies: Array<Record<string, unknown>> = [];
	await page.route("**/api/local/**", async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path.endsWith("/configuration/apply")) applyBodies.push(route.request().postDataJSON() as Record<string, unknown>);
		await respondWithData(route, data, "proj-1");
	});
	await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
	await connectApplications(page, /Application api, Assigned, unchanged/, /Application worker, Assigned, unchanged/);
	await expect(page.getByRole("heading", { name: "HTTP connection" })).toBeVisible();
	await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
	await expect(page.getByLabel("Runtime intent")).toHaveValue("internal_http");
	await page.getByLabel("Environment prefix").fill("BACKEND");
	await page.getByRole("button", { name: "Review connection" }).click();
	await expect(page.getByLabel("Cloud service configuration review").getByText("generated environment", { exact: true }).first()).toBeVisible();
	await page.getByRole("button", { name: "Apply service configuration" }).click();
	await expect(page.getByText("0 unpublished changes", { exact: true })).toBeVisible();
	await expect(page.getByText("Service configuration applied for api.", { exact: true })).toBeVisible();
	expect(data.deployments).toHaveLength(1);

	await page.locator(".react-flow__edge").first().click({ force: true });
	await page.getByLabel("Runtime intent").selectOption("browser_http");
	await expect(page.getByLabel("Same-origin path")).toHaveValue("/api");
	await expect(page.getByLabel("Environment name")).toHaveValue("WORKER_BASE_URL");
	await page.getByRole("button", { name: "Review connection" }).click();
	await page.getByRole("button", { name: "Apply service configuration" }).click();
	await expect(page.getByText("Service configuration applied for api.", { exact: true })).toBeVisible();
	const appliedDraft = (applyBodies.at(-1)?.draft ?? {}) as { bindings?: Array<Record<string, unknown>> };
	expect(appliedDraft.bindings?.[0]).toMatchObject({ kind: "browser_http", path: "/api", env_name: "WORKER_BASE_URL" });
	expect(data.deployments).toHaveLength(1);

	await page.locator(".react-flow__edge").first().click({ force: true });
	await page.getByRole("button", { name: "Remove connection" }).click();
	await expect(page.getByText("Browser · pending removal", { exact: true })).toBeVisible();
});

test("Design draft clears when the project changes", async ({ page }) => {
  await page.unroute("**/api/local/**");
  const projects = fixture().projects.concat({ id: "proj-2", org_id: "org-1", name: "Second Project", slug: "second", status: "ready" });
  await page.route("**/api/local/**", async (route) => {
    const projectID = new URL(route.request().url()).pathname.includes("/proj-2/") ? "proj-2" : "proj-1";
    const data = fixture();
    data.projects = projects;
    await respondWithData(route, data, projectID);
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: /Second Project/ }).click();
  await expect(page.locator(".breadcrumb")).toContainText("Second Project");
  await expect(page.getByText("0 unpublished changes", { exact: true })).toBeVisible();
});

test("Design draft survives an applied topology refresh", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let stateHash = "topology-state";
  await page.route("**/api/local/**", async (route) => {
    const data = fixture();
    data.facts.agents = data.facts.agents.map((agent) => ({ ...agent, status: "offline" }));
    data.topology = { ...data.topology, state_hash: stateHash };
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review draft" }).click();
  await expect(page.getByRole("heading", { name: "Cloud topology review" })).toBeVisible();
  stateHash = "topology-state-changed";
  await page.getByRole("button", { name: "Refresh current data" }).click();
  await expect(page.getByText("Topology changed. Review draft again.", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Cloud topology review" })).toHaveCount(0);
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application reports, Assigned, new placement/ })).toBeVisible();
});

test("Cloud validation issues disable apply", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let applyRequests = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/topology/validate")) {
      const { draft } = route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] };
      await route.fulfill({ body: JSON.stringify(topologyValidation(draft, false)), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/topology/apply")) applyRequests += 1;
    await respondWithData(route, fixture(), "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await page.getByRole("button", { name: "Review draft" }).click();
  await expect(page.getByText("Cloud validation failed", { exact: true })).toBeVisible();
  await expect(page.getByText(/reports \/ runtime-primary: Requested capacity exceeds available capacity/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Apply topology" })).toBeDisabled();
  expect(applyRequests).toBe(0);
});

test("Apply sends reviewed authority, refreshes facts, and clears only the matching proposal", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let appliedPlan: ReturnType<typeof fixture>["topology"] | undefined;
  let reviewedDraft: ReturnType<typeof fixture>["topology"] | undefined;
  let applyBody: Record<string, unknown> | undefined;
  let idempotencyKey = "";
  let topologyReads = 0;
  let factsReads = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/topology/plan")) {
      reviewedDraft = (route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] }).draft;
      await route.fulfill({ body: JSON.stringify({ draft: reviewedDraft, plan_hash: "proposal-hash", state_hash: "topology-state" }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/topology/apply")) {
      const request = route.request().postDataJSON() as Record<string, unknown>;
      applyBody = request;
      idempotencyKey = route.request().headers()["idempotency-key"] ?? "";
      appliedPlan = appliedTopology(request.draft as ReturnType<typeof fixture>["topology"]);
      await route.fulfill({ body: JSON.stringify({ plan: appliedPlan, reused: true }), contentType: "application/json", status: 200 });
      return;
    }
    const data = fixture();
    if (appliedPlan) data.topology = appliedPlan;
    if (path.endsWith("/topology")) topologyReads += 1;
    if (path.endsWith("/topology/facts")) factsReads += 1;
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await page.getByRole("button", { name: "Review draft" }).click();
  const readsBeforeApply = { topology: topologyReads, facts: factsReads };
  await page.getByRole("button", { name: "Apply topology" }).click();
  await expect(page.getByText("TopologyPlan r5", { exact: true })).toBeVisible();
  await expect(page.getByText("0 unpublished changes", { exact: true })).toBeVisible();
  await expect(page.getByText(/idempotent replay/)).toBeVisible();
  expect(applyBody?.expected_revision).toBe(4);
  expect(applyBody?.expected_state_hash).toBe("topology-state");
  expect(applyBody?.draft).toEqual(reviewedDraft);
  expect((applyBody?.draft as ReturnType<typeof fixture>["topology"]).assignments.map((assignment) => assignment.service_key)).toEqual(["api", "reports", "worker"]);
  expect(idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
  expect(topologyReads).toBeGreaterThan(readsBeforeApply.topology);
  expect(factsReads).toBeGreaterThan(readsBeforeApply.facts);
});

test("State conflict refreshes once, preserves local edits, and requires review again", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let conflicted = false;
  let applyRequests = 0;
  let topologyReads = 0;
  let factsReads = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/topology/apply")) {
      applyRequests += 1;
      conflicted = true;
      await route.fulfill({ body: JSON.stringify({ error: { code: "TOPOLOGY_STATE_CONFLICT", message: "topology changed" } }), contentType: "application/json", status: 409 });
      return;
    }
    const data = fixture();
    if (conflicted) data.topology = { ...data.topology, revision: 5, state_hash: "topology-state-5" };
    if (path.endsWith("/topology")) topologyReads += 1;
    if (path.endsWith("/topology/facts")) factsReads += 1;
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await dragNode(page, /Application reports, Unplaced, unchanged/, /Server Primary runtime/);
  await page.getByRole("button", { name: "Review draft" }).click();
  const readsBeforeApply = { topology: topologyReads, facts: factsReads };
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/topology/apply", status: 409, method: "POST" });
  await page.getByRole("button", { name: "Apply topology" }).click();
  await expect(page.getByText("Topology changed. Review draft again.", { exact: true })).toBeVisible();
  await expect(page.getByText("TopologyPlan r5", { exact: true })).toBeVisible();
  await expect(page.getByText("1 unpublished change", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Application reports, Assigned, new placement/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "Apply topology" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Review draft" })).toBeEnabled();
  expect(applyRequests).toBe(1);
  expect(topologyReads).toBeGreaterThan(readsBeforeApply.topology);
  expect(factsReads).toBeGreaterThan(readsBeforeApply.facts);
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
    await expect(page.locator(".topologyContextBar")).toHaveAttribute("data-state", state);
    const button = page.getByRole("button", { name: action, exact: true });
    await expect(button).toBeVisible();
    if (next === "application") {
      const onboardingBox = await page.locator(".topologyContextBar").boundingBox();
      const actionBox = await button.boundingBox();
      const canvasBox = await page.locator(".topologyFlow").boundingBox();
      expect(onboardingBox).not.toBeNull();
      expect(actionBox).not.toBeNull();
      expect(canvasBox).not.toBeNull();
      expect(onboardingBox!.y).toBeLessThan(canvasBox!.y);
      expect(actionBox!.y).toBeLessThan(onboardingBox!.y + onboardingBox!.height / 2);
    }
    if (next === "connect") { await button.click(); await expect(page.getByRole("dialog", { name: "Connect Server" })).toBeVisible(); await page.getByRole("button", { name: "Close connect server dialog" }).click(); }
    if (next === "bootstrap") { await expect(page.locator(".topologyContextBar").getByText(/50% · preflight/)).toBeVisible(); await button.click(); await expect(page).toHaveURL(/tab=bootstrap/); await expect(page.locator(".bootstrapDetail h2").filter({ hasText: "203.0.113.10" })).toBeVisible(); }
    if (next === "failed") { await button.click(); await expect(page.getByRole("dialog", { name: /retry bootstrap session/i })).toBeVisible(); await page.getByRole("button", { name: "Confirm and submit" }).click(); await expect(page.getByText(/Bootstrap boot-failed returned status pending/)).toBeVisible(); await page.getByRole("button", { name: "Close" }).click(); }
    if (next === "application") { await button.click(); await expect(page.getByRole("dialog", { name: "Add application" })).toBeVisible(); await page.getByRole("button", { name: "Close application wizard" }).click(); }
    if (next === "placement") { await button.click(); await expect(page.getByRole("dialog", { name: "Plan placement" })).toBeVisible(); await page.getByRole("button", { name: "Close placement dialog" }).click(); }
    if (next === "review") { await button.click(); await expect(page.getByRole("button", { name: "Design", exact: true })).toHaveAttribute("aria-pressed", "true"); }
  }
});

test("Topology polling moves an active bootstrap to ready and moves bootstrap history to details", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let sessionReads = 0;
  let ready = false;
  const events = Array.from({ length: 7 }, (_, index) => ({ id: `event-${index}`, step: `step-${index}`, message_redacted: `Event ${index}`, progress_percent: index * 10, created_at: `2026-07-30T08:${String(index).padStart(2, "0")}:00Z` }));
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/bootstrap-sessions") && route.request().method() === "GET") {
      sessionReads += 1;
      if (sessionReads > 1) await new Promise((resolve) => setTimeout(resolve, 250));
      const session = { id: "boot-1", status: ready ? "succeeded" : "installing", public_host: "203.0.113.10", role: "first_server", checkpoint: { plan_version: "v1", next_step_index: ready ? 4 : 2, last_completed_step: ready ? "agent_ready" : "preflight" }, created_at: "2026-07-30T08:10:00Z" };
      await route.fulfill({ body: JSON.stringify({ sessions: [session] }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/bootstrap-sessions/boot-1/events")) { await route.fulfill({ body: JSON.stringify(events), contentType: "application/json", status: 200 }); return; }
    const data = onboardingFixture(fixture(), ready ? "application" : "bootstrap");
    data.sessions = [{ id: "boot-1", status: ready ? "succeeded" : "installing", public_host: "203.0.113.10", role: "first_server", created_at: "2026-07-30T08:10:00Z" }];
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=topology");
  await expect(page.locator(".topologyContextBar").getByText("Server Bootstrapping", { exact: true })).toBeVisible();
  ready = true;
  await expect(page.locator(".topologyContextBar").getByText("Server Ready", { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(page.getByRole("button", { name: "Add application" })).toBeVisible();
  await expect(page.locator(".topologyWorkspace").getByText("Recent Events", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "View server details" }).click();
  await expect(page).toHaveURL(/tab=bootstrap/);
  await expect(page.locator(".bootstrapSessionDetail .eventTimeline li")).toHaveCount(7);
});

test("Ready factual server does not keep stale bootstrap as its primary session or poll it", async ({ page }) => {
  await page.unroute("**/api/local/**");
  let sessionReads = 0;
  let eventReads = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/bootstrap-sessions") && route.request().method() === "GET") sessionReads += 1;
    if (path.endsWith("/bootstrap-sessions/boot-1/events")) eventReads += 1;
    await respondWithData(route, fixture(), "proj-1");
  });
  await page.goto("/?project=proj-1&view=topology");
  await expect(page.locator(".topologyContextBar").getByText("Server Ready", { exact: true })).toBeVisible();
  await expect(page.locator(".topologyWorkspace").getByText("Recent Events", { exact: true })).toHaveCount(0);
  await page.waitForTimeout(4_300);
  expect(sessionReads).toBe(1);
  expect(eventReads).toBe(1);
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
  await page.goto("/?project=proj-1&view=topology");
  await expect(page.locator(".topologyContextBar").getByText("Server Bootstrapping", { exact: true })).toBeVisible();
  await expect.poll(() => projectOneReads, { timeout: 10_000 }).toBeGreaterThanOrEqual(3);
  expect(maxActiveRequests).toBe(1);
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
  let claimed = false;
  let bound = false;
  let navigationRequests = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/github/installations")) {
      await route.fulfill({ body: JSON.stringify({ installations: [{ installation_id: 11, account_login: "example", status: "active" }] }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/github/repositories") && route.request().method() === "GET") {
      await route.fulfill({ body: JSON.stringify({ repositories: [{ repository_id: 101, installation_id: 11, full_name: "example/api", default_branch: "main", status: "active", claim_status: claimed ? "active" : "available" }] }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/github/repositories/101/claim")) {
      claimed = true;
      await route.fulfill({ body: JSON.stringify({ repository_id: 101, project_id: "proj-1", status: "active" }), contentType: "application/json", status: 200 });
      return;
    }
    if (path.endsWith("/services") && route.request().method() === "POST") {
      created = true;
      await route.fulfill({ body: JSON.stringify({ id: "api", name: "api", type: "application", status: "ready", source_type: "git", repo_url: "https://github.com/example/api", branch: "main", build_context: ".", dockerfile: "Dockerfile", container_port: 8080, health_path: "/health" }), contentType: "application/json", status: 201 });
      return;
    }
    if (path.endsWith("/github/bindings") && route.request().method() === "POST") {
      bound = true;
      await route.fulfill({ body: JSON.stringify({ id: "binding-api", project_id: "proj-1", service_id: "api", repository_id: 101, installation_id: 11, service_key: "api", config_path: ".opsi/opsi-cd.yaml", status: "active" }), contentType: "application/json", status: 201 });
      return;
    }
    if (path.endsWith("/github/bindings")) {
      await route.fulfill({ body: JSON.stringify({ bindings: bound ? [{ id: "binding-api", project_id: "proj-1", service_id: "api", repository_id: 101, installation_id: 11, service_key: "api", config_path: ".opsi/opsi-cd.yaml", status: "active" }] : [] }), contentType: "application/json", status: 200 });
      return;
    }
    const data = onboardingFixture(fixture(), created ? "placement" : "application");
    await respondWithData(route, data, "proj-1");
  });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  page.on("request", (request) => { if (request.isNavigationRequest()) navigationRequests += 1; });
  await page.getByRole("button", { name: "Add application" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Application name").fill("api");
  await page.getByRole("button", { name: "Review application" }).click();
  const review = page.getByRole("dialog", { name: "create application" });
  await review.getByRole("button", { name: "Confirm and submit" }).click();
  await review.getByRole("button", { name: "Close" }).click();
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
  await expect(page.locator(".topologyFlow")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.locator(".topologyFlow")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-fe03/infrastructure-mobile-390x844.png" });
  await page.setViewportSize({ width: 320, height: 800 });
  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await expect(page.locator(".topologyFlow")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
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
  else if (path.endsWith("/nodes")) body = { nodes: data.nodes };
  else if (/\/nodes\/[^/]+$/.test(path)) body = { node: data.nodes.find((item) => path.endsWith(item.id)), open_bootstrap_events: data.bootstrapEvents, recent_deployment_jobs: data.deployments };
	else if (/\/services\/[^/]+\/configuration\/(preview|validate|diff|apply)$/.test(path)) {
		const serviceID = path.split("/").at(-3) ?? "";
		const service = data.services.find((item) => item.id === serviceID);
		const action = path.split("/").at(-1);
		const request = route.request().postDataJSON() as Record<string, unknown>;
		const draft = ((action === "apply" ? request.draft : request) ?? service?.configuration) as FixtureConfiguration | undefined;
		const generated = (draft?.bindings ?? []).flatMap((binding, index) => binding.kind === "internal_http" ? [`${binding.env_prefix}_HOST`, `${binding.env_prefix}_PORT`, `${binding.env_prefix}_URL`].map((name) => ({ name, value: name.endsWith("_PORT") ? "9000" : "generated", binding: index })) : [{ name: binding.env_name ?? "", value: binding.path ?? "/api", binding: index }]);
		if (action === "preview") body = { configuration: draft, generated_environment: generated, current_revision: service?.configuration.revision ?? 0, current_state_hash: service?.configuration.state_hash ?? "empty", draft_state_hash: "configuration-draft-hash" };
		if (action === "validate") body = { valid: true, issues: [] };
		if (action === "diff") body = { changes: [{ kind: "connection", action: "change", name: serviceID }, ...generated.map((item) => ({ kind: "generated_environment", action: "set", name: item.name, after: item.value }))] };
		if (action === "apply" && service && draft) { service.configuration = { ...draft, schema_version: "opsi.service_configuration/v1", revision: (service.configuration.revision ?? 0) + 1, state_hash: "configuration-draft-hash" }; body = { configuration: service.configuration, reused: false }; }
	}
	else if (/\/services\/[^/]+\/configuration$/.test(path)) body = data.services.find((item) => path.includes(`/${item.id}/`))?.configuration;
	else if (path.endsWith("/services")) body = { services: data.services };
  else if (path.endsWith("/deployments")) body = { deployments: data.deployments };
  else if (/\/bootstrap-sessions\/[^/]+\/retry$/.test(path)) body = { ...data.sessions.find((session) => path.includes(session.id)), status: "pending" };
  else if (path.endsWith("/bootstrap-sessions")) body = { sessions: data.sessions };
  else if (/\/bootstrap-sessions\/[^/]+\/events$/.test(path)) body = data.bootstrapEvents;
  else if (path.endsWith("/audit")) body = { events: [] };
  else if (path.endsWith("/support")) body = data.support;
  else if (path.endsWith("/topology/facts")) body = data.facts;
  else if (path.endsWith("/topology/plan")) {
    const { draft } = route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] };
    body = { draft, plan_hash: "proposal-hash", state_hash: data.topology.state_hash };
  }
  else if (path.endsWith("/topology/validate")) {
    const { draft } = route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] };
    body = topologyValidation(draft, true);
  }
  else if (path.endsWith("/topology/diff")) {
    const { draft } = route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] };
    body = topologyDiff(data.topology, draft);
  }
  else if (path.endsWith("/topology/apply")) {
    const request = route.request().postDataJSON() as { draft: ReturnType<typeof fixture>["topology"] };
    body = { plan: appliedTopology(request.draft), reused: false };
  }
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

function topologyValidation(draft: ReturnType<typeof fixture>["topology"], valid: boolean) {
  const runtimes = [...new Set(draft.assignments.map((assignment) => assignment.runtime_id))].map((runtimeID) => {
    const assignments = draft.assignments.filter((assignment) => assignment.runtime_id === runtimeID);
    const requestedCPU = assignments.reduce((sum, assignment) => sum + assignment.replicas * assignment.cpu_request_millicores, 0);
    const requestedMemory = assignments.reduce((sum, assignment) => sum + assignment.replicas * assignment.memory_request_bytes, 0);
    return {
      runtime_id: runtimeID,
      eligible: valid,
      capacity: {
        runtime_id: runtimeID, source: "placement_facts", heartbeat_fresh: true,
        cpu_capacity_millicores: 4000, memory_capacity_bytes: 8192 * 1024 * 1024,
        reserved_cpu_millicores: 0, reserved_memory_bytes: 0, assigned_cpu_millicores: 0, assigned_memory_bytes: 0,
        requested_cpu_millicores: requestedCPU, requested_memory_bytes: requestedMemory,
        available_cpu_millicores: 4000, available_memory_bytes: 8192 * 1024 * 1024,
        unknown_capacity: false, unknown_capacity_policy_override: false, oversubscribed: !valid,
      },
      issues: valid ? [] : [{ code: "CAPACITY_EXCEEDED", message: "Requested capacity exceeds available capacity." }],
    };
  });
  return {
    schema_version: "opsi.topology_plan/v1", project_id: draft.project_id, plan_hash: "proposal-hash", valid, runtimes,
    issues: valid ? [] : [{ code: "CAPACITY_EXCEEDED", message: "Requested capacity exceeds available capacity.", service_key: "reports", runtime_id: "runtime-primary" }],
    validated_at: "2026-07-30T09:00:00Z",
  };
}

function topologyDiff(current: ReturnType<typeof fixture>["topology"], proposed: ReturnType<typeof fixture>["topology"]) {
  const before = new Map(current.assignments.map((assignment) => [assignment.service_key, assignment]));
  const after = new Map(proposed.assignments.map((assignment) => [assignment.service_key, assignment]));
  const changes = [...new Set([...before.keys(), ...after.keys()])].sort().flatMap((serviceKey) => {
    const previous = before.get(serviceKey);
    const next = after.get(serviceKey);
    if (JSON.stringify(previous) === JSON.stringify(next)) return [];
    return [{ service_key: serviceKey, change: previous ? next ? "updated" : "removed" : "added", ...(previous ? { before: previous } : {}), ...(next ? { after: next } : {}) }];
  });
  return { project_id: proposed.project_id, current_revision: current.revision, current_hash: current.state_hash, proposed_hash: "proposal-hash", changes };
}

function appliedTopology(draft: ReturnType<typeof fixture>["topology"]) {
  return { ...draft, id: "topo-1", revision: 5, state_hash: "topology-state-5", plan_hash: "proposal-hash", created_by: "owner", applied_by: "owner", created_at: "2026-07-30T08:00:00Z", applied_at: "2026-07-30T09:00:00Z" };
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
	const services: FixtureService[] = [{ id: "api", name: "api", type: "application", status: "ready", source_type: "image", replicas: 2, container_port: 8080, configuration: { schema_version: "opsi.service_configuration/v1", revision: 1, state_hash: "api-config", environment: [], public_route: { hostname: "apps.example.com", path: "/" }, bindings: [] } }, { id: "worker", name: "worker", type: "application", status: "ready", source_type: "image", replicas: 2, container_port: 9000, configuration: { schema_version: "opsi.service_configuration/v1", revision: 1, state_hash: "worker-config", environment: [], public_route: { hostname: "apps.example.com", path: "/api" }, bindings: [] } }, { id: "reports", name: "reports", type: "application", status: "ready", source_type: "image", container_port: 7000, configuration: { schema_version: "opsi.service_configuration/v1", revision: 0, state_hash: "reports-config", environment: [], bindings: [] } }];
  const nodes = [{ id: "node-primary", name: "Primary node", role: "server", status: "healthy", cpu_cores: 4, memory_mb: 8192, disk_total_gb: 80, k3s_status: "ready", agent_id: "agent-primary", agent_version: "1.8.0", last_seen_at: "2026-07-30T09:00:00Z" }, { id: "node-edge", name: "Edge node", role: "worker", status: "stale", k3s_status: "ready", agent_id: "agent-stale", agent_version: "1.7.4", last_seen_at: "2026-07-30T08:20:00Z" }];
  const facts = { project_id: "proj-1", environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-primary", project_id: "proj-1", environment_id: "env-prod", name: "Primary runtime", type: "k3s", status: "ready" }, { id: "runtime-edge", project_id: "proj-1", environment_id: "env-prod", name: "Edge runtime", type: "k3s", status: "degraded" }], nodes: [{ id: "node-primary", project_id: "proj-1", runtime_id: "runtime-primary", status: "healthy", cpu_cores: 4, memory_mb: 8192, last_seen_at: nodes[0].last_seen_at }, { id: "node-edge", project_id: "proj-1", runtime_id: "runtime-edge", status: "stale", last_seen_at: nodes[1].last_seen_at }], agents: [{ id: "agent-primary", project_id: "proj-1", runtime_id: "runtime-primary", node_id: "node-primary", status: "active", capabilities: { deploy: true }, last_seen_at: nodes[0].last_seen_at }, { id: "agent-stale", project_id: "proj-1", runtime_id: "runtime-edge", node_id: "node-edge", status: "stale", capabilities: { deploy: true }, last_seen_at: nodes[1].last_seen_at }], services: services.map((item) => ({ id: item.id, project_id: "proj-1", key: item.name })) };
  const topology = { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: "proj-1", revision: 4, state_hash: "topology-state", plan_hash: "topology-plan", created_by: "owner", applied_by: "owner", created_at: "2026-07-30T08:00:00Z", applied_at: "2026-07-30T08:00:00Z", assignments: [{ service_key: "api", environment_id: "env-prod", runtime_id: "runtime-primary", replicas: 2, cpu_request_millicores: 250, memory_request_bytes: 268435456, exposure: { mode: "none" } }, { service_key: "worker", environment_id: "env-prod", runtime_id: "runtime-edge", replicas: 2, cpu_request_millicores: 200, memory_request_bytes: 268435456, exposure: { mode: "internal" } }] };
  const incidents = [{ incident_id: "inc-1", project_id: "proj-1", service_id: "worker", node_id: "node-edge", pod_id: "worker-7", status: "open", severity: "warning", anomaly_type: "readiness", created_at_unix: 1785290100 }];
  return { projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }], services, nodes, facts, topology, telemetry: [{ service_id: "api", health: "healthy", pod_count: 2, ready_pods: 2, cpu_cores: 0.4, memory_bytes: 268435456, restart_count: 0, recent_error_count: 0, last_seen_unix: 1785290900 }, { service_id: "worker", health: "degraded", pod_count: 2, ready_pods: 1, cpu_cores: 0.2, memory_bytes: 201326592, restart_count: 3, recent_error_count: 1, last_seen_unix: 1785290800 }], incidents, logs: [{ service_id: "worker", pod_id: "worker-7", namespace: "opsi-prod", level: "error", message: "request timeout after 30s", fingerprint: "fp-timeout", observed_unix: 1785290800 }, { service_id: "api", pod_id: "api-9", namespace: "opsi-prod", level: "warning", message: "<script>alert('x')</script> token=should-hide", fingerprint: "fp-untrusted", observed_unix: 1785290700 }], sessions: [{ id: "boot-1", status: "installing", public_host: "203.0.113.10", role: "worker", attempt_count: 1, max_attempts: 3, checkpoint: { plan_version: "v1", next_step_index: 2, last_completed_step: "preflight" }, created_at: "2026-07-30T08:10:00Z" }, { id: "boot-failed", status: "failed", public_host: "203.0.113.11", role: "worker", attempt_count: 3, max_attempts: 3, last_failure_code: "SSH_HOST_KEY_MISMATCH", last_failure_message_redacted: "Pinned host key did not match", created_at: "2026-07-30T07:00:00Z" }], bootstrapEvents: [{ id: "be-1", step: "preflight", message_redacted: "Host identity verified", progress_percent: 0, created_at: "2026-07-30T08:12:00Z" }, { id: "be-2", step: "installing", message_redacted: "Installing K3s and Agent", progress_percent: 0, created_at: "2026-07-30T08:14:00Z" }], deployments: [{ id: "dep-1", environment_id: "env-prod", service_id: "worker", status: "failed", created_at: "2026-07-30T08:00:00Z" }], support: { generated_at: "2026-07-30T09:00:00Z", readiness: { project_id: "proj-1", status: "degraded", can_deploy: true }, counts: { nodes: 2, healthy_nodes: 1, services: 3, deployment_jobs: 1, failed_deployments: 1, bootstrap_sessions: 2, open_bootstrap_jobs: 1, audit_events: 0 }, dashboard: { title: "Runtime evidence", datasource: "agent", refresh: "30s", panels: [{ id: "ordered", title: "Backend ordered samples", kind: "series", unit: "count", query: "agent.samples", series: [{ name: "worker", status: "degraded", value: 3, points: [1, 2, 3] }] }] }, signals: [{ name: "readiness", status: "warning", value: "3/4", target: "4/4" }], active_alerts: [{ id: "alert-1", severity: "warning", status: "active", title: "Worker readiness degraded", resource_id: "worker", runbook_id: "runbook-1" }], configured_alerts: [{ id: "rule-1", severity: "warning", title: "Readiness", metric: "ready_pods", runbook_id: "runbook-1" }], production_gates: [{ name: "runtime readiness", passed: false, detail: "worker has 1/2 ready pods" }], break_glass_policy: { time_limited: true, approval_required: true, reason_required: true, audited: true, secret_reveal_by_default: false, owner_notification: "required" }, runbooks: [{ id: "runbook-1", title: "Worker readiness", symptoms: "pod not ready", impact: "jobs delayed", dashboard_query: "worker", immediate_mitigation: "inspect rollout", long_term_fix: "fix readiness", customer_communication: "status page", escalation_path: "on-call" }], recent_request_ids: ["request-1"] }, evidence: { schema_version: "opsi.incident_evidence/v1", identity: incidents[0], generated_at_unix: 1785290900, observation_window: { start_unix: 1785290000, end_unix: 1785290900 }, deployment: { desired_digest: "sha256:desired", observed_digest: "sha256:observed" }, rollout: { rollout_id: "rollout-1", state: "failed", failure_code: "READINESS_FAILED", readiness_hash: "readiness-hash" }, timeline: [{ observed_at_unix: 1785290200, source: "kubernetes", kind: "readiness", detail: "probe failed", untrusted_content: true }], pods: [{ namespace: "opsi-prod", pod_id: "worker-7", node_id: "node-edge", ready_containers: 0, total_containers: 1, restart_count: 3 }], kubernetes_events: [], log_fingerprints: [{ fingerprint: "fp-timeout", level: "error", count: 3, first_observed_unix: 1785290200, last_observed_unix: 1785290800, excerpt: "request timeout", untrusted_content: true }], audit_references: [], coverage: [{ source: "rollout", status: "available", item_count: 1, truncated: false }, { source: "kubernetes", status: "partial", reason_code: "EVENT_LIMIT", item_count: 1, truncated: true }], truncations: [{ section: "kubernetes_events", omitted_items: 4, utf8_safe: true }], content_sha256: "content-hash-1" } };
}

async function dragNode(page: Page, sourceName: RegExp, targetName: RegExp) {
  const source = page.getByRole("button", { name: sourceName });
  const target = page.getByRole("button", { name: targetName });
  await expect(source).toBeVisible();
  await expect(target).toBeVisible();
  await source.scrollIntoViewIfNeeded();
  const sourceBox = await source.boundingBox();
  const targetBox = await target.boundingBox();
  if (!sourceBox || !targetBox) throw new Error("Canvas drag target is not measurable.");
  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + Math.min(210, targetBox.height - 30), { steps: 14 });
  await page.mouse.up();
}

async function connectApplications(page: Page, sourceName: RegExp, targetName: RegExp) {
	const source = page.getByRole("button", { name: sourceName });
	const target = page.getByRole("button", { name: targetName });
	await expect(source).toBeVisible();
	await expect(target).toBeVisible();
	await source.scrollIntoViewIfNeeded();
	await target.scrollIntoViewIfNeeded();
	const sourceHandle = source.locator(".react-flow__handle-right");
	const targetHandle = target.locator(".react-flow__handle-left");
	await expect(sourceHandle).toBeVisible();
	await expect(targetHandle).toBeVisible();
	const sourceBox = await sourceHandle.boundingBox();
	const targetBox = await targetHandle.boundingBox();
	if (!sourceBox || !targetBox) throw new Error("Application connection handles are not measurable.");
	await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
	await page.mouse.down();
	await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 12 });
	await page.mouse.up();
}
