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
  await expect(page.getByText("Parity Project", { exact: true })).toBeVisible();
  await expect(page.getByText("Cloud ok", { exact: false })).toBeVisible();

  await page.getByLabel("Name").fill("Created Project");
  await page.getByLabel("Slug").fill("created");
  await page.getByRole("button", { name: "Create project" }).click();
  await expect(page.getByRole("dialog", { name: /create project/i })).toBeVisible();
  await expect(page.getByText("Idempotency", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/Project proj-2 created by the Local backend/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.getByText("Created Project", { exact: true }).first()).toBeVisible();

  await page.getByRole("button", { name: "Runtime", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Runtime inventory" })).toBeVisible();
  await expect(page.getByText("Primary", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Servers / Nodes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Node list" })).toBeVisible();
  await expect(page.getByText("agent-node", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "GitHub", exact: true }).click();
  await expect(page.getByRole("heading", { name: "GitHub App connection" })).toBeVisible();
  await expect(page.getByText("opsi-test/api", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Topology", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Manual placement wizard" })).toBeVisible();

  await page.getByRole("button", { name: "Build Records", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Trusted build records" })).toBeVisible();
  await expect(page.getByText("build-1", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Deployments", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Immutable deployment" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Exposure rollout" })).toBeVisible();

  await page.getByRole("button", { name: "Secrets", exact: true }).click();
  await expect(page.getByText("Secret metadata/listing is a BACKEND_GAP", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Review TOTP setup" }).click();
  await page.getByRole("button", { name: "Confirm and submit" }).click();
  await expect(page.getByText(/TOTP setup created by the Agent/)).toBeVisible();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(page.getByText("JBSWY3DPEHPK3PXP", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Metrics", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Agent telemetry" })).toBeVisible();
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  await expect(page.getByText("Untrusted Agent content", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Incidents", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Incident inventory" })).toBeVisible();
  await page.getByRole("button", { name: "Audit", exact: true }).click();
  await expect(page.getByText("req-e2e-1", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Version and configuration" })).toBeVisible();
  await expect(page.getByText("organization listing", { exact: true })).toBeVisible();

  const storage = await page.evaluate(async () => ({
    local: Object.keys(window["local" + "Storage"]),
    session: Object.keys(window["session" + "Storage"]),
    databases: await window["indexed" + "DB"].databases(),
    cookies: document["coo" + "kie"],
  }));
  expect(storage).toEqual({ local: [], session: [], databases: [], cookies: "" });
  expect([...browserAuthorities]).toEqual([localOrigin]);

  await fetch(`${controlURL}?agent=down`);
  await page.reload();
  await page.getByRole("button", { name: "Runtime", exact: true }).click();
  await expect(page.getByText("AGENT_UNAVAILABLE", { exact: true })).toBeVisible();
  await expect(page.getByText("Primary", { exact: true })).toBeVisible();

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
