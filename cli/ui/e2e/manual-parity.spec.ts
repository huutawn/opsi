import { expect, test } from "@playwright/test";
import { expectHTTPFailure, expectNoConsoleErrors, expectRequestFailure, watchConsoleErrors } from "./console-errors";

const localOrigin = "http://127.0.0.1:19881";
const controlURL = "http://127.0.0.1:19882/__control";

test.beforeEach(async () => {
  await fetch(`${controlURL}?mode=&agent=up`);
});
test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("manual Local UI parity stays behind the Local backend", async ({ page }) => {
  const browserAuthorities = new Set<string>();
  page.on("request", (request) => browserAuthorities.add(new URL(request.url()).origin));
  expectHTTPFailure(page, { path: "/api/local/repository/config", status: 422, method: "GET" });
  for (const projectID of ["proj-1", "proj-2"]) {
    expectRequestFailure(page, { path: `/api/local/projects/${projectID}/incidents`, status: 200, method: "GET", errorText: "net::ERR_ABORTED" });
  }

  await page.goto("/");
  await expect(page).toHaveTitle("Opsi Console");
  await page.getByRole("button", { name: "Browse projects" }).click();
  await expect(page.getByRole("link", { name: /Parity Project/ })).toBeVisible();
  await expect(page.locator(".projectRow [role=status]")).toHaveCount(0);
  await page.getByRole("link", { name: /Parity Project/ }).click();
  await expect(page.locator(".breadcrumb")).toContainText("Parity Project");

  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: "Browse all projects", exact: true }).click();
  await page.getByRole("button", { name: "New project", exact: true }).click();
  const createDialog = page.getByRole("dialog", { name: "Create project" });
  const dialogBox = await createDialog.boundingBox();
  const viewport = page.viewportSize();
  expect(dialogBox).not.toBeNull();
  expect(viewport).not.toBeNull();
  expect(Math.abs(dialogBox!.x + dialogBox!.width / 2 - viewport!.width / 2)).toBeLessThan(2);
  expect(Math.abs(dialogBox!.y + dialogBox!.height / 2 - viewport!.height / 2)).toBeLessThan(2);
  await page.getByLabel("Name").fill("Created Project");
  await page.getByLabel("Slug").fill("created");
  await page.getByRole("button", { name: "Review project" }).click();
  await expect(page.getByRole("dialog", { name: /create project/i })).toBeVisible();
  await page.getByText("Technical details", { exact: true }).click();
  await expect(page.getByText("Idempotency key", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Project proj-2 created by the Local backend/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page).toHaveURL(/project=proj-2&view=topology/);
  await expect(page.locator(".breadcrumb")).toContainText("Created Project");

  await page.getByRole("link", { name: "Infrastructure", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Execution Capacity (Servers)" })).toBeVisible();

  await page.getByRole("link", { name: "Delivery", exact: true }).click();
  await expect(page.getByText("Current Release", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Repository Ownership & Service Bindings" })).toBeVisible();
  await expect(page.getByText("opsi-test/api", { exact: true }).first()).toBeVisible();

  await page.getByRole("link", { name: "Topology", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Topology", exact: true })).toBeVisible();
  await expect(page.locator(".topologyContextBar").getByText("Server Ready", { exact: true })).toBeVisible();

  await page.getByRole("link", { name: "Delivery", exact: true }).click();
  await page.getByRole("tab", { name: "Builds", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Build Records" })).toBeVisible();
  await expect(page.getByText("build-1", { exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Deployments", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Deployments" })).toBeVisible();

  await page.getByRole("tab", { name: "Exposure", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Exposure" })).toBeVisible();

  await page.getByRole("link", { name: "Security", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Security Overview" })).toBeVisible();
  await page.getByRole("tab", { name: "Audit", exact: true }).click();
  await expect(page.getByText("req-e2e-1", { exact: false }).first()).toBeVisible();
  await page.getByRole("tab", { name: "Access & Identities", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Access & Identities" })).toBeVisible();

  await page.getByRole("link", { name: "Observability", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Observability Overview" })).toBeVisible();
  await page.getByRole("tab", { name: "Applications", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Applications Runtime" })).toBeVisible();
  await page.getByRole("tab", { name: "Servers", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Server Observability" })).toBeVisible();
  await page.getByRole("tab", { name: "Managed Resources", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Managed Resources Observability" })).toBeVisible();
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "System", exact: true }).click();
  await page.getByText("Capability limits", { exact: true }).click();
  await expect(page.getByText("organization listing", { exact: true })).toBeVisible();

  const storage = await page.evaluate(async () => ({
    local: Object.keys(Reflect.get(window, "local" + "Storage") as Storage),
    session: Object.keys(Reflect.get(window, "session" + "Storage") as Storage),
    databases: await (Reflect.get(window, "indexed" + "DB") as IDBFactory).databases(),
    cookies: Reflect.get(document, "coo" + "kie") as string,
  }));
  expect(storage).toEqual({ local: [], session: [], databases: [], cookies: "" });
  expect([...browserAuthorities]).toEqual([localOrigin]);

  await page.waitForLoadState("networkidle");
  await fetch(`${controlURL}?agent=down`);
  await page.reload();
  await page.getByRole("link", { name: "Topology", exact: true }).click();
  await expect(page.locator(".topologyContextBar")).toBeVisible();

  await page.waitForLoadState("networkidle");
  await fetch(`${controlURL}?mode=cloud-outage&agent=up`);
  await page.reload();
  await expect(page.getByText("Cloud is unavailable", { exact: false })).toBeVisible();

  await page.waitForLoadState("networkidle");
  await fetch(`${controlURL}?mode=session-expired&agent=up`);
  await page.reload();
  await expect(page.getByText("saved Cloud session expired", { exact: false })).toBeVisible();

  await page.waitForLoadState("networkidle");
  await fetch(`${controlURL}?mode=&agent=up`);
  await page.reload();
  await page.waitForLoadState("networkidle");
  await expect(page.getByLabel("Current context").getByText("Created Project", { exact: true })).toBeVisible();
  expect([...browserAuthorities]).toEqual([localOrigin]);
});
