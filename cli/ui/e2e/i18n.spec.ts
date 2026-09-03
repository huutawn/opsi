import { expect, test, type Page, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => {
  watchConsoleErrors(page);
  await mockConsoleAPI(page);
});

test.afterEach(async ({ page }) => {
  expectNoConsoleErrors(page);
});

test("Default initial load uses English and has lang=en", async ({ page }) => {
  await page.goto("/?view=settings&tab=general");

  // Document language attribute
  await expect(page.locator("html")).toHaveAttribute("lang", "en");

  // Settings page title and tabs in English
  await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "General", exact: true })).toBeVisible();

  // Language selector is visible and selects English
  const select = page.getByLabel("Language");
  await expect(select).toBeVisible();
  await expect(select).toHaveValue("en");

  // Navigation items in English
  await expect(page.getByRole("link", { name: "Home" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Projects" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
});

test("Switching language to Vietnamese in Settings updates UI immediately and persists to localStorage", async ({ page }) => {
  await page.goto("/?view=settings&tab=general");

  // Select Vietnamese
  const langSelect = page.getByLabel("Language");
  await langSelect.selectOption("vi");

  // Immediate document lang attribute update
  await expect(page.locator("html")).toHaveAttribute("lang", "vi");

  // Immediate text updates in Settings view
  await expect(page.getByRole("heading", { name: "Cài đặt", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Chung", exact: true })).toBeVisible();

  // Immediate navigation updates
  await expect(page.getByRole("link", { name: "Trang chủ" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Dự án" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Cài đặt" })).toBeVisible();

  // Verify localStorage contains the selection
  const stored = await page.evaluate(() => window.localStorage.getItem("opsi:locale"));
  expect(stored).toBe("vi");

  // Reload the page to verify persistence
  await page.reload();

  // Preserved after reload
  await expect(page.locator("html")).toHaveAttribute("lang", "vi");
  await expect(page.getByRole("heading", { name: "Cài đặt", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Chung", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Trang chủ" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Dự án" })).toBeVisible();
});

test("Projects and workspace views render Vietnamese translations and responsive layouts", async ({ page }) => {
  // Set locale preference to Vietnamese in localStorage before navigation
  await page.addInitScript(() => {
    window.localStorage.setItem("opsi:locale", "vi");
  });

  await page.goto("/?view=projects");

  await expect(page.locator("html")).toHaveAttribute("lang", "vi");
  await expect(page.getByRole("heading", { name: "Dự án", exact: true })).toBeVisible();
  await expect(page.getByPlaceholder("Tìm kiếm dự án theo tên hoặc slug...")).toBeVisible();

  // Responsive layout verification at all standard breakpoints with Vietnamese strings
  for (const width of [320, 768, 1024, 1440]) {
    await page.setViewportSize({ width, height: 844 });
    const fitsInViewport = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(fitsInViewport).toBe(true);
  }
});

test("First load with Vietnamese browser locale automatically selects Vietnamese", async ({ page, context }) => {
  // Set browser language to Vietnamese
  await context.setExtraHTTPHeaders({ "Accept-Language": "vi-VN,vi;q=0.9" });
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "languages", { get: () => ["vi-VN", "vi"], configurable: true });
    Object.defineProperty(navigator, "language", { get: () => "vi-VN", configurable: true });
  });

  await page.goto("/?view=settings&tab=general");

  // Automatically detected Vietnamese
  await expect(page.locator("html")).toHaveAttribute("lang", "vi");
  await expect(page.getByRole("heading", { name: "Cài đặt", exact: true })).toBeVisible();
  const select = page.getByLabel("Ngôn ngữ");
  await expect(select).toBeVisible();
  await expect(select).toHaveValue("vi");
});

async function mockConsoleAPI(page: Page) {
  await page.unroute("**/api/local/**").catch(() => undefined);
  await page.route("**/api/local/**", async (route: Route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path === "/api/local/session") {
      return json(route, {
        authenticated: true,
        cloud_connected: "ok",
        agent_connected: "ok",
        org_id: "org-1",
        project_id: "proj-1",
        token_status: "Active in OS store",
        role: "owner",
      });
    }

    if (path === "/api/local/projects") {
      return json(route, {
        projects: [
          { id: "proj-1", name: "Checkout Service", slug: "checkout-service", status: "ready" },
          { id: "proj-2", name: "Billing API", slug: "billing-api", status: "ready" },
        ],
      });
    }

    if (path.endsWith("/support")) {
      return json(route, {
        version: "v2.4.1",
        revision: "a1b2c3d",
        go_version: "go1.26.4",
        ui_assets: "embedded",
        cloud_authority: "cloud.opsi.dev",
        agent_tls_pinned: true,
        backend_gaps: [],
      });
    }

    return json(route, {});
  });
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    body: JSON.stringify(body),
    contentType: "application/json",
    status,
  });
}
