import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 45_000,
  fullyParallel: false,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:19881",
    trace: "retain-on-failure",
    channel: "chromium",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "OPSI_UI_E2E_SERVER=1 GOTOOLCHAIN=go1.26.4 go test ./internal/commands -run '^TestManualParityServer$' -count=1 -v",
    cwd: "..",
    url: "http://127.0.0.1:19881/health",
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
