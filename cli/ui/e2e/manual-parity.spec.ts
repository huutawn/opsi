import { expect, test } from "@playwright/test";

const localOrigin = "http://127.0.0.1:19881";
const controlURL = "http://127.0.0.1:19882/__control";

test.beforeEach(async () => {
  await fetch(`${controlURL}?mode=&agent=up`);
});

test("manual Local UI parity stays behind the Local backend", async ({ page }) => {
  const browserAuthorities = new Set<string>();
  page.on("request", (request) => browserAuthorities.add(new URL(request.url()).origin));

  await page.goto("/");
  await expect(page).toHaveTitle("Opsi Console");
  await page.getByRole("button", { name: "Browse projects" }).click();
  await expect(page.getByRole("link", { name: /Parity Project/ })).toBeVisible();
  await page.getByRole("link", { name: /Parity Project/ }).click();
  await expect(page.getByRole("heading", { name: "Parity Project" })).toBeVisible();

  await page.getByLabel("Switch project").click();
  await page.getByRole("link", { name: "Browse all projects", exact: true }).click();
  await page.getByRole("button", { name: "New project", exact: true }).click();
  await page.getByLabel("Name").fill("Created Project");
  await page.getByLabel("Slug").fill("created");
  await page.getByRole("button", { name: "Review project" }).click();
  await expect(page.getByRole("dialog", { name: /create project/i })).toBeVisible();
  await page.getByText("Technical details", { exact: true }).click();
  await expect(page.getByText("Idempotency key", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Project proj-2 created by the Local backend/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.getByText("Created Project", { exact: true }).first()).toBeVisible();

  await page.getByRole("link", { name: "Infrastructure", exact: true }).click();
  await page.getByRole("tab", { name: "Runtimes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Runtimes" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Primary/ })).toBeVisible();

  await page.getByRole("tab", { name: "Nodes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Nodes" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "agent-node" })).toBeVisible();

  await page.getByRole("link", { name: "Delivery", exact: true }).click();
  await expect(page.getByText("Current Release", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Repository Ownership & Service Bindings" })).toBeVisible();
  await expect(page.getByText("opsi-test/api", { exact: true }).first()).toBeVisible();

  await page.getByRole("link", { name: "Infrastructure", exact: true }).click();
  await page.getByRole("tab", { name: "Topology", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Factual topology" })).toBeVisible();

  await page.getByRole("link", { name: "Delivery", exact: true }).click();
  await page.getByRole("tab", { name: "Builds", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Build Records" })).toBeVisible();
  await expect(page.getByText("build-1", { exact: true })).toBeVisible();

  await page.getByRole("tab", { name: "Deployments", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Deployments" })).toBeVisible();

  await page.getByRole("tab", { name: "Exposure", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Exposure" })).toBeVisible();

  await page.getByRole("link", { name: "Security", exact: true }).click();
  await page.getByRole("tab", { name: "Secrets", exact: true }).click();
  await expect(page.getByText(/Secret metadata\/listing is a backend capability gap/)).toBeVisible();
  await page.getByLabel("Operation").selectOption("totp");
  await page.getByRole("button", { name: "Review TOTP setup" }).click();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByRole("dialog", { name: "Sensitive content" })).toBeVisible();
  await expect(page.getByText(/JBSWY3DPEHPK3PXP/)).toBeVisible();
  await page.getByRole("button", { name: "Hide now" }).click();
  await expect(page.getByText(/JBSWY3DPEHPK3PXP/)).toHaveCount(0);

  await page.getByRole("link", { name: "Observability", exact: true }).click();
  await page.getByRole("tab", { name: "Metrics", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();
  await page.getByRole("tab", { name: "Logs", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Logs", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Incidents", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Incidents" })).toBeVisible();
  await page.getByRole("link", { name: "Security", exact: true }).click();
  await page.getByRole("tab", { name: "Audit", exact: true }).click();
  await expect(page.getByText("req-e2e-1", { exact: false }).first()).toBeVisible();
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

  await fetch(`${controlURL}?agent=down`);
  await page.reload();
  await page.getByRole("link", { name: "Infrastructure", exact: true }).click();
  await page.getByRole("tab", { name: "Runtimes", exact: true }).click();
  await expect(page.getByText(/Cloud topology facts remain visible/)).toBeVisible();
  await expect(page.getByRole("button", { name: /Primary/ })).toBeVisible();

  await fetch(`${controlURL}?mode=cloud-outage&agent=up`);
  await page.reload();
  await expect(page.getByText("Cloud is unavailable", { exact: false })).toBeVisible();

  await fetch(`${controlURL}?mode=session-expired&agent=up`);
  await page.reload();
  await expect(page.getByText("saved Cloud session expired", { exact: false })).toBeVisible();

  await fetch(`${controlURL}?mode=&agent=up`);
  await page.reload();
  await expect(page.getByText("Created Project", { exact: true }).first()).toBeVisible();
  expect([...browserAuthorities]).toEqual([localOrigin]);
});
