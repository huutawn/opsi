import { expect, test, type Page, type Route } from "@playwright/test";
import { MAX_PROJECT_SUMMARY_REQUESTS } from "../hooks/console-state-support";
import { PROJECT_SUMMARY_TTL_MS } from "../lib/presentation/project";
import { collectedErrors, expectHTTPFailure, expectNoConsoleErrors, resetConsoleErrors, watchConsoleErrors } from "./console-errors";

type Scenario = "healthy" | "degraded" | "refreshed" | "unavailable" | "empty" | "long" | "failed-build";

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("workspace, grouped navigation, restoration, and back-forward behavior", async ({ page }) => {
  await mockLocalAPI(page, "healthy");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Home" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Overview" })).toHaveCount(0);
  await page.getByRole("button", { name: "Browse projects" }).click();
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("link", { name: /Checkout Platform/ })).toBeVisible();
  await expect(page.locator(".projectRow .status").first()).toHaveText("Healthy");
  await expect(page.locator(".projectRow").filter({ hasText: "Payments" }).locator(".status").first()).toHaveText("Degraded");
  await page.getByRole("link", { name: /Checkout Platform/ }).click();
  await expect(page).toHaveURL(/view=infrastructure&tab=topology/);
  await expect(page.locator(".breadcrumb")).toHaveText("Projects/Checkout Platform/Production/Topology");
  await expect(page.getByRole("link", { name: "Overview" })).toBeVisible();
  for (const destination of ["Topology", "Overview", "Services", "Delivery", "Observability", "Security"]) await expect(page.getByRole("link", { name: destination, exact: true })).toBeVisible();
  await expect(page.locator(".navSection a").first()).toHaveText("Topology");
  await expect(page.locator(".navSection a")).toHaveCount(6);
  await page.getByRole("button", { name: "Collapse sidebar" }).click();
  await expect(page.getByRole("button", { name: "Expand sidebar" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Switch project")).toBeVisible();
  await page.getByRole("button", { name: "Expand sidebar" }).click();

  await page.getByRole("link", { name: "Observability", exact: true }).click();
  await page.getByRole("tab", { name: "Health", exact: true }).focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("tab", { name: "Metrics", exact: true })).toBeFocused();
  await page.keyboard.press("Space");
  await expect(page).toHaveURL(/view=observability&tab=metrics/);
  await page.getByRole("tab", { name: "Logs", exact: true }).click();
  await expect(page).toHaveURL(/view=observability&tab=logs/);
  await page.reload();
  await expect(page.getByRole("tab", { name: "Logs", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("link", { name: "Security", exact: true }).click();
  await expect(page).toHaveURL(/view=security&tab=secrets/);
  await page.goBack();
  await expect(page.getByRole("tab", { name: "Logs", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.goForward();
  await expect(page.getByRole("tab", { name: "Secrets", exact: true })).toHaveAttribute("aria-selected", "true");
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
      await expect(page.locator(".statusStrip > div").nth(3).getByText("Unavailable", { exact: true })).toBeVisible();
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
  await page.evaluate(() => {
    window.history.pushState({}, "", "/?project=proj-2&view=services");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page).toHaveURL(/project=proj-2/);
  await expect(page.locator(".detailDrawer")).toHaveCount(0);
});

test("required viewports have no horizontal overflow and produce review screenshots", async ({ page }) => {
  await mockLocalAPI(page, "degraded");
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 390, height: 844 }, { width: 320, height: 800 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=overview");
    await expect(page.locator(".statusLead strong")).toHaveText("Degraded");
    if (viewport.width <= 390) {
      const menu = page.getByRole("button", { name: "Open navigation" });
      await menu.click();
      await page.keyboard.press("Escape");
      await expect(menu).toBeFocused();
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ fullPage: true, path: `../../.tmp/ui-fe01/overview-${viewport.width}x${viewport.height}.png` });
  }
});

for (const staleResult of ["success", "error"] as const) {
  test(`project switch is latest-wins after stale ${staleResult}`, async ({ page }) => {
    let releaseOld!: () => void;
    const oldGate = new Promise<void>((resolve) => { releaseOld = resolve; });
    const switches: string[] = [];
    let activeMutations = 0;
    let maxMutations = 0;
    await page.route("**/api/local/**", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/local/session/project") {
        const projectID = String(route.request().postDataJSON().project_id);
        switches.push(projectID);
        activeMutations += 1;
        maxMutations = Math.max(maxMutations, activeMutations);
        if (projectID === "proj-1") await oldGate;
        activeMutations -= 1;
        if (projectID === "proj-1" && staleResult === "error") {
          await route.fulfill({ body: JSON.stringify({ error: { code: "STALE_SWITCH", message: "obsolete A failed" } }), contentType: "application/json", status: 500 });
          return;
        }
      }
      await respond(route, "healthy");
    });
    await page.goto("/?view=projects");
    if (staleResult === "error") expectHTTPFailure(page, { path: "/api/local/session/project", status: 500, method: "POST" });
    await page.getByRole("link", { name: /Checkout Platform/ }).click();
    await expect.poll(() => switches).toEqual(["proj-1"]);
    await page.getByLabel("Switch project").click();
    await page.getByRole("link", { name: /Payments/ }).click();
    await expect(page).toHaveURL(/project=proj-2/);
    releaseOld();
    await expect(page.locator(".breadcrumb")).toContainText("Payments");
    await page.getByRole("link", { name: "Overview", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Payments" })).toBeVisible();
    await expect(page.getByRole("link", { name: /^payments-api / }).first()).toBeVisible();
    await expect(page.getByText("obsolete A failed", { exact: true })).toHaveCount(0);
    expect(switches).toEqual(["proj-1", "proj-2"]);
    expect(maxMutations).toBe(1);
  });
}

for (const staleRefresh of ["success", "error"] as const) {
  test(`old project background refresh cannot overwrite the new project after stale ${staleRefresh}`, async ({ page }) => {
    let releaseRefresh!: () => void;
    const refreshGate = new Promise<void>((resolve) => { releaseRefresh = resolve; });
    let projectADeployments = 0;
    let refreshWaiting = false;
    await page.route("**/api/local/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/local/projects/proj-1/deployments") {
        projectADeployments += 1;
        if (projectADeployments === 2) {
          refreshWaiting = true;
          await refreshGate;
          if (staleRefresh === "error") {
            await route.fulfill({ body: JSON.stringify({ error: { code: "OLD_REFRESH", message: "obsolete refresh failed" } }), contentType: "application/json", status: 500 });
            return;
          }
        }
      }
      await respond(route, "healthy");
    });
    await page.goto("/?project=proj-1&view=overview");
    if (staleRefresh === "error") expectHTTPFailure(page, { path: "/api/local/projects/proj-1/deployments", status: 500, method: "GET" });
    await expect(page.getByRole("heading", { name: "Checkout Platform" })).toBeVisible();
    await page.getByRole("button", { name: "Refresh project overview" }).click();
    await expect.poll(() => refreshWaiting).toBe(true);
    await page.getByLabel("Switch project").click();
    await page.getByRole("link", { name: /Payments/ }).click();
    await expect(page.locator(".breadcrumb")).toContainText("Payments");
    await page.getByRole("link", { name: "Overview", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Payments" })).toBeVisible();
    releaseRefresh();
    await expect(page.getByRole("link", { name: /^payments-api / }).first()).toBeVisible();
    await expect(page.getByText(/obsolete refresh failed/)).toHaveCount(0);
    await expect(page).toHaveURL(/project=proj-2/);
  });
}

test("project summaries are factual, bounded, searchable, filterable, and partially resilient", async ({ page }) => {
  let activeProjects = 0;
  let maxProjects = 0;
  let switchCalls = 0;
  await page.route("**/api/local/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/local/session/project") switchCalls += 1;
    if (/\/projects\/proj-[^/]+\/readiness$/.test(path)) {
      activeProjects += 1;
      maxProjects = Math.max(maxProjects, activeProjects);
      await new Promise((resolve) => setTimeout(resolve, 35));
      activeProjects -= 1;
      if (path.includes("proj-3")) {
        await route.fulfill({ body: JSON.stringify({ error: { code: "SUMMARY_UNAVAILABLE", message: "summary unavailable" } }), contentType: "application/json", status: 503 });
        return;
      }
    }
    await respond(route, "healthy");
  });
  expectHTTPFailure(page, { path: "/api/local/projects/proj-3/readiness", status: 503, method: "GET" });
  await page.goto("/?view=projects");
  const checkout = page.locator(".projectRow").filter({ hasText: "Checkout Platform" });
  const payments = page.locator(".projectRow").filter({ hasText: "Payments" });
  const analytics = page.locator(".projectRow").filter({ hasText: "Analytics" });
  await expect(checkout.getByText("Healthy", { exact: true }).first()).toBeVisible();
  await expect(payments.getByText("Degraded", { exact: true }).first()).toBeVisible();
  await expect(checkout.getByText("2", { exact: true })).toBeVisible();
  await expect(checkout.getByText(/abcdef012/)).toBeVisible();
  await expect(analytics.getByText("Unavailable", { exact: true }).first()).toBeVisible();
  expect(maxProjects).toBe(2);
  expect(switchCalls).toBe(0);
  const search = page.getByLabel("Search projects");
  await search.focus();
  await search.fill("payments");
  await expect(payments).toBeVisible();
  await expect(checkout).toHaveCount(0);
  await search.fill("");
  const filter = page.getByRole("combobox", { name: "Status" });
  await filter.focus();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(payments).toBeVisible();
  await expect(checkout).toHaveCount(0);
});

test("workspace project summaries reuse fresh cache and revalidate without discarding facts", async ({ page }) => {
  await page.addInitScript(() => {
    const clock = Date.now.bind(Date);
    let offset = 0;
    Object.assign(window, { advanceProjectSummaryClock: (milliseconds: number) => { offset += milliseconds; } });
    Date.now = () => clock() + offset;
  });
  let scenario: Scenario = "healthy";
  let includeAnalytics = true;
  let failCheckout = false;
  let holdCheckout = false;
  let releaseCheckout!: () => void;
  let checkoutWaiting = false;
  let proj2Readiness = 0;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/local/projects" && !includeAnalytics) {
      const projects = fixture("proj-1", scenario).projects.filter((project) => project.id !== "proj-3");
      await route.fulfill({ body: JSON.stringify({ projects }), contentType: "application/json", status: 200 });
      return;
    }
    if (path === "/api/local/projects/proj-2/readiness") proj2Readiness += 1;
    if (path === "/api/local/projects/proj-1/readiness" && holdCheckout) {
      checkoutWaiting = true;
      await new Promise<void>((resolve) => { releaseCheckout = resolve; });
      holdCheckout = false;
    }
    if (path === "/api/local/projects/proj-1/readiness" && failCheckout) {
      await route.fulfill({ body: JSON.stringify({ error: { message: "summary refresh unavailable" } }), contentType: "application/json", status: 503 });
      return;
    }
    await respond(route, scenario);
  });

  await page.goto("/?view=projects");
  const checkout = page.locator(".projectRow").filter({ hasText: "Checkout Platform" });
  await expect(checkout.locator('[data-label="Health"]').getByText("Healthy", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Services"]')).toHaveText("2");
  const freshProj2Readiness = proj2Readiness;

  await checkout.click();
  await expect(page.locator(".breadcrumb")).toContainText("Checkout Platform");
  await page.getByRole("link", { name: "Overview", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Checkout Platform" })).toBeVisible();
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: "Browse all projects", exact: true }).click();
  await expect(checkout.locator('[data-label="Health"]').getByText("Healthy", { exact: true })).toBeVisible();
  expect(proj2Readiness).toBe(freshProj2Readiness);

  scenario = "refreshed";
  holdCheckout = true;
  const search = page.getByLabel("Search projects");
  await search.fill("checkout");
  await search.focus();
  await page.getByRole("button", { name: "Refresh current data" }).evaluate((button: HTMLButtonElement) => button.click());
  await expect.poll(() => checkoutWaiting).toBe(true);
  await expect(checkout.locator('[data-label="Health"]').getByText("Healthy", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Services"]')).toHaveText("2");
  await expect(checkout.getByText("Refreshing", { exact: true })).toBeVisible();
  await expect(search).toBeFocused();
  await expect(search).toHaveValue("checkout");
  releaseCheckout();
  await expect(checkout.locator('[data-label="Health"]').getByText("Degraded", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Services"]')).toHaveText("3");
  await expect(checkout.locator('[data-label="Incidents"]')).toHaveText("1 open");

  includeAnalytics = false;
  failCheckout = true;
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/readiness", status: 503, method: "GET" });
  await page.getByRole("button", { name: "Refresh current data" }).click();
  await expect(checkout.getByText("Stale — retry Refresh current data", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Health"]').getByText("Degraded", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Services"]')).toHaveText("3");
  await expect(checkout.locator('[data-label="Incidents"]')).toHaveText("1 open");
  await search.fill("");
  await expect(page.getByText("Analytics", { exact: true })).toHaveCount(0);

  failCheckout = false;
  await checkout.click();
  await expect(page.locator(".breadcrumb")).toContainText("Checkout Platform");
  await page.getByRole("link", { name: "Overview", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Checkout Platform" })).toBeVisible();
  const beforeExpiry = proj2Readiness;
  await page.evaluate((ttl) => (window as unknown as { advanceProjectSummaryClock: (milliseconds: number) => void }).advanceProjectSummaryClock(ttl + 1), PROJECT_SUMMARY_TTL_MS);
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: "Browse all projects", exact: true }).click();
  await expect.poll(() => proj2Readiness).toBeGreaterThan(beforeExpiry);
});

test("obsolete workspace summary responses cannot overwrite a newer project operation", async ({ page }) => {
  let scenario: Scenario = "healthy";
  let readinessCalls = 0;
  let releaseOld!: () => void;
  let oldWaiting = false;
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const responseScenario = scenario;
    if (path === "/api/local/projects/proj-1/readiness") {
      readinessCalls += 1;
      if (readinessCalls === 2) {
        oldWaiting = true;
        await new Promise<void>((resolve) => { releaseOld = resolve; });
      }
    }
    await respond(route, responseScenario);
  });
  await page.goto("/?view=projects");
  const checkout = page.locator(".projectRow").filter({ hasText: "Checkout Platform" });
  await expect(checkout.locator('[data-label="Health"]').getByText("Healthy", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Refresh current data" }).click();
  await expect.poll(() => oldWaiting).toBe(true);
  scenario = "refreshed";
  await checkout.click();
  await expect(page.locator(".breadcrumb")).toContainText("Checkout Platform");
  await page.getByRole("link", { name: "Overview", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Checkout Platform" })).toBeVisible();
  await expect(page.locator(".statusLead strong")).toHaveText("Degraded");
  releaseOld();
  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: "Browse all projects", exact: true }).click();
  await expect(checkout.locator('[data-label="Health"]').getByText("Degraded", { exact: true })).toBeVisible();
  await expect(checkout.locator('[data-label="Services"]')).toHaveText("3");
});

test("shared project-summary limiter bounds every request and drains after telemetry failure", async ({ page }) => {
  const projectIDs = ["proj-1", "proj-2", "proj-3"];
  const servicesPerProject = 24;
  let active = 0;
  let maximum = 0;
  let completed = 0;
  let switchCalls = 0;
  const failedTelemetryPath = "/api/local/projects/proj-2/telemetry/services/svc-2-7";
  expectHTTPFailure(page, { path: `${failedTelemetryPath}?since_unix=0`, status: 503, method: "GET" });
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/local/session/project") switchCalls += 1;
    if (path === "/api/local/session") {
      await route.fulfill({ body: JSON.stringify({ authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", capabilities: [] }), contentType: "application/json", status: 200 });
      return;
    }
    if (path === "/api/local/projects") {
      await route.fulfill({ body: JSON.stringify({ projects: projectIDs.map((id, index) => ({ id, org_id: "org-1", name: `Project ${index + 1}`, slug: id, status: "ready" })) }), contentType: "application/json", status: 200 });
      return;
    }
    const match = path.match(/^\/api\/local\/projects\/proj-(\d+)\/(.+)$/);
    if (!match) {
      await route.fulfill({ body: "{}", contentType: "application/json", status: 200 });
      return;
    }
    active += 1;
    maximum = Math.max(maximum, active);
    await new Promise((resolve) => setTimeout(resolve, 12));
    const projectNumber = Number(match[1]);
    const suffix = match[2];
    let body: unknown = {};
    let status = 200;
    if (suffix === "readiness") body = { project_id: `proj-${projectNumber}`, status: "ready", can_deploy: true };
    else if (suffix === "services") body = { services: Array.from({ length: servicesPerProject }, (_, index) => ({ id: `svc-${projectNumber}-${index}`, name: `service-${index}`, type: "application", status: "ready", source_type: "image", replicas: 1 })) };
    else if (suffix === "deployments") body = { deployments: [] };
    else if (suffix === "topology/facts") body = { project_id: `proj-${projectNumber}`, environments: [], runtimes: [], nodes: [], agents: [], services: [] };
    else if (suffix === "topology") body = { assignments: [] };
    else if (suffix === "build-records") body = { records: [] };
    else if (suffix.startsWith("telemetry/services/")) {
      const serviceID = suffix.slice("telemetry/services/".length);
      if (path === failedTelemetryPath) status = 503;
      else body = { services: [{ service_id: serviceID, health: "healthy", pod_count: 1, ready_pods: 1, last_seen_unix: 1785290400 }] };
    } else if (suffix === "incidents") body = { incidents: [] };
    await route.fulfill({ body: JSON.stringify(status === 503 ? { error: { message: "one telemetry source failed" } } : body), contentType: "application/json", status });
    active -= 1;
    completed += 1;
  });

  await page.goto("/?view=projects");
  await expect(page.locator(".projectRow")).toHaveCount(3);
  await expect(page.locator(".projectRow").filter({ hasText: "Project 3" }).locator('[data-label="Services"]')).toHaveText(String(servicesPerProject));
  await expect(page.locator(".projectRow").filter({ hasText: "Project 2" }).getByText("Unavailable", { exact: true })).toBeVisible();
  await expect.poll(() => completed).toBe(3 * (7 + servicesPerProject));
  expect(maximum).toBe(MAX_PROJECT_SUMMARY_REQUESTS);
  expect(switchCalls).toBe(0);
});

test("console and resource gate records unexpected failures and page errors", async ({ page }) => {
  await mockLocalAPI(page, "healthy");
  await page.goto("/?view=projects");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
  expect(collectedErrors(page)).toEqual([]);

  await page.route("**/api/local/unexpected-404", (route) => route.fulfill({ body: "{}", contentType: "application/json", status: 404 }));
  await page.evaluate(() => fetch("/api/local/unexpected-404"));
  await expect.poll(() => collectedErrors(page).some((error) => error.includes("HTTP 404") && error.includes("/api/local/unexpected-404"))).toBe(true);
  resetConsoleErrors(page);

  await page.route("**/api/local/unexpected-abort", (route) => route.abort("connectionfailed"));
  await page.evaluate(() => fetch("/api/local/unexpected-abort").catch(() => undefined));
  await expect.poll(() => collectedErrors(page).some((error) => error.includes("Request failed") && error.includes("/api/local/unexpected-abort"))).toBe(true);
  resetConsoleErrors(page);

  await page.evaluate(() => new Promise<void>((resolve) => {
    const script = document.createElement("script");
    script.src = "/unexpected-missing-asset.js";
    script.onload = () => resolve();
    script.onerror = () => resolve();
    document.head.append(script);
  }));
  await expect.poll(() => collectedErrors(page).some((error) => error.includes("unexpected-missing-asset.js"))).toBe(true);
  resetConsoleErrors(page);

  await page.evaluate(() => console.error("intentional application console error"));
  await expect.poll(() => collectedErrors(page)).toContainEqual("console.error: intentional application console error");
  resetConsoleErrors(page);

  await page.evaluate(() => { setTimeout(() => { throw new Error("intentional page error"); }, 0); });
  await expect.poll(() => collectedErrors(page)).toContainEqual("pageerror: intentional page error");
  resetConsoleErrors(page);
});

test("tabs, activity outcomes, service drawer, mobile drawer, and target sizes follow accessible patterns", async ({ page }) => {
  await mockLocalAPI(page, "healthy");
  await page.goto("/?project=proj-1&view=observability&tab=health");
  for (const tab of await page.getByRole("tab").all()) {
    const controls = await tab.getAttribute("aria-controls");
    expect(controls).toBeTruthy();
    const panel = page.locator(`#${controls}`);
    if (await tab.getAttribute("aria-selected") === "true") {
      await expect(panel).toHaveAttribute("role", "tabpanel");
      await expect(panel).toHaveAttribute("aria-labelledby", await tab.getAttribute("id") ?? "");
    }
  }
  await page.getByRole("tab", { name: "Health" }).focus();
  await page.keyboard.press("End");
  await expect(page.getByRole("tab", { name: "Incidents" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("tab", { name: "Incidents" })).toHaveAttribute("aria-selected", "true");

  await page.goto("/?project=proj-1&view=overview");
  await expect(page.getByText(/cancelled, 1 other/)).toBeVisible();
  await expect(page.getByRole("columnheader", { name: "Cancelled" })).toBeVisible();
  await expect(page.getByRole("row", { name: /2026-07-25 0 0 0 1 0 1/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /2026-07-26 0 0 0 0 1 1/ })).toBeVisible();

  await page.goto("/?project=proj-1&view=services");
  const service = page.locator(".serviceRow").first();
  await service.focus();
  await page.keyboard.press("Enter");
  const drawer = page.getByRole("dialog", { name: "api" });
  await expect(drawer).toBeVisible();
  await expect(page).toHaveURL(/service=api/);
  for (const section of ["Summary", "Runtime", "Delivery", "Dependencies", "Configuration"]) await expect(drawer.getByRole("heading", { name: section })).toBeVisible();
  await expect(drawer.getByText("Not reported by Local API.", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(service).toBeFocused();
  await service.click();
  await page.reload();
  await expect(page.getByRole("dialog", { name: "api" })).toBeVisible();
  await page.evaluate(() => {
    window.history.pushState({}, "", "/?project=proj-2&view=services");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page.locator(".detailDrawer")).toHaveCount(0);

  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?project=proj-1&view=overview");
  const menu = page.getByRole("button", { name: "Open navigation" });
  await menu.click();
  await expect(page.getByRole("button", { name: "Close navigation" }).last()).toBeFocused();
  expect(await page.locator("main").evaluate((element) => (element as HTMLElement).inert)).toBe(true);
  for (let index = 0; index < 12; index += 1) {
    await page.keyboard.press("Tab");
    expect(await page.evaluate(() => document.activeElement?.closest(".sidebar") !== null)).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(menu).toBeFocused();
  const undersized = await page.locator(".iconButton:visible, .projectSwitcher summary:visible, .sidebar a:visible, .accountMenu summary:visible").evaluateAll((elements) => elements.filter((element) => { const box = element.getBoundingClientRect(); return box.width < 40 || box.height < 40; }).map((element) => ({ tag: element.tagName, label: element.getAttribute("aria-label") || element.textContent, box: element.getBoundingClientRect().toJSON() })));
  expect(undersized).toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("bootstrap credential leaves the DOM before the request resolves", async ({ page }) => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => { release = resolve; });
  const submitted: Record<string, unknown>[] = [];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/local/projects/proj-1/bootstrap-sessions" && route.request().method() === "POST") {
      submitted.push(route.request().postDataJSON());
      await pending;
      await route.fulfill({ body: JSON.stringify({ id: "boot-new", status: "pending" }), contentType: "application/json", status: 200 });
      return;
    }
    await respond(route, "healthy");
  });
  const trigger = await openBootstrapReview(page);
  const secret = "credential-must-not-remain";
  const input = page.getByLabel("One-time SSH password");
  await input.fill(secret);
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(input).toHaveCount(0);
  await expect(page.getByText(secret, { exact: false })).toHaveCount(0);
  expect(await page.locator("body").textContent()).not.toContain(secret);
  expect(await browserStorage(page)).toEqual({ local: [], session: [] });
  expect(submitted).toEqual([{ role: "worker", public_host: "203.0.113.10", ssh_port: 22, ssh_username: "opsi", auth_method: "password", ssh_password: secret }]);
  release();
  await expect(page.getByText(/Bootstrap boot-new accepted/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(trigger).toBeFocused();
});

test("bootstrap failure requires a new credential and lifecycle exits clear it", async ({ page }) => {
  const bodies: Record<string, unknown>[] = [];
  await page.route("**/api/local/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/local/projects/proj-1/bootstrap-sessions" && route.request().method() === "POST") {
      bodies.push(route.request().postDataJSON());
      if (bodies.length === 1) {
        await route.fulfill({ body: JSON.stringify({ error: { message: "bootstrap unavailable" } }), contentType: "application/json", status: 503 });
      } else {
        await route.fulfill({ body: JSON.stringify({ id: "boot-retry", status: "pending" }), contentType: "application/json", status: 200 });
      }
      return;
    }
    await respond(route, "healthy");
  });
  expectHTTPFailure(page, { path: "/api/local/projects/proj-1/bootstrap-sessions", status: 503, method: "POST" });
  const trigger = await openBootstrapReview(page);
  const firstSecret = "first-secret-must-disappear";
  await page.getByLabel("One-time SSH password").fill(firstSecret);
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.locator(".errorBox")).toContainText("bootstrap unavailable");
  await expect(page.getByLabel("One-time SSH password")).toHaveValue("");
  await expect(page.getByRole("button", { name: "Retry same attempt" })).toBeDisabled();
  expect(await page.locator("body").textContent()).not.toContain(firstSecret);
  await page.getByLabel("One-time SSH password").fill("replacement-secret");
  await page.getByRole("button", { name: "Retry same attempt" }).click();
  await expect(page.getByText(/Bootstrap boot-retry accepted/)).toBeVisible();
  expect(bodies.map((body) => body.ssh_password)).toEqual([firstSecret, "replacement-secret"]);
  await page.getByRole("button", { name: "Close" }).click();
  await expect(trigger).toBeFocused();

  await openBootstrapReview(page);
  await page.getByLabel("One-time SSH password").fill("escape-secret");
  await page.keyboard.press("Escape");
  await expect(page.getByText("escape-secret", { exact: false })).toHaveCount(0);
  await expect(trigger).toBeFocused();

  await openBootstrapReview(page);
  await page.getByLabel("One-time SSH password").fill("cancel-secret");
  await page.getByRole("button", { name: "Cancel" }).click();
  expect(await page.locator("body").textContent()).not.toContain("cancel-secret");

  await openBootstrapReview(page);
  await page.getByLabel("One-time SSH password").fill("navigation-secret");
  await page.evaluate(() => {
    window.history.pushState({}, "", "/?project=proj-1&view=overview");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page.getByRole("dialog", { name: "bootstrap server" })).toHaveCount(0);
  expect(await page.locator("body").textContent()).not.toContain("navigation-secret");

  await openBootstrapReview(page);
  await page.getByLabel("One-time SSH password").fill("switch-secret");
  await page.evaluate(() => {
    window.history.pushState({}, "", "/?project=proj-2&view=overview");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(page.getByRole("heading", { name: "Payments" })).toBeVisible();
  expect(await page.locator("body").textContent()).not.toContain("switch-secret");
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
  else if (path.endsWith("/nodes")) body = { nodes: data.nodes };
  else if (path.endsWith("/services")) body = { services: data.services };
  else if (path.endsWith("/deployments")) body = { deployments: data.deployments };
  else if (/\/deployments\/[^/]+\/events$/.test(path)) body = { events: [] };
  else if (/\/bootstrap-sessions\/[^/]+\/events$/.test(path)) body = [];
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
  const degraded = scenario === "degraded" || scenario === "refreshed" || projectID === "proj-2";
  const servicePrefix = projectID === "proj-2" ? "payments-" : "";
  const services = empty ? [] : [
    { id: `${servicePrefix}api`, name: long ? "checkout-api-with-an-intentionally-long-production-service-name-that-must-wrap-safely" : `${servicePrefix}api`, type: "application", status: "ready", source_type: "image", replicas: 2, container_port: 8080, health_path: "/healthz", namespace: "opsi-prod" },
    { id: `${servicePrefix}worker`, name: `${servicePrefix}worker`, type: "application", status: "ready", source_type: "image", replicas: 2 },
    ...(scenario === "refreshed" && projectID === "proj-1" ? [{ id: "scheduler", name: "scheduler", type: "application", status: "ready", source_type: "image", replicas: 1 }] : []),
  ];
  const telemetry = services.map((service) => ({ service_id: service.id, health: degraded && service.id.endsWith("worker") ? "degraded" : "healthy", pod_count: 2, ready_pods: degraded && service.id.endsWith("worker") ? 1 : 2, last_seen_unix: 1785290400 }));
  const deployments = empty ? [] : [
    deployment(projectID, "dep-3", `${servicePrefix}worker`, "succeeded", "2026-07-29T03:00:00Z"),
    deployment(projectID, "dep-2", `${servicePrefix}api`, "failed", "2026-07-28T02:00:00Z"),
    deployment(projectID, "dep-1", `${servicePrefix}api`, "succeeded", "2026-07-27T01:00:00Z"),
    deployment(projectID, "dep-other", `${servicePrefix}api`, "mystery", "2026-07-26T01:00:00Z"),
    deployment(projectID, "dep-cancelled", `${servicePrefix}api`, "cancelled", "2026-07-25T01:00:00Z"),
  ];
  const builds = empty ? [] : [build(projectID, "api", "succeeded", "2026-07-29T01:00:00Z"), ...(scenario === "failed-build" ? [build(projectID, "worker", "failed", "2026-07-29T04:00:00Z")] : [])];
  const nodes = empty ? [] : [{ id: "node-1", name: "primary-node", role: "worker", status: "healthy", last_seen_at: "2026-07-29T04:00:00Z" }];
  const placement = { project_id: projectID, environments: [{ id: "env-1", project_id: projectID, name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: projectID, environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }], nodes: nodes.map((node) => ({ id: node.id, project_id: projectID, runtime_id: "runtime-1", status: node.status, last_seen_at: node.last_seen_at })), agents: empty ? [] : [{ id: "agent-1", project_id: projectID, runtime_id: "runtime-1", node_id: "node-1", status: "active", capabilities: { deploy: true } }], services: services.map((service) => ({ id: service.id, project_id: projectID, key: service.name })) };
  return {
    projects: [{ id: "proj-1", org_id: "org-1", name: long ? "Checkout Platform With A Very Long Project Name That Must Never Break The Workspace Layout" : "Checkout Platform", slug: "checkout", status: "ready" }, { id: "proj-2", org_id: "org-1", name: "Payments", slug: "payments", status: "ready" }, { id: "proj-3", org_id: "org-1", name: "Analytics", slug: "analytics", status: "ready" }],
    readiness: { project_id: projectID, status: "ready", can_deploy: !empty },
    nodes,
    services,
    deployments,
    builds,
    telemetry,
    incidents: degraded ? [{ incident_id: "inc-1", project_id: projectID, service_id: `${servicePrefix}worker`, status: "open", severity: "warning", anomaly_type: "readiness", created_at_unix: 1785290000 }] : [],
    placement,
    topology: { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: projectID, revision: 1, state_hash: "state", plan_hash: "plan", created_by: "user", applied_by: "user", created_at: "2026-07-27T01:00:00Z", applied_at: "2026-07-27T01:00:00Z", assignments: services.map((service) => ({ service_key: service.name, environment_id: "env-1", runtime_id: "runtime-1", replicas: 2, cpu_request_millicores: 100, memory_request_bytes: 134217728, exposure: { mode: "none" } })) },
    support: { generated_at: "2026-07-29T04:00:00Z", readiness: { project_id: projectID, status: "ready", can_deploy: !empty }, counts: { nodes: nodes.length, healthy_nodes: nodes.length, services: services.length, deployment_jobs: deployments.length, failed_deployments: 1, bootstrap_sessions: 0, open_bootstrap_jobs: 0, audit_events: 0 }, dashboard: { title: "Opsi", datasource: "local", refresh: "30s", panels: [] }, signals: [], active_alerts: [], configured_alerts: [], production_gates: [], runbooks: [], break_glass_policy: { time_limited: true, approval_required: true, reason_required: true, audited: true, secret_reveal_by_default: false, owner_notification: "required" } },
  };
}

function deployment(projectID: string, id: string, serviceID: string, status: string, createdAt: string) { return { id, project_id: projectID, service_id: serviceID, status, created_at: createdAt, updated_at: createdAt, current_digest: `sha256:${id.padEnd(64, "a")}` }; }
function build(projectID: string, serviceKey: string, status: string, createdAt: string) { return { schema_version: "opsi.build_record/v1", id: `build-${serviceKey}-${status}`, project_id: projectID, repository_id: 101, repository_owner_id: 42, active_binding_id: `binding-${serviceKey}`, service_id: serviceKey, service_key: serviceKey, created_at: createdAt, workload: { issuer: "https://token.actions.githubusercontent.com", subject: "repo:example/app", repository_id: 101, repository_owner_id: 42, ref: "refs/heads/main", sha: "abcdef0123456789", event_name: "push", workflow: "build", workflow_ref: "example/app/.github/workflows/build.yml@refs/heads/main", run_id: 7, run_attempt: 1 }, build: { config_hash: "config", plan_hash: "plan", platform: "linux/amd64", oci_repository: `registry.example.test/app/${serviceKey}`, oci_digest: `sha256:${serviceKey.padEnd(64, "b")}`, status } }; }

async function openBootstrapReview(page: Page) {
  await page.goto("/?project=proj-1&view=infrastructure&tab=bootstrap");
  const trigger = page.getByRole("button", { name: "Add server" });
  await trigger.click();
  const setup = page.getByRole("dialog", { name: "Add server" });
  await setup.getByLabel("Role").selectOption("worker");
  await setup.getByLabel("SSH host or IP").fill("203.0.113.10");
  await setup.getByLabel("SSH port").fill("22");
  await setup.getByLabel("SSH username").fill("opsi");
  await setup.getByLabel("Authentication").selectOption("password");
  await setup.getByRole("button", { name: "Review bootstrap request" }).click();
  const review = page.getByRole("dialog", { name: "bootstrap server" });
  await expect(review).toBeVisible();
  expect(await page.evaluate(() => document.activeElement?.closest("dialog") !== null)).toBe(true);
  for (let index = 0; index < 8; index += 1) {
    await page.keyboard.press("Tab");
    expect(await page.evaluate(() => document.activeElement?.closest("dialog") !== null)).toBe(true);
  }
  return trigger;
}

async function browserStorage(page: Page) {
  return page.evaluate(() => ({ local: Object.keys(localStorage), session: Object.keys(sessionStorage) }));
}
